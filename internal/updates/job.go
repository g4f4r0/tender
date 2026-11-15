package updates

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/config"
)

// Job starts the update job on this machine and reads how it ended. The daemon runs it for an
// update action.
type Job struct {
	Binary   string // the installed tender, /usr/local/bin/tender
	StateDir string // the daemon's state folder, where the job leaves its result and its output
}

// The update job's transient unit, and its output's file in the daemon's state folder.
const (
	UpdateUnit = "tenderupdate"
	UpdateLog  = "tenderupdate.log"
)

// jobEvery is how often the daemon looks for the job's result.
var jobEvery = 2 * time.Second

// Update starts "tender update --apply VERSION" in the transient unit tenderupdate, without --wait,
// and waits until the job leaves its result, or ends without one. The job restarts the daemon when it
// swaps the binary, which ends this wait; the job runs on, and its result stays in the state folder
// for the next daemon.
func (j Job) Update(ctx context.Context, version string) error {
	if err := config.RemoveUpdateResult(j.StateDir); err != nil {
		return err
	}
	log := filepath.Join(j.StateDir, UpdateLog)
	u, err := config.ShowUnit(ctx, UpdateUnit)
	if err != nil {
		return err
	}
	if !u.Running() {
		config.ClearUnit(ctx, UpdateUnit)
		if err := os.WriteFile(log, nil, 0o600); err != nil {
			return err
		}
		if err := config.StartUnit(ctx, UpdateUnit, log, nil, j.Binary, "update", "--apply", version); err != nil {
			return err
		}
	}
	for {
		if _, _, ok := j.UpdateResult(version); ok {
			return nil
		}
		if u, err := config.ShowUnit(ctx, UpdateUnit); err == nil && !u.Running() {
			if _, _, ok := j.UpdateResult(version); ok {
				return nil
			}
			msg := lastLine(log)
			if msg == "" {
				msg = fmt.Sprintf("it exited with status %d", u.Status)
			}
			return fmt.Errorf("the update job failed: %s", strings.TrimPrefix(msg, "tender: "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jobEvery):
		}
	}
}

// Running reports whether the update job still runs. It is true when systemd can't say, so a report
// waits rather than fails.
func (j Job) Running(ctx context.Context) bool {
	u, err := config.ShowUnit(ctx, UpdateUnit)
	return err != nil || u.Running()
}

// lastLine is the last line of the file at path, or "".
func lastLine(path string) string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// UpdateResult returns how the last update job to version ended, once it has.
func (j Job) UpdateResult(version string) (status, output string, ok bool) {
	r, ok, err := config.ReadUpdateResult(j.StateDir)
	if err != nil || !ok || strings.TrimPrefix(r.Version, "v") != strings.TrimPrefix(version, "v") {
		return "", "", false
	}
	return r.Status, r.Output, true
}

// ClearUpdateResult forgets the job's result, once the hub has it.
func (j Job) ClearUpdateResult() {
	_ = config.RemoveUpdateResult(j.StateDir)
	if !j.Running(context.Background()) {
		config.ClearUnit(context.Background(), UpdateUnit)
	}
}
