package actions

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

// Services is what the queue needs from the services domain on the hub. cmd/tender wires it.
type Services interface {
	// Check returns nil when the machine has the service, else a 404 server.Error.
	Check(ctx context.Context, machineID int64, service string) error
	// Finished tells the services domain that a start, stop or restart of the service completed.
	Finished(ctx context.Context, machineID int64, service, typ string) error
}

// Store reads and writes the actions.
type Store struct {
	db       *db.DB
	services Services
	// Keep is how long a logs action keeps its output after it finished. Logs can hold secrets.
	Keep time.Duration

	unflushed atomic.Bool // logs were cleared but the WAL may still hold them; Sweep tries again
}

// DefaultKeep is how long a logs action keeps its output, unless the hub is told otherwise.
const DefaultKeep = time.Hour

// NewStore returns a store on d. services checks and hears about the service of an action.
func NewStore(d *db.DB, services Services) *Store {
	return &Store{db: d, services: services, Keep: DefaultKeep}
}

const (
	// lifetime is how long an action may wait for the daemon, or run without an answer.
	lifetime = 15 * time.Minute
	// upgradeLifetime is how long an upgrade may run without an answer: it can take a while.
	upgradeLifetime = 45 * time.Minute
	// updateLifetime is how long an update may run without an answer. Its job waits for a running
	// backup as long as it takes, since a busy machine isn't a failed update.
	updateLifetime = 24 * time.Hour
	// handOutAgain is how long, in seconds, the daemon has to claim an action it was handed, before
	// it is handed out again.
	handOutAgain = "30"
	// kept is how many actions each machine keeps; older ones are deleted.
	kept = 100
	// pollEvery is how often a wait looks at the database again. Actions can be queued by another
	// process, "tender mcp --stdio", so a wait can't rely on being told.
	pollEvery = 500 * time.Millisecond

	notPickedUp = "the daemon did not pick this up; is the machine online?"
	notAnswered = "the daemon started this but did not say how it ended; is the machine online?"
)

// The machine's name comes from the machines table, which the schema already ties this domain to.
// selectSummaries leaves out the output, which can be 256 KiB an action, for the list.
const (
	summaryColumns  = `a.id, m.name, a.type, a.service, a.unit, a.priority, a.since, a.lines, a.version, a.packages, a.status, a.output_cleared, a.created_at, a.updated_at`
	selectSummaries = `SELECT ` + summaryColumns + ` FROM actions a JOIN machines m ON m.id = a.machine_id`
	selectActions   = `SELECT ` + summaryColumns + `, a.output FROM actions a JOIN machines m ON m.id = a.machine_id`
)

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Action, error) {
	var a Action
	var created, updated int64
	err := row.Scan(&a.ID, &a.Machine, &a.Type, &a.Service, &a.Unit, &a.Priority, &a.Since, &a.Lines, &a.Version, &a.Packages, &a.Status, &a.OutputCleared,
		&created, &updated, &a.Output)
	a.CreatedAt, a.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return a, err
}

func scanSummary(row scanner) (Summary, error) {
	var a Summary
	var created, updated int64
	err := row.Scan(&a.ID, &a.Machine, &a.Type, &a.Service, &a.Unit, &a.Priority, &a.Since, &a.Lines, &a.Version, &a.Packages, &a.Status, &a.OutputCleared,
		&created, &updated)
	a.CreatedAt, a.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return a, err
}

// Create queues an action for the daemon of a machine. It keeps the machine's newest 100 finished
// ones; an action that hasn't finished is never deleted.
func (s *Store) Create(ctx context.Context, machineID int64, in Input) (Action, error) {
	if err := in.validate(); err != nil {
		return Action{}, err
	}
	if in.Service != nil {
		if err := s.services.Check(ctx, machineID, *in.Service); err != nil {
			return Action{}, err
		}
	}
	return s.insert(ctx, machineID, in, nil)
}

// CreateUpdate queues the update of a machine to a Tender release, such as 0.3.1. Only an update
// rollout calls it, so the machines update one at a time.
func (s *Store) CreateUpdate(ctx context.Context, machineID int64, version string) (Action, error) {
	return s.insert(ctx, machineID, Input{Type: TypeUpdate}, &version)
}

func (s *Store) insert(ctx context.Context, machineID int64, in Input, version *string) (Action, error) {
	now := time.Now().Unix()
	var id int64
	err := s.db.Write.QueryRowContext(ctx,
		`INSERT INTO actions (machine_id, type, service, unit, priority, since, lines, version, packages, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?) RETURNING id`,
		machineID, in.Type, in.Service, in.Unit, in.Priority, in.Since, in.Lines, version, in.Packages, now, now).Scan(&id)
	if err != nil {
		return Action{}, err
	}
	if _, err := s.db.Write.ExecContext(ctx,
		`DELETE FROM actions WHERE machine_id = ?1 AND status IN ('completed', 'failed') AND id NOT IN (
		   SELECT id FROM actions WHERE machine_id = ?1 AND status IN ('completed', 'failed') ORDER BY id DESC LIMIT ?2)`,
		machineID, kept); err != nil {
		return Action{}, err
	}
	return s.get(ctx, machineID, id)
}

// List returns a machine's actions, newest first, without their output: at most the 100 it keeps.
func (s *Store) List(ctx context.Context, machineID int64) ([]Summary, error) {
	if err := s.tidy(ctx, machineID, time.Now()); err != nil {
		return nil, err
	}
	rows, err := s.db.Read.QueryContext(ctx, selectSummaries+` WHERE a.machine_id = ? ORDER BY a.id DESC`, machineID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Summary{}
	for rows.Next() {
		a, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// tidy fails a machine's actions that waited or ran too long without the daemon, and clears the
// output of logs actions that completed more than Keep ago. machineID 0 tidies every machine's.
// It only reads when there is nothing to do, so a wait doesn't take the write lock.
func (s *Store) tidy(ctx context.Context, machineID int64, now time.Time) error {
	// Every statement numbers its arguments the same way: ?1 the machine, ?2 when a pending or a
	// running action is stale, ?3 when a logs action's output is old, ?4 when a running upgrade and
	// ?5 a running update is stale, ?6 and ?7 the failures, ?8 now.
	args := []any{machineID, now.Add(-lifetime).Unix(), now.Add(-s.Keep).Unix(), now.Add(-upgradeLifetime).Unix(),
		now.Add(-updateLifetime).Unix(), notPickedUp, notAnswered, now.Unix()}
	const expired = `(status = 'pending' AND created_at < ?2) OR
		(status = 'in_progress' AND updated_at < CASE type WHEN 'upgrade' THEN ?4 WHEN 'update' THEN ?5 ELSE ?2 END)`
	// A failed logs action holds why it failed, not logs, so only a completed one is cleared.
	const clearable = `type = 'logs' AND status = 'completed' AND output_cleared = 0 AND updated_at < ?3`
	var any bool
	if err := s.db.Read.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM actions WHERE (?1 = 0 OR machine_id = ?1) AND ((`+expired+`) OR (`+clearable+`)))`,
		args[:5]...).Scan(&any); err != nil || !any {
		return err
	}
	if _, err := s.db.Write.ExecContext(ctx,
		`UPDATE actions SET status = 'failed', output = CASE status WHEN 'pending' THEN ?6 ELSE ?7 END, updated_at = ?8
		 WHERE (?1 = 0 OR machine_id = ?1) AND (`+expired+`)`, args...); err != nil {
		return err
	}
	res, err := s.db.Write.ExecContext(ctx,
		`UPDATE actions SET output = NULL, output_cleared = 1, updated_at = ?8
		 WHERE (?1 = 0 OR machine_id = ?1) AND `+clearable, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	// The cleared pages were zeroed (secure_delete); the WAL still holds their old copies until a
	// checkpoint empties it. One that a reader holds up is left to the next pass, a minute later.
	s.unflushed.Store(true)
	return s.flush(ctx)
}

// flush empties the WAL after logs were cleared, until it has once.
func (s *Store) flush(ctx context.Context) error {
	if !s.unflushed.Load() {
		return nil
	}
	done, err := s.db.Checkpoint(ctx)
	if done {
		s.unflushed.Store(false)
	}
	return err
}

// Sweep tidies every machine's actions each minute until ctx ends, so logs don't outlive Keep
// when nobody reads them.
func (s *Store) Sweep(ctx context.Context, log *slog.Logger) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		err := s.tidy(ctx, 0, time.Now())
		if err == nil {
			err = s.flush(ctx)
		}
		if err != nil && ctx.Err() == nil {
			log.Error("tidy the actions", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Store) get(ctx context.Context, machineID, id int64) (Action, error) {
	a, err := scan(s.db.Read.QueryRowContext(ctx, selectActions+` WHERE a.machine_id = ? AND a.id = ?`, machineID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, server.NotFound("Action %d doesn't exist on this machine.", id)
	}
	return a, err
}

// Get returns one of a machine's actions, with its output.
func (s *Store) Get(ctx context.Context, machineID, id int64) (Action, error) {
	if err := s.tidy(ctx, machineID, time.Now()); err != nil {
		return Action{}, err
	}
	return s.get(ctx, machineID, id)
}

// Await returns the action once it finished, or as it is after timeout. It tidies once, then
// only reads.
func (s *Store) Await(ctx context.Context, machineID, id int64, timeout time.Duration) (Action, error) {
	deadline := time.Now().Add(timeout)
	if err := s.tidy(ctx, machineID, time.Now()); err != nil {
		return Action{}, err
	}
	for {
		a, err := s.get(ctx, machineID, id)
		if err != nil || a.Finished() || !time.Now().Before(deadline) {
			return a, err
		}
		select {
		case <-ctx.Done():
			return a, ctx.Err()
		case <-time.After(min(pollEvery, time.Until(deadline))):
		}
	}
}

// Take hands out the machine's oldest pending action that wasn't handed out in the last 30 s, or
// none. It stays pending: the daemon claims it with Claim before it runs it, and one it doesn't
// claim in time is handed out again. It hands out one at a time because the daemon runs them one
// at a time: the others wait, so none of them expires as unanswered while it waits its turn.
func (s *Store) Take(ctx context.Context, machineID int64) ([]Action, error) {
	if err := s.tidy(ctx, machineID, time.Now()); err != nil {
		return nil, err
	}
	return s.takeOne(ctx, machineID)
}

// available is a pending action of machine ?1 that may be handed out at ?2.
const available = `machine_id = ?1 AND status = 'pending' AND (handed_out_at IS NULL OR handed_out_at <= ?2 - ` + handOutAgain + `)`

func (s *Store) takeOne(ctx context.Context, machineID int64) ([]Action, error) {
	var id int64
	now := time.Now().Unix()
	err := s.db.Write.QueryRowContext(ctx,
		`UPDATE actions SET handed_out_at = ?2 WHERE id = (SELECT id FROM actions WHERE `+available+` ORDER BY id LIMIT 1) RETURNING id`,
		machineID, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return []Action{}, nil
	}
	if err != nil {
		return nil, err
	}
	a, err := s.get(ctx, machineID, id)
	if err != nil {
		return nil, err
	}
	return []Action{a}, nil
}

// Wait is Take, but when nothing is pending it waits up to wait for an action, looking again every
// half second with a read, and writing only to take one. It answers early, with nothing, when ctx
// ends or stop closes.
func (s *Store) Wait(ctx context.Context, machineID int64, wait time.Duration, stop <-chan struct{}) ([]Action, error) {
	deadline := time.Now().Add(wait)
	if err := s.tidy(ctx, machineID, time.Now()); err != nil {
		return nil, err
	}
	for {
		var pending bool
		if err := s.db.Read.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM actions WHERE `+available+`)`,
			machineID, time.Now().Unix()).Scan(&pending); err != nil {
			return nil, err
		}
		if pending {
			list, err := s.takeOne(ctx, machineID)
			if err != nil || len(list) > 0 {
				return list, err
			}
		}
		if !time.Now().Before(deadline) {
			return []Action{}, nil
		}
		select {
		case <-ctx.Done():
			return []Action{}, nil
		case <-stop:
			return []Action{}, nil
		case <-time.After(min(pollEvery, time.Until(deadline))):
		}
	}
}

// Claim moves a pending action to in_progress: the daemon is about to run it. It does that once;
// claiming it again, or after it expired, is a 409. The daemon runs an action only when its claim
// succeeded, and retries a claim with the same Idempotency-Key, so a claim whose answer was lost
// gets that answer replayed rather than a 409, and nothing else can claim it.
func (s *Store) Claim(ctx context.Context, machineID, id int64) error {
	if err := s.tidy(ctx, machineID, time.Now()); err != nil {
		return err
	}
	res, err := s.db.Write.ExecContext(ctx, `UPDATE actions SET status = 'in_progress', updated_at = ? WHERE machine_id = ? AND id = ? AND status = 'pending'`,
		time.Now().Unix(), machineID, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	a, err := s.get(ctx, machineID, id)
	if err != nil {
		return err
	}
	return server.Conflict("Action %d is %s, not pending, so it can't be claimed.", id, a.Status)
}

// Finish stores how an in_progress action ended. It also takes a late report, for an action the
// hub gave up on as unanswered: the daemon keeps a report until the hub has it, so the real result
// replaces that failure. A completed start, stop or restart is told to the services domain first,
// which marks a service Tender stopped, so it doesn't alert as down, and clears the mark on a start
// or restart. If that fails, the action stays as it was and the daemon's retry tries again.
func (s *Store) Finish(ctx context.Context, machineID, id int64, p Patch) error {
	if p.Status != StatusCompleted && p.Status != StatusFailed {
		return server.Invalid("status must be in_progress, completed or failed.")
	}
	a, err := s.get(ctx, machineID, id)
	if err != nil {
		return err
	}
	late := a.Status == StatusFailed && deref(a.Output) == notAnswered
	if a.Status != StatusInProgress && !late {
		return server.Conflict("Action %d is %s, not in_progress, so it can't finish now.", id, a.Status)
	}
	if a.Type != TypeLogs && a.Service != nil && p.Status == StatusCompleted {
		if err := s.services.Finished(ctx, machineID, *a.Service, a.Type); err != nil {
			return err
		}
	}
	res, err := s.db.Write.ExecContext(ctx,
		`UPDATE actions SET status = ?, output = ?, updated_at = ? WHERE id = ? AND (status = 'in_progress' OR (status = 'failed' AND output = ?))`,
		p.Status, p.Output, time.Now().Unix(), id, notAnswered)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return server.Conflict("Action %d is no longer in_progress, so it can't finish now.", id)
	}
	return nil
}
