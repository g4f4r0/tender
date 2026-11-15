package updates

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/g4f4r0/tender/internal/install"
)

const (
	// checkEvery is how often the hub asks GitHub for the latest release.
	checkEvery = 6 * time.Hour
	// stepEvery is how often the hub moves the rollout in progress along.
	stepEvery = 10 * time.Second
	// stepTimeout is how long one machine's update may take, from its queuing: the update waits up
	// to 30 minutes for a running backup and has 2 for its health check.
	stepTimeout = 50 * time.Minute
	// reportTimeout is how long after its update completed the machine has to report the new version.
	reportTimeout = 5 * time.Minute
	// silentAfter is how long a machine may go without reporting before auto mode leaves it out.
	silentAfter = 10 * time.Minute
)

// Run looks at GitHub now and every 6 hours, and when the settings change, even from another
// process such as "tender mcp"; starts an auto rollout when one is due; and moves the rollout in
// progress along every 10 seconds, until ctx ends.
func (s *Store) Run(ctx context.Context, log *slog.Logger) {
	step := time.NewTicker(stepEvery)
	defer step.Stop()
	var checked, changed time.Time
	for ctx.Err() == nil {
		if st, err := s.Settings(ctx); err == nil && (!st.UpdatedAt.Equal(changed) || time.Since(checked) >= checkEvery) {
			s.Check(ctx, log)
			checked, changed = time.Now(), st.UpdatedAt
		}
		if err := s.Step(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Error("move the update rollout along", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-step.C:
		}
	}
}

// Check asks GitHub for the latest stable release and keeps what it says.
func (s *Store) Check(ctx context.Context, log *slog.Logger) {
	version, auto, err := s.releases.Latest(ctx)
	if err != nil && ctx.Err() == nil {
		log.Warn("could not ask GitHub for the latest Tender release", "error", err)
	}
	if err := s.setLatest(ctx, version, auto, err, time.Now()); err != nil && ctx.Err() == nil {
		log.Error("save the latest Tender release", "error", err)
	}
}

// Step starts an auto rollout when one is due, then moves the rollout in progress along by one
// step: it queues the update of its next machine, or looks at how the current one went.
func (s *Store) Step(ctx context.Context, now time.Time) error {
	id, err := s.current(ctx)
	if err != nil {
		return err
	}
	if id == 0 {
		if id, err = s.autoStart(ctx, now); err != nil || id == 0 {
			return err
		}
	}
	return s.advance(ctx, id, now)
}

// autoStart starts a rollout of the latest release to the machines in auto mode that run an older
// version and report, inside the window, unless the release says it needs a person or a rollout of it
// already failed.
func (s *Store) autoStart(ctx context.Context, now time.Time) (int64, error) {
	st, latest, err := s.settings(ctx)
	if err != nil || latest.Version == nil || !latest.Auto || !inWindow(st.Window, now) {
		return 0, err
	}
	var failed bool
	if err := s.db.Read.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM update_rollouts WHERE version = ? AND status = 'failed')`,
		*latest.Version).Scan(&failed); err != nil || failed {
		return 0, err
	}
	ms, err := s.machines(ctx)
	if err != nil {
		return 0, err
	}
	var pick []Machine
	for _, m := range ms {
		if effective(m.Updates, st.Mode) == ModeAuto && m.Version != nil && !install.DevBuild(*m.Version) && install.CompareVersions(*latest.Version, *m.Version) > 0 &&
			m.LastSeen != nil && now.Sub(*m.LastSeen) < silentAfter {
			pick = append(pick, m)
		}
	}
	if len(pick) == 0 {
		return 0, nil
	}
	r, err := s.create(ctx, *latest.Version, true, pick)
	return r.ID, err
}

type step struct {
	id, machineID int64
	machine       string
	status        string
	actionID      *int64
	updated       time.Time
}

// advance moves rollout id along.
func (s *Store) advance(ctx context.Context, id int64, now time.Time) error {
	var version string
	if err := s.db.Read.QueryRowContext(ctx, `SELECT version FROM update_rollouts WHERE id = ?`, id).Scan(&version); err != nil {
		return err
	}
	var st step
	var updated int64
	err := s.db.Read.QueryRowContext(ctx, `SELECT s.id, s.machine_id, m.name, s.status, s.action_id, s.updated_at
		FROM update_rollout_machines s JOIN machines m ON m.id = s.machine_id
		WHERE s.rollout_id = ? AND s.status IN ('pending', 'in_progress') ORDER BY s.position LIMIT 1`, id).
		Scan(&st.id, &st.machineID, &st.machine, &st.status, &st.actionID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return s.finish(ctx, id, StatusCompleted, nil, now)
	}
	if err != nil {
		return err
	}
	st.updated = time.Unix(updated, 0).UTC()
	ms, err := s.machines(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ms, func(m Machine) bool { return m.ID == st.machineID })
	if i < 0 {
		return nil // removed meanwhile: its step goes with it
	}
	m := ms[i]
	onIt := m.Version != nil && install.CompareVersions(*m.Version, version) == 0

	if st.status == StatusPending {
		if onIt {
			return s.setStep(ctx, st.id, StatusCompleted, nil, nil, now)
		}
		action, err := s.actions.CreateUpdate(ctx, m.ID, version)
		if err != nil {
			return err
		}
		return s.setStep(ctx, st.id, StatusInProgress, &action, nil, now)
	}

	status, output, changed, err := s.actions.Status(ctx, m.ID, *st.actionID)
	if err != nil {
		var reason = errText("The hub lost the update action of %s: %v.", m.Name, err)
		return s.fail(ctx, id, st, reason, now)
	}
	switch {
	case status == StatusFailed:
		if output == "" {
			output = "The update failed without saying why."
		}
		return s.fail(ctx, id, st, errText("%s", output), now)
	case status == StatusCompleted && onIt:
		return s.setStep(ctx, st.id, StatusCompleted, st.actionID, nil, now)
	case status == StatusCompleted && now.Sub(changed) > reportTimeout:
		v := "no version"
		if m.Version != nil {
			v = "Tender " + *m.Version
		}
		return s.fail(ctx, id, st, errText("The update of %s to Tender %s completed, but %s still reports %s.", m.Name, version, m.Name, v), now)
	case status != StatusCompleted && now.Sub(st.updated) > stepTimeout:
		return s.fail(ctx, id, st, errText("The update of %s to Tender %s didn't end within %s. Is the machine online?", m.Name, version, stepTimeout), now)
	}
	return nil
}

// fail marks a step failed and stops its rollout there.
func (s *Store) fail(ctx context.Context, id int64, st step, reason string, now time.Time) error {
	if err := s.setStep(ctx, st.id, StatusFailed, st.actionID, &reason, now); err != nil {
		return err
	}
	stop := errText("The rollout stopped at %s, which failed to update. The machines after it were not updated.", st.machine)
	return s.finish(ctx, id, StatusFailed, &stop, now)
}

func (s *Store) setStep(ctx context.Context, id int64, status string, action *int64, reason *string, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx, `UPDATE update_rollout_machines SET status = ?, action_id = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, action, reason, now.Unix(), id)
	return err
}

func (s *Store) finish(ctx context.Context, id int64, status string, reason *string, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx, `UPDATE update_rollouts SET status = ?, error = ?, updated_at = ? WHERE id = ? AND status = 'in_progress'`,
		status, reason, now.Unix(), id)
	return err
}

// GitHub asks GitHub's API about Tender's releases.
type GitHub struct {
	API      string // the releases API, such as https://api.github.com/repos/g4f4r0/tender/releases
	Download string // where the release files are
}

// Latest returns the newest stable release and whether auto mode may roll it out.
func (g GitHub) Latest(ctx context.Context) (string, bool, error) {
	r, err := install.GetRelease(ctx, g.API, "")
	if err != nil {
		return "", false, err
	}
	if err := r.Official(); err != nil {
		return "", false, err
	}
	auto, err := install.AutoAllowed(ctx, g.Download, r)
	return r.Tag[1:], auto, err
}

// Official is nil when version is a published stable release.
func (g GitHub) Official(ctx context.Context, version string) error {
	tag, err := install.Tag(version)
	if err != nil {
		return err
	}
	r, err := install.GetRelease(ctx, g.API, tag)
	if err != nil {
		return errors.New("GitHub has no Tender release " + tag)
	}
	return r.Official()
}
