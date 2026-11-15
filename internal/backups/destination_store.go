package backups

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/backups/destinations"
	"github.com/g4f4r0/tender/internal/secret"
	"github.com/g4f4r0/tender/internal/server"
)

// Hidden is what a secret shows as in a destination's config.
const Hidden = "***"

// errNoKey is what a process without TENDER_ENCRYPTION_KEY answers when asked for credentials.
var errNoKey = &server.Error{Status: http.StatusServiceUnavailable,
	Detail: "This process has no TENDER_ENCRYPTION_KEY, so it can't store or read destination credentials. Set it to the hub's key and run the command again."}

func (s *Store) seal(v string) ([]byte, error) {
	if len(s.key) == 0 {
		return nil, errNoKey
	}
	b, err := secret.Seal(s.key, []byte(v))
	if err != nil {
		return nil, fmt.Errorf("seal destination credentials: %w", err)
	}
	return b, nil
}

func (s *Store) open(sealed []byte) (string, error) {
	if len(s.key) == 0 {
		return "", errNoKey
	}
	v, err := secret.Open(s.key, sealed)
	if err != nil {
		return "", fmt.Errorf("open destination credentials: %w. Start the hub with the TENDER_ENCRYPTION_KEY it first stored them with", err)
	}
	return string(v), nil
}

// errPasswordFixed refuses a change of the repository password.
var errPasswordFixed = server.Invalid("The password can't change: every repository at this destination is encrypted with it. " +
	"Add a new destination with the new password and move the machines to it.")

// errTypeFixed refuses a change of a destination's type.
var errTypeFixed = server.Invalid("The type can't change: the repositories are where the type put them. Add a new destination of the other type and move the machines to it.")

// destinationType is the type called name, or a 422 that lists the types.
func destinationType(name string) (destinations.Type, error) {
	t, ok := destinations.Get(name)
	if !ok {
		return nil, server.Invalid("type must be one of %s.", strings.Join(destinations.Names(), ", "))
	}
	return t, nil
}

// row is a destination as the table holds it.
type row struct {
	Destination
	config   map[string]any    // without the secrets
	secrets  map[string]string // field to sealed value, in hex
	password []byte            // sealed
}

const destinationColumns = `d.id, d.name, d.type, d.config, d.secrets, d.password, d.location, d.created_at, d.updated_at`

func scanRow(r scanner) (row, error) {
	var d row
	var config, secrets string
	var created, updated int64
	if err := r.Scan(&d.ID, &d.Name, &d.Type, &config, &secrets, &d.password, &d.Location, &created, &updated); err != nil {
		return d, err
	}
	d.CreatedAt, d.UpdatedAt = at(created), at(updated)
	if err := json.Unmarshal([]byte(config), &d.config); err != nil {
		return d, fmt.Errorf("destination %q has a damaged config: %w", d.Name, err)
	}
	if err := json.Unmarshal([]byte(secrets), &d.secrets); err != nil {
		return d, fmt.Errorf("destination %q has damaged secrets: %w", d.Name, err)
	}
	d.Config = redact(d.Type, d.config, d.secrets)
	return d, nil
}

// redact is the config people see: the plain fields, and each secret as "***" with <field>_set.
func redact(typ string, config map[string]any, secrets map[string]string) map[string]any {
	out := maps.Clone(config)
	if out == nil {
		out = map[string]any{}
	}
	if t, ok := destinations.Get(typ); ok {
		for _, f := range t.Secrets() {
			_, set := secrets[f]
			if set {
				out[f] = Hidden
			}
			out[f+"_set"] = set
		}
	}
	return out
}

// plain returns the destination's whole config with its secrets opened.
func (s *Store) plain(d row) (map[string]any, error) {
	out := maps.Clone(d.config)
	if out == nil {
		out = map[string]any{}
	}
	for f, h := range d.secrets {
		sealed, err := hex.DecodeString(h)
		if err != nil {
			return nil, fmt.Errorf("destination %q: secret %s is damaged", d.Name, f)
		}
		v, err := s.open(sealed)
		if err != nil {
			return nil, err
		}
		out[f] = v
	}
	return out, nil
}

// split checks a whole config for its type and seals its secrets. It returns the plain fields as
// JSON, the sealed secrets as JSON and the location.
func (s *Store) split(t destinations.Type, config map[string]any) (plainJSON, secretsJSON, location string, err error) {
	clean, err := t.Check(config)
	if err != nil {
		return "", "", "", err
	}
	plainFields, sealed := map[string]any{}, map[string]string{}
	for f, v := range clean {
		if !destinations.IsSecret(t, f) {
			plainFields[f] = v
			continue
		}
		text, _ := v.(string)
		if text == "" {
			continue
		}
		b, err := s.seal(text)
		if err != nil {
			return "", "", "", err
		}
		sealed[f] = hex.EncodeToString(b)
	}
	p, err := json.Marshal(plainFields)
	if err != nil {
		return "", "", "", err
	}
	q, err := json.Marshal(sealed)
	if err != nil {
		return "", "", "", err
	}
	return string(p), string(q), t.Location(clean), nil
}

// AddDestination stores a destination and its secrets, sealed. Without a password it makes one
// and returns it in the result: that is the only time anyone can read it.
func (s *Store) AddDestination(ctx context.Context, in DestinationInput) (DestinationCreated, error) {
	var out DestinationCreated
	if in.Type == "" {
		in.Type = "s3"
	}
	if !server.ValidName(in.Name) {
		return out, server.Invalid("Destination names are lowercase letters, digits and hyphens, up to 63 characters.")
	}
	t, err := destinationType(in.Type)
	if err != nil {
		return out, err
	}
	if in.Config == nil {
		return out, server.Invalid("config is required: the %s destination's %s.", in.Type, strings.Join(t.Fields(), ", "))
	}
	for f, v := range in.Config {
		if destinations.IsSecret(t, f) && v == Hidden {
			return out, server.Invalid("config.%s is \"***\", which is how the hub hides a secret. Give the secret itself.", f)
		}
	}
	plainJSON, secretsJSON, location, err := s.split(t, in.Config)
	if err != nil {
		return out, err
	}
	if in.Password == "" {
		p, err := newPassword()
		if err != nil {
			return out, err
		}
		in.Password, out.Password = p, p
	}
	password, err := s.seal(in.Password)
	if err != nil {
		return DestinationCreated{}, err
	}

	now := time.Now().Unix()
	res, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO backup_destinations (name, type, config, secrets, password, location, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		in.Name, in.Type, plainJSON, secretsJSON, password, location, now, now)
	if err != nil {
		return DestinationCreated{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return DestinationCreated{}, err
	}
	if n == 0 {
		return DestinationCreated{}, server.Conflict("A destination with this name or this location (%s) already exists. Choose another name, or use the existing one.", location)
	}
	out.Destination, err = s.Destination(ctx, in.Name)
	return out, err
}

// PatchDestination merges p into a destination's config: a field with a value replaces it, null
// removes it, and a secret given as "***" stays as it is.
func (s *Store) PatchDestination(ctx context.Context, name string, p DestinationPatch) (Destination, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Destination{}, err
	}
	defer func() { _ = tx.Rollback() }()

	d, err := scanRow(tx.QueryRowContext(ctx, `SELECT `+destinationColumns+` FROM backup_destinations d WHERE d.name = ?`, name))
	if err != nil {
		return Destination{}, notFoundIfNone(err, "Destination %q doesn't exist.", name)
	}
	t, err := destinationType(d.Type)
	if err != nil {
		return Destination{}, err
	}
	config, err := s.plain(d)
	if err != nil {
		return Destination{}, err
	}
	for f, v := range p.Config {
		switch {
		case v == nil:
			delete(config, f)
		case destinations.IsSecret(t, f) && v == Hidden:
		default:
			config[f] = v
		}
	}
	plainJSON, secretsJSON, location, err := s.split(t, config)
	if err != nil {
		return Destination{}, err
	}
	var taken int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM backup_destinations WHERE location = ? AND id != ?`, location, d.ID).Scan(&taken); err != nil {
		return Destination{}, err
	}
	if taken > 0 {
		return Destination{}, server.Conflict("Another destination already uses %s. Use that one, or choose another location.", location)
	}
	_, err = tx.ExecContext(ctx, `UPDATE backup_destinations SET config = ?, secrets = ?, location = ?, updated_at = ? WHERE id = ?`,
		plainJSON, secretsJSON, location, time.Now().Unix(), d.ID)
	if err != nil {
		return Destination{}, err
	}
	out, err := scanRow(tx.QueryRowContext(ctx, `SELECT `+destinationColumns+` FROM backup_destinations d WHERE d.id = ?`, d.ID))
	if err != nil {
		return Destination{}, err
	}
	return out.Destination, tx.Commit()
}

// DeleteDestination removes a destination that no machine uses. Its repositories stay where they are.
func (s *Store) DeleteDestination(ctx context.Context, name string) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM backup_destinations WHERE name = ?`, name).Scan(&id); err != nil {
		return notFoundIfNone(err, "Destination %q doesn't exist.", name)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT m.name FROM backup_settings s JOIN machines m ON m.id = s.machine_id WHERE s.destination_id = ? ORDER BY m.name`, id)
	if err != nil {
		return err
	}
	var users []string
	for rows.Next() {
		var machine string
		if err := rows.Scan(&machine); err != nil {
			return errors.Join(err, rows.Close())
		}
		users = append(users, machine)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(users) > 0 {
		return server.Conflict("Destination %q is used by %s. Stop their backups first (delete their backup settings), then remove the destination.",
			name, strings.Join(users, ", "))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM backup_destinations WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// CheckKey is an error unless the store's key opens every secret already stored. The hub and
// "tender mcp" run it at start, so a wrong key stops them there instead of failing every daemon
// later, or storing new secrets that the hub can't open.
func (s *Store) CheckKey(ctx context.Context) error {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT `+destinationColumns+` FROM backup_destinations d ORDER BY d.id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		d, err := scanRow(rows)
		if err != nil {
			return err
		}
		if _, err := s.plain(d); err != nil {
			return fmt.Errorf("destination %q: %w", d.Name, err)
		}
		if _, err := s.open(d.password); err != nil {
			return fmt.Errorf("destination %q: %w", d.Name, err)
		}
	}
	return rows.Err()
}

// Destination returns the destination called name.
func (s *Store) Destination(ctx context.Context, name string) (Destination, error) {
	d, err := scanRow(s.db.Read.QueryRowContext(ctx, `SELECT `+destinationColumns+` FROM backup_destinations d WHERE d.name = ?`, name))
	return d.Destination, notFoundIfNone(err, "Destination %q doesn't exist.", name)
}

// Destinations returns every destination by name.
func (s *Store) Destinations(ctx context.Context) ([]Destination, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT `+destinationColumns+` FROM backup_destinations d ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Destination{}
	for rows.Next() {
		d, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d.Destination)
	}
	return out, rows.Err()
}

// Target returns the destination called name with its secrets and password opened, for the daemon
// that tests it.
func (s *Store) Target(ctx context.Context, name string) (Target, error) {
	d, err := scanRow(s.db.Read.QueryRowContext(ctx, `SELECT `+destinationColumns+` FROM backup_destinations d WHERE d.name = ?`, name))
	if err != nil {
		return Target{}, notFoundIfNone(err, "Destination %q doesn't exist.", name)
	}
	config, err := s.plain(d)
	if err != nil {
		return Target{}, err
	}
	password, err := s.open(d.password)
	if err != nil {
		return Target{}, err
	}
	return Target{Name: d.Name, Type: d.Type, Config: config, Password: password}, nil
}

// Settings returns a machine's settings, or a NotFound error before they are set.
func (s *Store) Settings(ctx context.Context, machineID int64) (Settings, error) {
	st, _, err := s.loadSettings(ctx, machineID)
	return st, err
}

func (s *Store) loadSettings(ctx context.Context, machineID int64) (Settings, row, error) {
	var st Settings
	var created, updated int64
	var schedule string
	var retention int
	var id int64
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT destination_id, schedule, retention_days, created_at, updated_at FROM backup_settings WHERE machine_id = ?`, machineID).
		Scan(&id, &schedule, &retention, &created, &updated)
	if err != nil {
		return st, row{}, noSettingsIfNone(err)
	}
	d, err := scanRow(s.db.Read.QueryRowContext(ctx, `SELECT `+destinationColumns+` FROM backup_destinations d WHERE d.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) { // removed between the two reads; RESTRICT makes that a race only
		return st, row{}, noSettingsIfNone(err)
	}
	if err != nil {
		return st, row{}, err
	}
	st = Settings{Destination: d.Destination, Schedule: schedule, RetentionDays: retention, CreatedAt: at(created), UpdatedAt: at(updated)}
	return st, d, nil
}

// DaemonSettings returns a machine's settings with the credentials its daemon needs: the
// destination's whole config and the password, and for an s3 destination its place and keys as
// daemons before v0.3 read them. Only the machine's own daemon may be given them.
func (s *Store) DaemonSettings(ctx context.Context, machineID int64) (DaemonSettings, error) {
	st, d, err := s.loadSettings(ctx, machineID)
	if err != nil {
		return DaemonSettings{}, err
	}
	config, err := s.plain(d)
	if err != nil {
		return DaemonSettings{}, err
	}
	password, err := s.open(d.password)
	if err != nil {
		return DaemonSettings{}, err
	}
	st.Credentials = &Credentials{Password: password, Config: config}
	out := DaemonSettings{Settings: st, Destination: DaemonDestination{Destination: st.Destination}}
	if d.Type == "s3" {
		text := func(f string) string { v, _ := config[f].(string); return v }
		out.Destination.Endpoint, out.Destination.Bucket, out.Destination.Path = text("endpoint"), text("bucket"), text("path")
		st.Credentials.AccessKeyID, st.Credentials.SecretAccessKey = text("access_key_id"), text("secret_access_key")
	}
	return out, nil
}

// newPassword returns 32 random bytes, base64 encoded: the repository password the hub makes
// when the owner doesn't bring one.
func newPassword() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
