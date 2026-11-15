package updates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/install"
	"github.com/g4f4r0/tender/internal/server"
)

// Machine is what this domain needs to know about a machine. cmd/tender fills it from the machines domain.
type Machine struct {
	ID       int64
	Name     string
	Version  *string    // the Tender version its daemon reports; nil before v0.3.0
	Updates  string     // inherit, auto or manual
	LastSeen *time.Time // its latest usage report
}

// Actions carries an update to a machine. cmd/tender wires it to the actions domain.
type Actions interface {
	CreateUpdate(ctx context.Context, machineID int64, version string) (id int64, err error)
	// Status returns how the action stands: pending, in_progress, completed or failed, its output,
	// and when it last changed.
	Status(ctx context.Context, machineID, id int64) (status, output string, changed time.Time, err error)
}

// Releases tells the hub about Tender's releases on GitHub.
type Releases interface {
	// Latest returns the newest stable release's version, such as 0.3.1, and whether auto mode may roll it out.
	Latest(ctx context.Context) (version string, auto bool, err error)
	// Official is nil when version is a published stable release, else why not.
	Official(ctx context.Context, version string) error
}

// Store keeps the update settings and the rollouts.
type Store struct {
	db       *db.DB
	machines func(context.Context) ([]Machine, error)
	actions  Actions
	releases Releases
	// HubMachine is the name of the machine the hub runs on, which a rollout updates first; "" when
	// the hub's machine has no daemon.
	HubMachine string
}

// NewStore returns a store on d. machines lists the machines, actions queues their updates and
// releases asks GitHub.
func NewStore(d *db.DB, machines func(context.Context) ([]Machine, error), actions Actions, releases Releases) *Store {
	return &Store{db: d, machines: machines, actions: actions, releases: releases}
}

var windowPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])-([01][0-9]|2[0-3]):([0-5][0-9])$`)

// parseWindow reads "02:00-05:00" as minutes of the day. The end may be past midnight, as in 22:00-02:00.
func parseWindow(w string) (start, end int, err error) {
	m := windowPattern.FindStringSubmatch(w)
	if m == nil {
		return 0, 0, server.Invalid("window must be two UTC times, such as 02:00-05:00.")
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	start, end = n(m[1])*60+n(m[2]), n(m[3])*60+n(m[4])
	if start == end {
		return 0, 0, server.Invalid("window must start and end at different times. Leave it out for any time.")
	}
	return start, end, nil
}

// inWindow reports whether now, in UTC, is inside window; no window is always.
func inWindow(window *string, now time.Time) bool {
	if window == nil {
		return true
	}
	start, end, err := parseWindow(*window)
	if err != nil {
		return false
	}
	now = now.UTC()
	m := now.Hour()*60 + now.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// Settings returns the hub's update settings.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	st, _, err := s.settings(ctx)
	return st, err
}

func (s *Store) settings(ctx context.Context) (Settings, Latest, error) {
	var st Settings
	var l Latest
	var created, updated int64
	var checked *int64
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT mode, "window", latest_version, latest_auto, latest_checked_at, latest_error, created_at, updated_at FROM update_settings WHERE id = 1`).
		Scan(&st.Mode, &st.Window, &l.Version, &l.Auto, &checked, &l.Error, &created, &updated)
	st.CreatedAt, st.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	if checked != nil {
		t := time.Unix(*checked, 0).UTC()
		l.CheckedAt = &t
	}
	return st, l, err
}

// SetSettings replaces the hub's update settings. The hub looks at GitHub again within 10 seconds.
func (s *Store) SetSettings(ctx context.Context, in SettingsInput) (Settings, error) {
	if in.Mode != ModeAuto && in.Mode != ModeManual {
		return Settings{}, server.Invalid("mode must be auto or manual.")
	}
	if in.Window != nil && *in.Window == "" {
		in.Window = nil
	}
	if in.Window != nil {
		if _, _, err := parseWindow(*in.Window); err != nil {
			return Settings{}, err
		}
	}
	// updated_at always moves, so the engine sees the change even within the same second.
	if _, err := s.db.Write.ExecContext(ctx, `UPDATE update_settings SET mode = ?, "window" = ?, updated_at = max(?, updated_at + 1) WHERE id = 1`,
		in.Mode, in.Window, time.Now().Unix()); err != nil {
		return Settings{}, err
	}
	return s.Settings(ctx)
}

func (s *Store) setLatest(ctx context.Context, version string, auto bool, checkErr error, now time.Time) error {
	var msg *string
	if checkErr != nil {
		m := checkErr.Error()
		msg = &m
	}
	var v *string
	if version != "" {
		v = &version
	}
	_, err := s.db.Write.ExecContext(ctx,
		`UPDATE update_settings SET latest_version = coalesce(?, latest_version), latest_auto = CASE WHEN ? IS NULL THEN latest_auto ELSE ? END,
		 latest_checked_at = ?, latest_error = ? WHERE id = 1`, v, v, auto, now.Unix(), msg)
	return err
}

// effective is the update mode that applies to a machine with setting updates.
func effective(updates, hub string) string {
	if updates == ModeAuto || updates == ModeManual {
		return updates
	}
	return hub
}

// Overview returns the settings, what the hub knows of the latest release, the rollout in progress,
// and every machine's version.
func (s *Store) Overview(ctx context.Context) (Overview, error) {
	st, latest, err := s.settings(ctx)
	if err != nil {
		return Overview{}, err
	}
	ms, err := s.machines(ctx)
	if err != nil {
		return Overview{}, err
	}
	o := Overview{Settings: st, Latest: latest, Data: []MachineVersion{}}
	for _, m := range ms {
		mv := MachineVersion{Machine: m.Name, Version: m.Version, Updates: m.Updates, Mode: effective(m.Updates, st.Mode)}
		mv.UpdateAvailable = m.Version != nil && latest.Version != nil && install.CompareVersions(*latest.Version, *m.Version) > 0
		o.Data = append(o.Data, mv)
	}
	if id, err := s.current(ctx); err != nil {
		return Overview{}, err
	} else if id != 0 {
		r, err := s.Rollout(ctx, id)
		if err != nil {
			return Overview{}, err
		}
		o.Rollout = &r
	}
	return o, nil
}

// current returns the id of the rollout in progress, or 0.
func (s *Store) current(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.Read.QueryRowContext(ctx, `SELECT id FROM update_rollouts WHERE status = 'in_progress' ORDER BY id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// keptRollouts is how many rollouts the hub keeps; older ones are deleted.
const keptRollouts = 50

// Start begins a rollout of version, the latest release when empty, to the machines named, or to
// every machine. It refuses while another runs, for a release that isn't a stable one, for a
// machine before v0.3.0, and for a machine that runs a newer version.
func (s *Store) Start(ctx context.Context, in RolloutInput) (Rollout, error) {
	if id, err := s.current(ctx); err != nil {
		return Rollout{}, err
	} else if id != 0 {
		return Rollout{}, server.Conflict("Rollout %d is still in progress. Wait for it to end; it updates one machine at a time.", id)
	}
	version := in.Version
	if version == "" {
		v, _, err := s.releases.Latest(ctx)
		if err != nil {
			return Rollout{}, &server.Error{Status: 502, Detail: "The hub could not ask GitHub for the latest Tender release: " + err.Error() + ". Name a version, or try again."}
		}
		version = v
	}
	tag, err := install.Tag(version)
	if err != nil {
		return Rollout{}, server.Invalid("version must be a Tender release, such as 0.3.1.")
	}
	version = tag[1:]
	if err := s.releases.Official(ctx, version); err != nil {
		return Rollout{}, server.Invalid("%s. Name a stable release, such as 0.3.1.", sentenceless(err.Error()))
	}
	ms, err := s.machines(ctx)
	if err != nil {
		return Rollout{}, err
	}
	var pick []Machine
	if len(in.Machines) == 0 {
		for _, m := range ms {
			if m.Version != nil { // a daemon before v0.3.0 can't take an update
				pick = append(pick, m)
			}
		}
		if len(pick) == 0 {
			return Rollout{}, server.Invalid("No machine runs Tender v0.3.0 or later yet, which an update needs. Update each one once by hand with: sudo tender update")
		}
	}
	for _, name := range in.Machines {
		i := slices.IndexFunc(ms, func(m Machine) bool { return m.Name == name })
		switch {
		case i < 0:
			return Rollout{}, server.NotFound("Machine %q doesn't exist.", name)
		case ms[i].Version == nil:
			return Rollout{}, server.Invalid("%s runs a Tender before v0.3.0, which can't update through the hub. Update it once by hand, on that machine: sudo tender update", name)
		case slices.ContainsFunc(pick, func(m Machine) bool { return m.Name == name }):
			continue
		}
		pick = append(pick, ms[i])
	}
	for _, m := range pick {
		if install.CompareVersions(*m.Version, version) > 0 {
			return Rollout{}, server.Invalid("%s runs Tender %s, newer than %s, and an update never downgrades. Root can downgrade it by hand, on that machine: sudo tender update --version %s",
				m.Name, *m.Version, version, version)
		}
	}
	return s.create(ctx, version, false, pick)
}

func sentenceless(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '.' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// create stores a rollout of version to ms, with the hub's own machine first: a newer hub takes the
// reports of older daemons, so the others follow it.
func (s *Store) create(ctx context.Context, version string, auto bool, ms []Machine) (Rollout, error) {
	ordered := slices.Clone(ms)
	slices.SortStableFunc(ordered, func(a, b Machine) int {
		switch {
		case a.Name == s.HubMachine && b.Name != s.HubMachine:
			return -1
		case b.Name == s.HubMachine && a.Name != s.HubMachine:
			return 1
		}
		return 0
	})
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Rollout{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO update_rollouts (version, status, auto, created_at, updated_at) VALUES (?, 'in_progress', ?, ?, ?) RETURNING id`,
		version, auto, now, now).Scan(&id); err != nil {
		return Rollout{}, err
	}
	for i, m := range ordered {
		if _, err := tx.ExecContext(ctx, `INSERT INTO update_rollout_machines (rollout_id, machine_id, position, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`, id, m.ID, i+1, now, now); err != nil {
			return Rollout{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM update_rollouts WHERE status != 'in_progress' AND id NOT IN (
		SELECT id FROM update_rollouts ORDER BY id DESC LIMIT ?)`, keptRollouts); err != nil {
		return Rollout{}, err
	}
	if err := tx.Commit(); err != nil {
		return Rollout{}, err
	}
	return s.Rollout(ctx, id)
}

// Rollouts lists the rollouts the hub keeps, newest first.
func (s *Store) Rollouts(ctx context.Context) ([]Rollout, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT id FROM update_rollouts ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	out := []Rollout{}
	for _, id := range ids {
		r, err := s.Rollout(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Rollout returns one rollout with its machines.
func (s *Store) Rollout(ctx context.Context, id int64) (Rollout, error) {
	var r Rollout
	var created, updated int64
	err := s.db.Read.QueryRowContext(ctx, `SELECT id, version, status, auto, error, created_at, updated_at FROM update_rollouts WHERE id = ?`, id).
		Scan(&r.ID, &r.Version, &r.Status, &r.Auto, &r.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return r, server.NotFound("Rollout %d doesn't exist. The hub keeps the last %d.", id, keptRollouts)
	}
	if err != nil {
		return r, err
	}
	r.CreatedAt, r.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	rows, err := s.db.Read.QueryContext(ctx, `SELECT m.name, s.position, s.status, s.action_id, s.error, s.created_at, s.updated_at
		FROM update_rollout_machines s JOIN machines m ON m.id = s.machine_id WHERE s.rollout_id = ? ORDER BY s.position`, id)
	if err != nil {
		return r, err
	}
	defer func() { _ = rows.Close() }()
	r.Machines = []Step{}
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.Machine, &st.Position, &st.Status, &st.ActionID, &st.Error, &created, &updated); err != nil {
			return r, err
		}
		st.CreatedAt, st.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		r.Machines = append(r.Machines, st)
	}
	return r, rows.Err()
}

// Failure is a machine's last update that failed.
type Failure struct {
	Version string
	Reason  string
	At      time.Time
}

// Failures returns, by machine id, the update that failed for each machine whose newest rollout step failed.
func (s *Store) Failures(ctx context.Context) (map[int64]Failure, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT s.machine_id, r.version, coalesce(s.error, ''), s.updated_at
		FROM update_rollout_machines s JOIN update_rollouts r ON r.id = s.rollout_id
		WHERE s.status = 'failed' AND s.id = (SELECT max(id) FROM update_rollout_machines t WHERE t.machine_id = s.machine_id AND t.status != 'pending')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]Failure{}
	for rows.Next() {
		var id, at int64
		var f Failure
		if err := rows.Scan(&id, &f.Version, &f.Reason, &at); err != nil {
			return nil, err
		}
		f.At = time.Unix(at, 0).UTC()
		out[id] = f
	}
	return out, rows.Err()
}

// errText keeps a step's error to a sensible length.
func errText(format string, a ...any) string {
	s := fmt.Sprintf(format, a...)
	if len(s) > 2000 {
		s = s[:2000]
	}
	return s
}
