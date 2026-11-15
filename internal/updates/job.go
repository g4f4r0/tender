package updates

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/g4f4r0/tender/internal/config"
)

// Job starts the update job on this machine and reads how it ended. The daemon runs it for an
// update action.
type Job struct {
	Binary   string // the installed tender, /usr/local/bin/tender
	StateDir string // the daemon's state folder, where the job leaves its result
}

// Update runs "tender update --apply VERSION" in the transient unit tenderupdate and waits for it.
// The job restarts the daemon when it swaps the binary, which ends this wait; the result stays in
// the state folder for the next daemon.
func (j Job) Update(ctx context.Context, version string) error {
	if err := config.RemoveUpdateResult(j.StateDir); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, config.SystemdRun(), "--wait", "--collect", "--quiet", "--unit", "tenderupdate",
		j.Binary, "update", "--apply", version)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("the update job failed: %s", msg)
	}
	return nil
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
func (j Job) ClearUpdateResult() { _ = config.RemoveUpdateResult(j.StateDir) }
