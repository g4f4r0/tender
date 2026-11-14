package backups

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/secret"
	"github.com/g4f4r0/tender/internal/server"
)

// Store reads and writes destinations, settings, snapshots and history.
type Store struct {
	db  *db.DB
	key []byte // encrypts destination credentials; nil where nothing reads or writes them
}

// NewStore returns a store on d. key is TENDER_ENCRYPTION_KEY, decoded: it seals the credentials
// that AddDestination and PatchDestination store and opens them in Credentials. The tools and the
// rules never touch credentials, so they pass nil.
func NewStore(d *db.DB, key []byte) *Store { return &Store{db: d, key: key} }

func at(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

type scanner interface{ Scan(dest ...any) error }

// querier is a database or a transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func notFoundIfNone(err error, format string, a ...any) error {
	if errors.Is(err, sql.ErrNoRows) {
		return server.NotFound(format, a...)
	}
	return err
}

// Destinations

const destinationColumns = `d.id, d.name, d.type, d.endpoint, d.bucket, d.path, d.created_at, d.updated_at`

func scanDestination(row scanner) (Destination, error) {
	var d Destination
	var created, updated int64
	err := row.Scan(&d.ID, &d.Name, &d.Type, &d.Endpoint, &d.Bucket, &d.Path, &created, &updated)
	d.CreatedAt, d.UpdatedAt = at(created), at(updated)
	return d, err
}

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

// newPassword returns 32 random bytes, base64 encoded: the repository password the hub makes
// when the owner doesn't bring one.
func newPassword() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// errPasswordFixed refuses a change of the repository password.
var errPasswordFixed = server.Invalid("The password can't change: every repository at this destination is encrypted with it. " +
	"Add a new destination with the new password and move the machines to it.")

// trimPath removes the slashes around a path inside the bucket.
func trimPath(p string) string { return strings.Trim(p, "/") }

// validateLocation is a 422 unless the endpoint, bucket and name can address a repository.
func validateLocation(endpoint, bucket string) error {
	u, err := url.Parse(endpoint)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return server.Invalid("endpoint must be a URL such as https://<account>.r2.cloudflarestorage.com.")
	case bucket == "":
		return server.Invalid("bucket is required.")
	}
	return nil
}

// AddDestination stores a destination and its credentials, sealed. Without a password it makes one
// and returns it in the result: that is the only time anyone can read it.
func (s *Store) AddDestination(ctx context.Context, in DestinationInput) (DestinationCreated, error) {
	var out DestinationCreated
	if in.Type == "" {
		in.Type = "s3"
	}
	in.Path = trimPath(in.Path)
	switch {
	case !server.ValidName(in.Name):
		return out, server.Invalid("Destination names are lowercase letters, digits and hyphens, up to 63 characters.")
	case in.Type != "s3":
		return out, server.Invalid("type must be s3.")
	case in.AccessKeyID == "":
		return out, server.Invalid("access_key_id is required.")
	case in.SecretAccessKey == "":
		return out, server.Invalid("secret_access_key is required.")
	}
	if err := validateLocation(in.Endpoint, in.Bucket); err != nil {
		return out, err
	}
	if in.Password == "" {
		p, err := newPassword()
		if err != nil {
			return out, err
		}
		in.Password, out.Password = p, p
	}
	sealed := make([][]byte, 3)
	for i, v := range []string{in.AccessKeyID, in.SecretAccessKey, in.Password} {
		b, err := s.seal(v)
		if err != nil {
			return DestinationCreated{}, err
		}
		sealed[i] = b
	}

	now := time.Now().Unix()
	res, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO backup_destinations (name, type, endpoint, bucket, path, access_key_id, secret_access_key, password, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		in.Name, in.Type, in.Endpoint, in.Bucket, in.Path, sealed[0], sealed[1], sealed[2], now, now)
	if err != nil {
		return DestinationCreated{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return DestinationCreated{}, err
	}
	if n == 0 {
		return DestinationCreated{}, server.Conflict("A destination with this name or this endpoint, bucket and path already exists. Choose another name, or use the existing one.")
	}
	out.Destination, err = s.Destination(ctx, in.Name)
	return out, err
}

// PatchDestination changes the location or the keys of a destination; fields left nil stay as they are.
func (s *Store) PatchDestination(ctx context.Context, name string, p DestinationPatch) (Destination, error) {
	switch {
	case p.Endpoint != nil && *p.Endpoint == "", p.Bucket != nil && *p.Bucket == "":
		return Destination{}, server.Invalid("endpoint and bucket can't be empty.")
	case p.AccessKeyID != nil && *p.AccessKeyID == "", p.SecretAccessKey != nil && *p.SecretAccessKey == "":
		return Destination{}, server.Invalid("access_key_id and secret_access_key can't be empty. Leave a key out to keep it.")
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Destination{}, err
	}
	defer func() { _ = tx.Rollback() }()

	d, err := scanDestination(tx.QueryRowContext(ctx, `SELECT `+destinationColumns+` FROM backup_destinations d WHERE d.name = ?`, name))
	if err != nil {
		return d, notFoundIfNone(err, "Destination %q doesn't exist.", name)
	}
	if p.Endpoint != nil {
		d.Endpoint = *p.Endpoint
	}
	if p.Bucket != nil {
		d.Bucket = *p.Bucket
	}
	if p.Path != nil {
		d.Path = trimPath(*p.Path)
	}
	if err := validateLocation(d.Endpoint, d.Bucket); err != nil {
		return d, err
	}
	var taken int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM backup_destinations WHERE endpoint = ? AND bucket = ? AND path = ? AND id != ?`,
		d.Endpoint, d.Bucket, d.Path, d.ID).Scan(&taken)
	if err != nil {
		return d, err
	}
	if taken > 0 {
		return d, server.Conflict("Another destination already uses this endpoint, bucket and path. Use that one, or choose another location.")
	}

	d.UpdatedAt = at(time.Now().Unix())
	_, err = tx.ExecContext(ctx, `UPDATE backup_destinations SET endpoint = ?, bucket = ?, path = ?, updated_at = ? WHERE id = ?`,
		d.Endpoint, d.Bucket, d.Path, d.UpdatedAt.Unix(), d.ID)
	if err != nil {
		return d, err
	}
	for column, v := range map[string]*string{"access_key_id": p.AccessKeyID, "secret_access_key": p.SecretAccessKey} {
		if v == nil {
			continue
		}
		sealed, err := s.seal(*v)
		if err != nil {
			return d, err
		}
		// The column name comes from the map above, never from the request.
		if _, err := tx.ExecContext(ctx, `UPDATE backup_destinations SET `+column+` = ? WHERE id = ?`, sealed, d.ID); err != nil {
			return d, err
		}
	}
	return d, tx.Commit()
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

// CheckKey is an error unless the store's key opens the credentials already stored. The hub runs it
// at start, so a wrong key stops it there instead of failing every agent later.
func (s *Store) CheckKey(ctx context.Context) error {
	var name string
	var sealed []byte
	err := s.db.Read.QueryRowContext(ctx, `SELECT name, password FROM backup_destinations ORDER BY id LIMIT 1`).Scan(&name, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := s.open(sealed); err != nil {
		return fmt.Errorf("destination %q: %w", name, err)
	}
	return nil
}

// Destination returns the destination called name.
func (s *Store) Destination(ctx context.Context, name string) (Destination, error) {
	d, err := scanDestination(s.db.Read.QueryRowContext(ctx,
		`SELECT `+destinationColumns+` FROM backup_destinations d WHERE d.name = ?`, name))
	return d, notFoundIfNone(err, "Destination %q doesn't exist.", name)
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
		d, err := scanDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Settings

// SetSettings sets a machine's destination, schedule and retention, replacing any earlier settings.
func (s *Store) SetSettings(ctx context.Context, machineID int64, destination, schedule string, retentionDays int) (Settings, error) {
	if _, err := cron.ParseStandard(schedule); err != nil {
		return Settings{}, server.Invalid("schedule must be a standard 5-field cron expression: %v.", err)
	}
	if retentionDays < 1 {
		return Settings{}, server.Invalid("retention_days must be at least 1.")
	}
	now := time.Now().Unix()
	res, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO backup_settings (machine_id, destination_id, schedule, retention_days, created_at, updated_at)
		 SELECT ?, id, ?, ?, ?, ? FROM backup_destinations WHERE name = ?
		 ON CONFLICT (machine_id) DO UPDATE SET destination_id = excluded.destination_id, schedule = excluded.schedule,
		   retention_days = excluded.retention_days, updated_at = excluded.updated_at`,
		machineID, schedule, retentionDays, now, now, destination)
	if err != nil {
		return Settings{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Settings{}, err
	} else if n == 0 {
		return Settings{}, server.Invalid("Destination %q doesn't exist.", destination)
	}
	return s.Settings(ctx, machineID)
}

// Settings returns a machine's settings, or a NotFound error before they are set.
func (s *Store) Settings(ctx context.Context, machineID int64) (Settings, error) {
	var st Settings
	var created, updated, dcreated, dupdated int64
	d := &st.Destination
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT d.id, d.name, d.type, d.endpoint, d.bucket, d.path, d.created_at, d.updated_at,
		        s.schedule, s.retention_days, s.created_at, s.updated_at
		 FROM backup_settings s JOIN backup_destinations d ON d.id = s.destination_id WHERE s.machine_id = ?`, machineID).
		Scan(&d.ID, &d.Name, &d.Type, &d.Endpoint, &d.Bucket, &d.Path, &dcreated, &dupdated,
			&st.Schedule, &st.RetentionDays, &created, &updated)
	d.CreatedAt, d.UpdatedAt = at(dcreated), at(dupdated)
	st.CreatedAt, st.UpdatedAt = at(created), at(updated)
	return st, notFoundIfNone(err, "This machine has no backup settings yet.")
}

// DeleteSettings removes a machine's backup settings: its agent stops backing up. Its snapshots
// and history stay. It is a NotFound error when the machine has none.
func (s *Store) DeleteSettings(ctx context.Context, machineID int64) error {
	res, err := s.db.Write.ExecContext(ctx, `DELETE FROM backup_settings WHERE machine_id = ?`, machineID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return server.NotFound("This machine has no backup settings. There is nothing to remove.")
	}
	return nil
}

// Credentials returns the keys and password of the destination a machine backs up to, decrypted.
// Only the machine's own agent may be given them.
func (s *Store) Credentials(ctx context.Context, machineID int64) (Credentials, error) {
	var sealed [3][]byte
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT d.access_key_id, d.secret_access_key, d.password
		 FROM backup_settings s JOIN backup_destinations d ON d.id = s.destination_id WHERE s.machine_id = ?`, machineID).
		Scan(&sealed[0], &sealed[1], &sealed[2])
	if err != nil {
		return Credentials{}, notFoundIfNone(err, "This machine has no backup settings yet.")
	}
	var plain [3]string
	for i, b := range sealed {
		v, err := s.open(b)
		if err != nil {
			return Credentials{}, err
		}
		plain[i] = v
	}
	return Credentials{AccessKeyID: plain[0], SecretAccessKey: plain[1], Password: plain[2]}, nil
}

// Snapshots

const snapshotColumns = `id, restic_id, size_bytes, paths, created_at, updated_at`

func scanSnapshot(row scanner) (Snapshot, error) {
	var sn Snapshot
	var paths string
	var created, updated int64
	if err := row.Scan(&sn.ID, &sn.ResticID, &sn.SizeBytes, &paths, &created, &updated); err != nil {
		return sn, err
	}
	sn.CreatedAt, sn.UpdatedAt = at(created), at(updated)
	return sn, json.Unmarshal([]byte(paths), &sn.Paths)
}

func (in SnapshotInput) validate() error {
	switch {
	case in.ResticID == "":
		return server.Invalid("restic_id is required.")
	case in.CreatedAt.IsZero():
		return server.Invalid("created_at is required for snapshot %s.", in.ResticID)
	case in.SizeBytes != nil && *in.SizeBytes < 0:
		return server.Invalid("size_bytes can't be negative for snapshot %s.", in.ResticID)
	}
	return nil
}

// ReplaceSnapshots makes in the machine's full snapshot set: it adds new snapshots, updates
// changed ones and deletes the rest. History rows that pointed at a deleted snapshot keep a null link.
func (s *Store) ReplaceSnapshots(ctx context.Context, machineID int64, in []SnapshotInput) ([]Snapshot, error) {
	ids := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, sn := range in {
		if err := sn.validate(); err != nil {
			return nil, err
		}
		if seen[sn.ResticID] {
			return nil, server.Invalid("restic_id %s appears twice.", sn.ResticID)
		}
		seen[sn.ResticID] = true
		ids = append(ids, sn.ResticID)
	}
	keep, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	for _, sn := range in {
		paths, err := json.Marshal(append([]string{}, sn.Paths...))
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO backup_snapshots (machine_id, restic_id, size_bytes, paths, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT (machine_id, restic_id) DO UPDATE SET size_bytes = excluded.size_bytes, paths = excluded.paths,
			   created_at = excluded.created_at, updated_at = excluded.updated_at
			 WHERE size_bytes IS NOT excluded.size_bytes OR paths != excluded.paths OR created_at != excluded.created_at`,
			machineID, sn.ResticID, sn.SizeBytes, string(paths), sn.CreatedAt.Unix(), now)
		if err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM backup_snapshots WHERE machine_id = ? AND restic_id NOT IN (SELECT value FROM json_each(?))`,
		machineID, string(keep)); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT `+snapshotColumns+` FROM backup_snapshots WHERE machine_id = ? ORDER BY created_at DESC, id DESC`, machineID)
	if err != nil {
		return nil, err
	}
	out := []Snapshot{}
	for rows.Next() {
		sn, err := scanSnapshot(rows)
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		out = append(out, sn)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// ListSnapshots returns a machine's snapshots newest first. It fetches one row more than the
// page limit; server.NewList cuts it.
func (s *Store) ListSnapshots(ctx context.Context, machineID int64, p server.Page) ([]Snapshot, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT `+snapshotColumns+` FROM backup_snapshots
		 WHERE machine_id = ? AND (created_at, id) < (?, ?) ORDER BY created_at DESC, id DESC LIMIT ?`,
		machineID, p.Before.CreatedAt, p.Before.ID, p.Size())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Snapshot
	for rows.Next() {
		sn, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

// Snapshot returns one of a machine's snapshots by the hub's id.
func (s *Store) Snapshot(ctx context.Context, machineID, id int64) (Snapshot, error) {
	sn, err := scanSnapshot(s.db.Read.QueryRowContext(ctx,
		`SELECT `+snapshotColumns+` FROM backup_snapshots WHERE machine_id = ? AND id = ?`, machineID, id))
	return sn, notFoundIfNone(err, "Snapshot %d doesn't exist on this machine.", id)
}

// History

const historyColumns = `id, snapshot_id, type, status, error, created_at, updated_at`

func scanHistory(row scanner) (History, error) {
	var h History
	var created, updated int64
	err := row.Scan(&h.ID, &h.SnapshotID, &h.Type, &h.Status, &h.Error, &created, &updated)
	h.CreatedAt, h.UpdatedAt = at(created), at(updated)
	return h, err
}

func validType(t string) bool { return t == TypeBackup || t == TypeCleanup || t == TypeCheck }

func validStatus(s string) bool {
	return s == StatusPending || s == StatusInProgress || s == StatusCompleted || s == StatusFailed
}

func isFinal(status string) bool { return status == StatusCompleted || status == StatusFailed }

// checkSnapshot is a 422 unless the snapshot belongs to the machine.
func checkSnapshot(ctx context.Context, q querier, machineID, id int64) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM backup_snapshots WHERE machine_id = ? AND id = ?`, machineID, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return server.Invalid("snapshot_id %d is not a snapshot of this machine.", id)
	}
	return err
}

// CreateHistory records a run: just started (in_progress), queued (pending), or, when the hub
// was unreachable, already finished (completed or failed, which may carry error and snapshot_id).
func (s *Store) CreateHistory(ctx context.Context, machineID int64, in HistoryInput) (History, error) {
	switch {
	case !validType(in.Type):
		return History{}, server.Invalid("type must be backup, cleanup or check.")
	case !validStatus(in.Status):
		return History{}, server.Invalid("status must be pending, in_progress, completed or failed.")
	case in.CreatedAt.IsZero():
		return History{}, server.Invalid("created_at is required.")
	case !isFinal(in.Status) && (in.Error != nil || in.SnapshotID != nil):
		return History{}, server.Invalid("error and snapshot_id are only accepted with status completed or failed.")
	}

	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return History{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if in.SnapshotID != nil {
		if err := checkSnapshot(ctx, tx, machineID, *in.SnapshotID); err != nil {
			return History{}, err
		}
	}
	h := History{SnapshotID: in.SnapshotID, Type: in.Type, Status: in.Status, Error: in.Error,
		CreatedAt: at(in.CreatedAt.Unix()), UpdatedAt: at(time.Now().Unix())}
	err = tx.QueryRowContext(ctx,
		`INSERT INTO backup_history (machine_id, snapshot_id, type, status, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		machineID, h.SnapshotID, h.Type, h.Status, h.Error, h.CreatedAt.Unix(), h.UpdatedAt.Unix()).Scan(&h.ID)
	if err != nil {
		return History{}, err
	}
	// An agent runs one job at a time, so an older unfinished run of the same type will never
	// be finished: it crashed or lost its report. Close it, so it can't look in progress forever.
	_, err = tx.ExecContext(ctx,
		`UPDATE backup_history SET status = ?, error = ?, updated_at = ?
		 WHERE machine_id = ? AND type = ? AND status IN (?, ?) AND created_at < ?`,
		StatusFailed, abandoned, h.UpdatedAt.Unix(), machineID, h.Type, StatusPending, StatusInProgress, h.CreatedAt.Unix())
	if err != nil {
		return History{}, err
	}
	return h, tx.Commit()
}

// abandoned is the error on a run whose end was never reported.
const abandoned = "The agent never reported how this run ended; a later run of the same type started."

// PatchHistory finishes or advances a run that is pending or in_progress. A finished run
// can't change: that is a Conflict.
func (s *Store) PatchHistory(ctx context.Context, machineID, id int64, p HistoryPatch) (History, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return History{}, err
	}
	defer func() { _ = tx.Rollback() }()

	h, err := scanHistory(tx.QueryRowContext(ctx,
		`SELECT `+historyColumns+` FROM backup_history WHERE machine_id = ? AND id = ?`, machineID, id))
	if err != nil {
		return h, notFoundIfNone(err, "Run %d doesn't exist on this machine.", id)
	}
	if isFinal(h.Status) {
		return h, server.Conflict("Run %d already finished as %s.", id, h.Status)
	}
	if p.Status != nil {
		if *p.Status == StatusPending || !validStatus(*p.Status) {
			return h, server.Invalid("status must be in_progress, completed or failed.")
		}
		h.Status = *p.Status
	}
	if !isFinal(h.Status) && (p.Error != nil || p.SnapshotID != nil) {
		return h, server.Invalid("error and snapshot_id are only accepted with status completed or failed.")
	}
	if p.Error != nil {
		h.Error = p.Error
	}
	if p.SnapshotID != nil {
		if err := checkSnapshot(ctx, tx, machineID, *p.SnapshotID); err != nil {
			return h, err
		}
		h.SnapshotID = p.SnapshotID
	}
	h.UpdatedAt = at(time.Now().Unix())
	_, err = tx.ExecContext(ctx, `UPDATE backup_history SET snapshot_id = ?, status = ?, error = ?, updated_at = ? WHERE id = ?`,
		h.SnapshotID, h.Status, h.Error, h.UpdatedAt.Unix(), h.ID)
	if err != nil {
		return h, err
	}
	return h, tx.Commit()
}

// History returns one of a machine's runs by the hub's id.
func (s *Store) History(ctx context.Context, machineID, id int64) (History, error) {
	h, err := scanHistory(s.db.Read.QueryRowContext(ctx,
		`SELECT `+historyColumns+` FROM backup_history WHERE machine_id = ? AND id = ?`, machineID, id))
	return h, notFoundIfNone(err, "Run %d doesn't exist on this machine.", id)
}

// ListHistory returns a machine's runs newest first, optionally of one type and status ("" means
// any). It fetches one row more than the page limit; server.NewList cuts it.
func (s *Store) ListHistory(ctx context.Context, machineID int64, typ, status string, p server.Page) ([]History, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT `+historyColumns+` FROM backup_history
		 WHERE machine_id = ? AND (? = '' OR type = ?) AND (? = '' OR status = ?) AND (created_at, id) < (?, ?)
		 ORDER BY created_at DESC, id DESC LIMIT ?`,
		machineID, typ, typ, status, status, p.Before.CreatedAt, p.Before.ID, p.Size())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []History
	for rows.Next() {
		h, err := scanHistory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Summaries

// Summary returns one machine's backup state.
func (s *Store) Summary(ctx context.Context, machineID int64, name string) (Summary, error) {
	sum := Summary{Machine: name}
	if st, err := s.Settings(ctx, machineID); err == nil {
		sum.Settings = &st
	} else if !isNotFound(err) {
		return sum, err
	}

	sn, err := scanSnapshot(s.db.Read.QueryRowContext(ctx,
		`SELECT `+snapshotColumns+` FROM backup_snapshots WHERE machine_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, machineID))
	if err == nil {
		sum.LatestSnapshot = &sn
	} else if !errors.Is(err, sql.ErrNoRows) {
		return sum, err
	}
	err = s.db.Read.QueryRowContext(ctx, `SELECT count(*) FROM backup_snapshots WHERE machine_id = ?`, machineID).Scan(&sum.SnapshotCount)
	if err != nil {
		return sum, err
	}

	for typ, dest := range map[string]**History{TypeBackup: &sum.LastBackup, TypeCheck: &sum.LastCheck} {
		h, err := scanHistory(s.db.Read.QueryRowContext(ctx,
			`SELECT `+historyColumns+` FROM backup_history WHERE machine_id = ? AND type = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
			machineID, typ))
		if err == nil {
			*dest = &h
		} else if !errors.Is(err, sql.ErrNoRows) {
			return sum, err
		}
	}
	return sum, nil
}

// Summaries returns the backup state of every machine by name. It reads the machines table
// for the names; the schema already ties this domain to it.
func (s *Store) Summaries(ctx context.Context) ([]Summary, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT id, name FROM machines ORDER BY name`)
	if err != nil {
		return nil, err
	}
	type machine struct {
		id   int64
		name string
	}
	var ms []machine
	for rows.Next() {
		var m machine
		if err := rows.Scan(&m.id, &m.name); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		ms = append(ms, m)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	out := []Summary{}
	for _, m := range ms {
		sum, err := s.Summary(ctx, m.id, m.name)
		if err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, nil
}

func isNotFound(err error) bool {
	var e *server.Error
	return errors.As(err, &e) && e.Status == 404
}

// LastCompleted returns when each machine's newest completed backup started, by machine name.
// Machines with none are missing. It reads the machines table for the names, like Summaries.
func (s *Store) LastCompleted(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT m.name, max(h.created_at) FROM backup_history h JOIN machines m ON m.id = h.machine_id
		 WHERE h.type = ? AND h.status = ? GROUP BY m.id`, TypeBackup, StatusCompleted)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]time.Time{}
	for rows.Next() {
		var name string
		var created int64
		if err := rows.Scan(&name, &created); err != nil {
			return nil, err
		}
		out[name] = at(created)
	}
	return out, rows.Err()
}

// LastFinished returns each machine's newest backup run that ended, completed or failed, by
// machine name. A run still in progress doesn't count, so a retry can't hide a failure early.
func (s *Store) LastFinished(ctx context.Context) (map[string]History, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT m.name, r.id, r.snapshot_id, r.type, r.status, r.error, r.created_at, r.updated_at
		 FROM machines m JOIN backup_history r ON r.id = (
		   SELECT h.id FROM backup_history h WHERE h.machine_id = m.id AND h.type = ? AND h.status IN (?, ?)
		   ORDER BY h.created_at DESC, h.id DESC LIMIT 1)`,
		TypeBackup, StatusCompleted, StatusFailed)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]History{}
	for rows.Next() {
		var name string
		var h History
		var created, updated int64
		if err := rows.Scan(&name, &h.ID, &h.SnapshotID, &h.Type, &h.Status, &h.Error, &created, &updated); err != nil {
			return nil, err
		}
		h.CreatedAt, h.UpdatedAt = at(created), at(updated)
		out[name] = h
	}
	return out, rows.Err()
}
