package backups

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/g4f4r0/tender/internal/db"
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

// NoSettingsType is the problem type of the 404 for a machine without backup settings. The daemon
// tells it apart from other 404s, such as a wrong machine name, before it deletes its saved keys.
const NoSettingsType = "https://github.com/g4f4r0/tender#no-backup-settings"

func noSettingsIfNone(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return &server.Error{Status: http.StatusNotFound, Type: NoSettingsType, Detail: "This machine has no backup settings yet."}
	}
	return err
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

// DeleteSettings removes a machine's backup settings: its daemon stops backing up. Its snapshots
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
	// A daemon runs one job at a time, so an older unfinished run of the same type will never
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
const abandoned = "The daemon never reported how this run ended; a later run of the same type started."

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
