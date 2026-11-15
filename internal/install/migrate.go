package install

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// The units and paths of Tender v0.2.1 and earlier, before the daemon and the hub got their
// current names. "install daemon", "install hub" and "install refresh" move them.
const (
	oldDaemonUnit  = "tender-agent"
	oldHubUnit     = "tender-hub"
	oldConfigPath  = etcDir + "/agent.toml"
	oldStateDir    = stateParent + "/agent"
	daemonStateDir = stateParent + "/daemon"
)

func oldUnitPath(unit string) string { return unitDir + "/" + unit + ".service" }

// checkOldDaemon stops a move that would leave the old daemon's config or state behind, before
// anything changes.
func (in *Installer) checkOldDaemon() error {
	for _, m := range [][2]string{{oldConfigPath, daemonConfigPath}, {oldStateDir, daemonStateDir}} {
		if in.installed(m[0]) && in.installed(m[1]) {
			return fmt.Errorf("this machine has both %s, from the old tender-agent service, and %s, so nothing was changed. "+
				"Keep the one the daemon should use, move the other away, then run this again", in.path(m[0]), in.path(m[1]))
		}
	}
	return nil
}

// moveOldDaemon stops the tender-agent unit and moves agent.toml and the state folder to their new
// names, so the daemon keeps its config, saved settings, queued reports and the time of its last
// backup. The stopped unit stays until removeOldUnit, once tenderd runs, so a failed run can be
// run again.
func (in *Installer) moveOldDaemon(ctx context.Context) error {
	if err := in.stopOldUnit(ctx, oldDaemonUnit); err != nil {
		return err
	}
	for _, m := range [][2]string{
		{oldConfigPath, daemonConfigPath},
		{oldStateDir, daemonStateDir},
		{daemonStateDir + "/agent-state.json", daemonStateDir + "/state.json"},
	} {
		if err := in.move(m[0], m[1]); err != nil {
			return err
		}
	}
	return nil
}

// stopOldUnit stops an old unit and keeps it from starting at boot.
func (in *Installer) stopOldUnit(ctx context.Context, unit string) error {
	_, err := in.run(ctx, "systemctl", "disable", "--now", unit)
	in.changed = true
	return err
}

// removeOldUnit deletes a stopped old unit once the service runs under its new name.
func (in *Installer) removeOldUnit(ctx context.Context, unit, now string) error {
	if err := os.Remove(in.path(oldUnitPath(unit))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := in.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	in.say("Renamed the %s service to %s", unit, now)
	return nil
}

// move renames from to to. A missing from was moved by an earlier run; checkOldDaemon has already
// refused the case where both exist.
func (in *Installer) move(from, to string) error {
	if !in.installed(from) || in.installed(to) {
		return nil
	}
	return os.Rename(in.path(from), in.path(to))
}
