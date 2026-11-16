package security

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

// Store keeps each machine's latest security report.
type Store struct {
	db *db.DB
	// Allowed returns a machine's security_allowed_ports, from the machines domain; cmd/tender
	// wires it. Nil allows none.
	Allowed func(ctx context.Context, machineID int64) ([]string, error)
}

// NewStore returns a store on d.
func NewStore(d *db.DB) *Store { return &Store{db: d} }

var checks = []string{CheckOK, CheckError, CheckUnsupported}

// validate checks a daemon's report.
func (r *Report) validate() error {
	for _, c := range []struct {
		name, check string
	}{{"firewall", r.Firewall.Check}, {"listeners", r.Listeners.Check}, {"ssh", r.SSH.Check}, {"updates", r.Updates.Check}, {"reboot", r.Reboot.Check}} {
		if !slices.Contains(checks, c.check) {
			return server.Invalid("%s.check must be ok, error or unsupported.", c.name)
		}
	}
	if r.CollectedAt.IsZero() {
		return server.Invalid("collected_at is required.")
	}
	if len(r.Listeners.Ports) > maxListeners {
		return server.Invalid("listeners.ports takes at most %d ports.", maxListeners)
	}
	if r.Listeners.Ports == nil {
		r.Listeners.Ports = []Listener{}
	}
	for i, l := range r.Listeners.Ports {
		if (l.Protocol != "tcp" && l.Protocol != "udp") || l.Port < 1 || l.Port > 65535 {
			return server.Invalid("listeners.ports[%d] needs protocol tcp or udp and a port from 1 to 65535.", i)
		}
		r.Listeners.Ports[i].Allowed = false // the hub decides that
	}
	if r.Updates.Pending < 0 || r.Updates.Security < 0 || r.Updates.Security > r.Updates.Pending {
		return server.Invalid("updates.pending and updates.security must be counts, security at most pending.")
	}
	return nil
}

// Put stores a machine's newest report; the one it had becomes the previous.
func (s *Store) Put(ctx context.Context, machineID int64, r Report) error {
	if err := r.validate(); err != nil {
		return err
	}
	parts := make([]string, 5)
	for i, v := range []any{r.Firewall, r.Listeners, r.SSH, r.Updates, r.Reboot} {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		parts[i] = string(b)
	}
	old, err := s.state(ctx, machineID)
	if err != nil && !isNotFound(err) {
		return err
	}
	var previous *string
	var since *int64
	if err == nil {
		b, err := json.Marshal(old.Report)
		if err != nil {
			return err
		}
		previous = new(string)
		*previous = string(b)
		if old.SecurityUpdatesSince != nil {
			since = new(int64)
			*since = old.SecurityUpdatesSince.Unix()
		}
	}
	// Security updates count from the first report that found them, until one finds none.
	switch {
	case r.Updates.Check == CheckOK && r.Updates.Security == 0, r.Updates.Check == CheckUnsupported:
		since = nil
	case r.Updates.Check == CheckOK && since == nil:
		since = new(int64)
		*since = r.CollectedAt.Unix()
	}
	now := time.Now().Unix()
	_, err = s.db.Write.ExecContext(ctx,
		`INSERT INTO security_states (machine_id, firewall, listeners, ssh, updates, reboot, previous, security_updates_since, collected_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (machine_id) DO UPDATE SET firewall = excluded.firewall, listeners = excluded.listeners, ssh = excluded.ssh,
		   updates = excluded.updates, reboot = excluded.reboot, previous = excluded.previous,
		   security_updates_since = excluded.security_updates_since, collected_at = excluded.collected_at, updated_at = excluded.updated_at`,
		machineID, parts[0], parts[1], parts[2], parts[3], parts[4], previous, since, r.CollectedAt.Unix(), now, now)
	return err
}

func isNotFound(err error) bool {
	var e *server.Error
	return errors.As(err, &e) && e.Status == 404
}

const selectStates = `SELECT s.id, m.name, s.firewall, s.listeners, s.ssh, s.updates, s.reboot, s.previous, s.security_updates_since,
  s.collected_at, s.created_at, s.updated_at FROM security_states s JOIN machines m ON m.id = s.machine_id`

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (State, error) {
	var st State
	var parts [5]string
	var previous *string
	var since *int64
	var collected, created, updated int64
	if err := row.Scan(&st.ID, &st.Machine, &parts[0], &parts[1], &parts[2], &parts[3], &parts[4], &previous, &since,
		&collected, &created, &updated); err != nil {
		return st, err
	}
	for i, v := range []any{&st.Firewall, &st.Listeners, &st.SSH, &st.Updates, &st.Reboot} {
		if err := json.Unmarshal([]byte(parts[i]), v); err != nil {
			return st, err
		}
	}
	if st.Listeners.Ports == nil {
		st.Listeners.Ports = []Listener{}
	}
	if previous != nil {
		st.Previous = &Report{}
		if err := json.Unmarshal([]byte(*previous), st.Previous); err != nil {
			return st, err
		}
	}
	if since != nil {
		t := time.Unix(*since, 0).UTC()
		st.SecurityUpdatesSince = &t
	}
	st.CollectedAt, st.CreatedAt, st.UpdatedAt = time.Unix(collected, 0).UTC(), time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return st, nil
}

func (s *Store) state(ctx context.Context, machineID int64) (State, error) {
	st, err := scan(s.db.Read.QueryRowContext(ctx, selectStates+` WHERE s.machine_id = ?`, machineID))
	if errors.Is(err, sql.ErrNoRows) {
		return st, server.NotFound("This machine hasn't sent a security report yet. Its daemon sends one when it starts and every 15 minutes, from Tender v0.3.0 on.")
	}
	return st, err
}

// Get returns a machine's latest report, with each listener on the allowlist marked allowed.
func (s *Store) Get(ctx context.Context, machineID int64) (State, error) {
	st, err := s.state(ctx, machineID)
	if err != nil {
		return st, err
	}
	if s.Allowed != nil {
		allowed, err := s.Allowed(ctx, machineID)
		if err != nil {
			return st, err
		}
		for i, l := range st.Listeners.Ports {
			process := ""
			if l.Process != nil {
				process = *l.Process
			}
			st.Listeners.Ports[i].Allowed = AllowedBy(allowed, l.Key(), process)
		}
	}
	return st, nil
}

// All returns every machine's latest report, by machine id, for the alert rules.
func (s *Store) All(ctx context.Context) (map[int64]State, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT s.machine_id, `+selectStates[len("SELECT "):])
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]State{}
	for rows.Next() {
		var id int64
		st, err := scan(prefixed{rows, &id})
		if err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// prefixed scans one extra leading column into first.
type prefixed struct {
	row   scanner
	first *int64
}

func (p prefixed) Scan(dest ...any) error { return p.row.Scan(append([]any{p.first}, dest...)...) }
