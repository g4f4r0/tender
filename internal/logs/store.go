package logs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/logs/sinks"
	"github.com/g4f4r0/tender/internal/secret"
	"github.com/g4f4r0/tender/internal/server"
)

// Hidden is what a secret shows as in a sink's config.
const Hidden = "***"

// Store reads and writes the logs tables. key seals and opens the sinks' secrets; a store
// without one can read the sinks as people see them, but not add one or hand one to a daemon.
type Store struct {
	db  *db.DB
	key []byte
}

// NewStore returns a store over d, with the hub's encryption key.
func NewStore(d *db.DB, key []byte) *Store { return &Store{db: d, key: key} }

func at(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

func atPtr(unix sql.NullInt64) *time.Time {
	if !unix.Valid {
		return nil
	}
	t := at(unix.Int64)
	return &t
}

func unixPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

// errNoKey is what a process without TENDER_ENCRYPTION_KEY answers when it needs a secret.
var errNoKey = &server.Error{Status: http.StatusServiceUnavailable,
	Detail: "This process has no TENDER_ENCRYPTION_KEY, so it can't store or read log sink secrets. Set it to the hub's key and run the command again."}

func (s *Store) seal(v any) (string, error) {
	if len(s.key) == 0 {
		return "", errNoKey
	}
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	b, err := secret.Seal(s.key, plain)
	if err != nil {
		return "", fmt.Errorf("seal a log sink secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) open(sink, field string, sealed any) (any, error) {
	if len(s.key) == 0 {
		return nil, errNoKey
	}
	h, _ := sealed.(string)
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("log sink %q: secret %s is damaged", sink, field)
	}
	plain, err := secret.Open(s.key, b)
	if err != nil {
		return nil, fmt.Errorf("open log sink %q's %s: %w. Start the hub with the TENDER_ENCRYPTION_KEY it first stored it with", sink, field, err)
	}
	var v any
	if err := json.Unmarshal(plain, &v); err != nil {
		return nil, fmt.Errorf("log sink %q: secret %s is damaged", sink, field)
	}
	return v, nil
}

// sinkType is the type called name, or a 422 that lists the types.
func sinkType(name string) (sinks.Type, error) {
	t, ok := sinks.Get(name)
	if !ok {
		return nil, server.Invalid("type must be one of %s.", strings.Join(sinks.Names(), ", "))
	}
	return t, nil
}

// row is a sink as the table holds it: config with its secrets sealed.
type row struct {
	Sink
	sealed map[string]any
}

const sinkColumns = `id, name, type, config, enabled, sources, machines, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanRow(r scanner) (row, error) {
	var d row
	var config, sources, machines string
	var created, updated int64
	if err := r.Scan(&d.ID, &d.Name, &d.Type, &config, &d.Enabled, &sources, &machines, &created, &updated); err != nil {
		return d, err
	}
	d.CreatedAt, d.UpdatedAt = at(created), at(updated)
	if err := json.Unmarshal([]byte(config), &d.sealed); err != nil {
		return d, fmt.Errorf("log sink %q has a damaged config: %w", d.Name, err)
	}
	if err := json.Unmarshal([]byte(sources), &d.Sources); err != nil {
		return d, fmt.Errorf("log sink %q has damaged sources: %w", d.Name, err)
	}
	if err := json.Unmarshal([]byte(machines), &d.Machines); err != nil {
		return d, fmt.Errorf("log sink %q has a damaged machine list: %w", d.Name, err)
	}
	d.Config = redact(d.Type, d.sealed)
	d.Status = []Status{}
	return d, nil
}

// QueryMask stands for a URL's query string where people see the URL: the query can carry a
// key, so the hub keeps it sealed in <field>_query, like a secret.
const QueryMask = "?***"

// cutQuery splits a URL at its query string.
func cutQuery(u string) (base, query string, has bool) {
	base, query, has = strings.Cut(u, "?")
	return base, query, has
}

// redact is the config people see: the plain fields, and each secret as "***" with <field>_set.
func redact(typ string, sealed map[string]any) map[string]any {
	out := maps.Clone(sealed)
	if out == nil {
		out = map[string]any{}
	}
	if t, ok := sinks.Get(typ); ok {
		delete(out, t.URL()+"_query")
		for _, f := range t.Secrets() {
			_, set := out[f]
			if set {
				out[f] = Hidden
			}
			out[f+"_set"] = set
		}
	}
	return out
}

// plain is the sink's whole config with its secrets opened.
func (s *Store) plain(d row) (map[string]any, error) {
	out := maps.Clone(d.sealed)
	if out == nil {
		out = map[string]any{}
	}
	t, ok := sinks.Get(d.Type)
	if !ok {
		return nil, fmt.Errorf("log sink %q has the unknown type %q", d.Name, d.Type)
	}
	for _, f := range t.Secrets() {
		v, set := out[f]
		if !set {
			continue
		}
		opened, err := s.open(d.Name, f, v)
		if err != nil {
			return nil, err
		}
		out[f] = opened
	}
	f := t.URL()
	if q, set := out[f+"_query"]; set {
		opened, err := s.open(d.Name, f, q)
		if err != nil {
			return nil, err
		}
		query, _ := opened.(string)
		base, _ := out[f].(string)
		out[f] = strings.TrimSuffix(base, QueryMask) + "?" + query
		delete(out, f+"_query")
	}
	return out, nil
}

// sealConfig checks a whole config for its type and seals its secrets, as the config column holds it.
func (s *Store) sealConfig(t sinks.Type, config map[string]any) (string, error) {
	clean, err := t.Check(config)
	if err != nil {
		return "", err
	}
	for _, f := range t.Secrets() {
		v, set := clean[f]
		if !set {
			continue
		}
		if clean[f], err = s.seal(v); err != nil {
			return "", err
		}
	}
	if u, ok := clean[t.URL()].(string); ok {
		if base, query, has := cutQuery(u); has {
			clean[t.URL()] = base + QueryMask
			if clean[t.URL()+"_query"], err = s.seal(query); err != nil {
				return "", err
			}
		}
	}
	b, err := json.Marshal(clean)
	return string(b), err
}

// unitPattern is a systemd unit name. It can't start with -, so it is never taken for an option.
var unitPattern = regexp.MustCompile(`^[A-Za-z0-9:_.@\\][A-Za-z0-9:_.@\\-]{0,254}$`)

// containerPattern is a Docker container name.
var containerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)

// checkSources is an error unless src is complete and valid. It fills the defaults: an empty
// list for units and names, and info for min_priority.
func checkSources(src *Sources) error {
	if src.Journal == nil && src.Services == nil {
		return server.Invalid("sources must name journal, services or both, or the sink gets nothing. Leave sources out for both.")
	}
	if j := src.Journal; j != nil {
		if j.Units == nil {
			j.Units = []string{}
		}
		if j.MinPriority == "" {
			j.MinPriority = "info"
		}
		if !slices.Contains(sinks.Priorities, j.MinPriority) {
			return server.Invalid("sources.journal.min_priority must be one of %s.", strings.Join(sinks.Priorities, ", "))
		}
		for _, u := range j.Units {
			if !unitPattern.MatchString(u) {
				return server.Invalid("sources.journal.units has %q, which is not a systemd unit name, such as nginx.service.", u)
			}
		}
	}
	if sv := src.Services; sv != nil {
		if sv.Names == nil {
			sv.Names = []string{}
		}
		for _, n := range sv.Names {
			if !containerPattern.MatchString(n) {
				return server.Invalid("sources.services.names has %q, which is not a container name.", n)
			}
		}
	}
	return nil
}

// checkMachines is an error unless every name is a machine.
func (s *Store) checkMachines(ctx context.Context, names []string) error {
	for _, n := range names {
		var id int64
		err := s.db.Read.QueryRowContext(ctx, `SELECT id FROM machines WHERE name = ?`, n).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return server.Invalid("machines has %q, which is not a machine. Run tender machines list to see the names, or leave machines empty for all.", n)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// newSecret is a webhook secret: 32 random bytes in hex.
func newSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("logs: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// AddSink stores a sink, with its secrets sealed. When the type makes a secret that was left out,
// the result holds it: that is the only time anyone can read it.
func (s *Store) AddSink(ctx context.Context, in SinkInput) (SinkCreated, error) {
	var out SinkCreated
	if !server.ValidName(in.Name) {
		return out, server.Invalid("Log sink names are lowercase letters, digits and hyphens, up to 63 characters.")
	}
	if in.Type == "" {
		return out, server.Invalid("type is required: %s.", strings.Join(sinks.Names(), " or "))
	}
	t, err := sinkType(in.Type)
	if err != nil {
		return out, err
	}
	if in.Config == nil {
		return out, server.Invalid("config is required: the %s sink's %s.", in.Type, strings.Join(t.Fields(), ", "))
	}
	config := maps.Clone(in.Config)
	for _, f := range t.Secrets() {
		if config[f] == Hidden {
			return out, server.Invalid("config.%s is \"***\", which is how the hub hides a secret. Give the secret itself.", f)
		}
	}
	if u, _ := config[t.URL()].(string); strings.HasSuffix(u, QueryMask) {
		return out, maskedURL(t.URL())
	}
	if g := t.Generated(); g != "" {
		if v, set := config[g]; !set || v == nil || v == "" {
			out.Secret = newSecret()
			config[g] = out.Secret
		}
	}
	sealed, err := s.sealConfig(t, config)
	if err != nil {
		return out, err
	}
	src := DefaultSources()
	if in.Sources != nil {
		src = *in.Sources
	}
	if err := checkSources(&src); err != nil {
		return out, err
	}
	machines := in.Machines
	if machines == nil {
		machines = []string{}
	}
	machines = cleanNames(machines)
	if err := s.checkMachines(ctx, machines); err != nil {
		return out, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	srcJSON, _ := json.Marshal(src)
	machinesJSON, _ := json.Marshal(machines)
	now := time.Now().Unix()
	_, err = s.db.Write.ExecContext(ctx,
		`INSERT INTO log_sinks (name, type, config, enabled, sources, machines, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Type, sealed, enabled, string(srcJSON), string(machinesJSON), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return out, server.Conflict("A log sink called %s already exists. Choose another name, or change that one.", in.Name)
		}
		return out, err
	}
	out.Sink, err = s.Sink(ctx, in.Name)
	return out, err
}

func maskedURL(field string) error {
	return server.Invalid("config.%s ends in ?***, which is how the hub hides a URL's query. Give the whole URL, or send back the one a read shows to keep it.", field)
}

// masks reports whether shown is how the hub shows the URL current.
func masks(shown, current string) bool {
	base, _, has := cutQuery(current)
	return has && shown == base+QueryMask
}

// cleanNames sorts names and drops repeats.
func cleanNames(names []string) []string {
	out := slices.Clone(names)
	sort.Strings(out)
	return slices.Compact(out)
}

func (s *Store) row(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, name string) (row, error) {
	d, err := scanRow(q.QueryRowContext(ctx, `SELECT `+sinkColumns+` FROM log_sinks WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return d, server.NotFound("Log sink %q doesn't exist.", name)
	}
	return d, err
}

// Sink returns the sink called name, as people see it, with each machine's status.
func (s *Store) Sink(ctx context.Context, name string) (Sink, error) {
	d, err := s.row(ctx, s.db.Read, name)
	if err != nil {
		return Sink{}, err
	}
	statuses, err := s.statuses(ctx, d.ID)
	if err != nil {
		return Sink{}, err
	}
	d.Status = statuses[d.ID]
	if d.Status == nil {
		d.Status = []Status{}
	}
	return d.Sink, nil
}

// Sinks lists every sink by name, as people see them, with each machine's status.
func (s *Store) Sinks(ctx context.Context) ([]Sink, error) {
	rows, err := s.rows(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := s.statuses(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Sink, len(rows))
	for i, d := range rows {
		if st := statuses[d.ID]; st != nil {
			d.Status = st
		}
		out[i] = d.Sink
	}
	return out, nil
}

func (s *Store) rows(ctx context.Context) ([]row, error) {
	rs, err := s.db.Read.QueryContext(ctx, `SELECT `+sinkColumns+` FROM log_sinks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []row
	for rs.Next() {
		d, err := scanRow(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rs.Err()
}

// statuses returns each sink's statuses by machine name, of one sink, or of all when sinkID is 0.
func (s *Store) statuses(ctx context.Context, sinkID int64) (map[int64][]Status, error) {
	rs, err := s.db.Read.QueryContext(ctx, `
		SELECT st.sink_id, m.name, st.records_sent, st.records_dropped, st.last_sent_at, st.last_error, st.failing_since,
		       st.last_dropped_at, st.created_at, st.updated_at
		FROM log_sink_status st JOIN machines m ON m.id = st.machine_id
		WHERE ? = 0 OR st.sink_id = ? ORDER BY m.name`, sinkID, sinkID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	out := map[int64][]Status{}
	for rs.Next() {
		var id, created, updated int64
		var st Status
		var sent, failing, dropped sql.NullInt64
		var lastError sql.NullString
		if err := rs.Scan(&id, &st.Machine, &st.RecordsSent, &st.RecordsDropped, &sent, &lastError, &failing, &dropped, &created, &updated); err != nil {
			return nil, err
		}
		st.LastSentAt, st.FailingSince, st.LastDroppedAt = atPtr(sent), atPtr(failing), atPtr(dropped)
		if lastError.Valid {
			st.LastError = &lastError.String
		}
		st.CreatedAt, st.UpdatedAt = at(created), at(updated)
		out[id] = append(out[id], st)
	}
	return out, rs.Err()
}

// patchFields are the fields a PATCH may change.
var patchFields = []string{"config", "enabled", "sources", "machines"}

// PatchSink applies a JSON merge patch to the sink called name: config, enabled, sources and
// machines. In config, null removes an optional field and a secret given as "***" stays as it
// is. name and type can't change.
func (s *Store) PatchSink(ctx context.Context, name string, patch map[string]any) (Sink, error) {
	for k := range patch {
		switch {
		case k == "name":
			return Sink{}, server.Invalid("The name can't change. Add a new sink under the new name and remove this one.")
		case k == "type":
			return Sink{}, server.Invalid("The type can't change. Add a new sink of the other type and remove this one.")
		case !slices.Contains(patchFields, k):
			return Sink{}, server.BadRequest("The body has %s, which a log sink doesn't have. It can change %s.", k, strings.Join(patchFields, ", "))
		}
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Sink{}, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := s.row(ctx, tx, name)
	if err != nil {
		return Sink{}, err
	}
	t, err := sinkType(d.Type)
	if err != nil {
		return Sink{}, err
	}
	current := map[string]any{"enabled": d.Enabled, "sources": toJSON(d.Sources), "machines": toJSON(d.Machines)}
	config := d.sealed // the secrets stay sealed unless the patch replaces them
	if p, ok := patch["config"]; ok {
		pc, isObject := p.(map[string]any)
		if !isObject {
			return Sink{}, server.Invalid("config must be a JSON object.")
		}
		if config, err = s.plain(d); err != nil {
			return Sink{}, err
		}
		pc = maps.Clone(pc)
		for _, f := range t.Secrets() {
			if pc[f] == Hidden {
				delete(pc, f)
			}
			delete(pc, f+"_set")
		}
		if u, _ := pc[t.URL()].(string); strings.HasSuffix(u, QueryMask) {
			if cur, _ := config[t.URL()].(string); !masks(u, cur) {
				return Sink{}, maskedURL(t.URL())
			}
			delete(pc, t.URL()) // sent back as a read showed it: it stays as it is
		}
		merged, _ := merge(toJSON(config), pc).(map[string]any)
		if merged == nil {
			merged = map[string]any{}
		}
		sealed, err := s.sealConfig(t, merged)
		if err != nil {
			return Sink{}, err
		}
		config = nil
		if err := json.Unmarshal([]byte(sealed), &config); err != nil {
			return Sink{}, err
		}
	}
	delete(patch, "config")
	next, _ := merge(current, patch).(map[string]any)
	enabled, ok := next["enabled"].(bool)
	if !ok {
		return Sink{}, server.Invalid("enabled must be true or false.")
	}
	var src Sources
	if err := fromJSON(next["sources"], &src); err != nil {
		return Sink{}, server.Invalid("sources must be {\"journal\": {\"units\": [...], \"min_priority\": \"info\"}, \"services\": {\"names\": [...]}}.")
	}
	if err := checkSources(&src); err != nil {
		return Sink{}, err
	}
	var machines []string
	if next["machines"] == nil {
		machines = []string{}
	} else if err := fromJSON(next["machines"], &machines); err != nil {
		return Sink{}, server.Invalid("machines must be a list of machine names.")
	}
	machines = cleanNames(machines)
	if err := s.checkMachines(ctx, machines); err != nil {
		return Sink{}, err
	}
	configJSON, _ := json.Marshal(config)
	srcJSON, _ := json.Marshal(src)
	machinesJSON, _ := json.Marshal(machines)
	if _, err := tx.ExecContext(ctx, `UPDATE log_sinks SET config = ?, enabled = ?, sources = ?, machines = ?, updated_at = ? WHERE id = ?`,
		string(configJSON), enabled, string(srcJSON), string(machinesJSON), time.Now().Unix(), d.ID); err != nil {
		return Sink{}, err
	}
	if err := tx.Commit(); err != nil {
		return Sink{}, err
	}
	return s.Sink(ctx, name)
}

// toJSON turns v into what encoding/json decodes into an any.
func toJSON(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func fromJSON(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

// merge applies an RFC 7396 merge patch to target.
func merge(target, patch any) any {
	p, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	t, ok := target.(map[string]any)
	if !ok {
		t = map[string]any{}
	} else {
		t = maps.Clone(t)
	}
	for k, v := range p {
		if v == nil {
			delete(t, k)
			continue
		}
		t[k] = merge(t[k], v)
	}
	return t
}

// DeleteSink removes the sink called name and every machine's status of it. Daemons stop
// sending to it within a minute, and delete what they buffered for it after 7 days.
func (s *Store) DeleteSink(ctx context.Context, name string) error {
	res, err := s.db.Write.ExecContext(ctx, `DELETE FROM log_sinks WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return server.NotFound("Log sink %q doesn't exist.", name)
	}
	return nil
}

// applies reports whether a sink with this machine list is for the machine called name.
func applies(machines []string, name string) bool {
	return len(machines) == 0 || slices.Contains(machines, name)
}

// DaemonSinks returns the enabled sinks for the machine called machine, with their secrets.
func (s *Store) DaemonSinks(ctx context.Context, machine string) ([]DaemonSink, error) {
	rows, err := s.rows(ctx)
	if err != nil {
		return nil, err
	}
	out := []DaemonSink{}
	for _, d := range rows {
		if !d.Enabled || !applies(d.Machines, machine) {
			continue
		}
		config, err := s.plain(d)
		if err != nil {
			return nil, err
		}
		out = append(out, DaemonSink{ID: d.ID, Name: d.Name, Type: d.Type, Config: config, Sources: d.Sources, UpdatedAt: d.UpdatedAt})
	}
	return out, nil
}

// maxErrorBytes is the most of a daemon's last_error the hub keeps.
const maxErrorBytes = 1024

// SetStatus stores a daemon's report on its sinks. A report on a sink that is gone is ignored.
func (s *Store) SetStatus(ctx context.Context, machineID int64, in StatusReport) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for _, st := range in.Data {
		if st.RecordsSent < 0 || st.RecordsDropped < 0 {
			return server.Invalid("records_sent and records_dropped can't be negative.")
		}
		var lastError any
		if st.LastError != nil {
			e := strings.ToValidUTF8(*st.LastError, "�")
			if len(e) > maxErrorBytes {
				e = strings.ToValidUTF8(e[:maxErrorBytes], "")
			}
			lastError = e
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO log_sink_status (sink_id, machine_id, records_sent, records_dropped, last_sent_at, last_error, failing_since,
			                             last_dropped_at, created_at, updated_at)
			SELECT id, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM log_sinks WHERE id = ?
			ON CONFLICT (sink_id, machine_id) DO UPDATE SET records_sent = excluded.records_sent,
			  records_dropped = excluded.records_dropped, last_sent_at = excluded.last_sent_at, last_error = excluded.last_error,
			  failing_since = excluded.failing_since, last_dropped_at = excluded.last_dropped_at, updated_at = excluded.updated_at`,
			machineID, st.RecordsSent, st.RecordsDropped, unixPtr(st.LastSentAt), lastError, unixPtr(st.FailingSince),
			unixPtr(st.LastDroppedAt), now, now, st.SinkID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Health returns the status of every enabled sink on each machine it is for, for the alert rules.
func (s *Store) Health(ctx context.Context) ([]Health, error) {
	rows, err := s.rows(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := s.statuses(ctx, 0)
	if err != nil {
		return nil, err
	}
	var out []Health
	for _, d := range rows {
		if !d.Enabled {
			continue
		}
		for _, st := range statuses[d.ID] {
			if applies(d.Machines, st.Machine) {
				out = append(out, Health{Machine: st.Machine, Sink: d.Name, Status: st})
			}
		}
	}
	return out, nil
}

// CheckKey is an error unless the store's key opens every sink's secrets.
func (s *Store) CheckKey(ctx context.Context) error {
	rows, err := s.rows(ctx)
	if err != nil {
		return err
	}
	for _, d := range rows {
		if _, err := s.plain(d); err != nil {
			return err
		}
	}
	return nil
}
