package config

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Transient units run the update of Tender and the upgrade of the system's packages outside the
// daemon's sandbox. systemd-run starts them without --wait or --pipe: as root, without those, it
// talks to systemd over its private socket, /run/systemd/private, where systemd trusts root by its
// uid, so it needs no D-Bus, no polkit and no CAP_SYS_ADMIN. Their output goes to a file, and
// systemctl show, over the same socket, says how they ended. RemainAfterExit keeps a unit, and its
// result, until ClearUnit, so a daemon that restarts meanwhile still finds it.

// Unit is what systemctl show says about a transient unit.
type Unit struct {
	Load, Active, Sub, Result string
	Status                    int // the exit status of its command
}

// Exists reports whether systemd has the unit, running or ended.
func (u Unit) Exists() bool { return u.Load == "loaded" }

// Running reports whether the unit's command still runs.
func (u Unit) Running() bool {
	return u.Exists() && u.Sub != "exited" && (u.Active == "active" || u.Active == "activating" || u.Active == "reloading" || u.Active == "deactivating")
}

// Succeeded reports whether the unit's command exited 0.
func (u Unit) Succeeded() bool { return u.Result == "success" && u.Status == 0 }

// StartUnit starts command as the transient service name, with env, its output appended to log.
func StartUnit(ctx context.Context, name, log string, env []string, command ...string) error {
	args := []string{"--unit", name, "--quiet", "--property=RemainAfterExit=yes",
		"--property=StandardOutput=append:" + log, "--property=StandardError=append:" + log}
	for _, e := range env {
		args = append(args, "--setenv="+e)
	}
	args = append(append(args, "--"), command...)
	out, err := exec.CommandContext(ctx, SystemdRun(), args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("systemd-run could not start %s: %s", name, msg)
		}
		return fmt.Errorf("systemd-run could not start %s: %w", name, err)
	}
	return nil
}

// ShowUnit asks systemd about the transient service name.
func ShowUnit(ctx context.Context, name string) (Unit, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", "--property=LoadState,ActiveState,SubState,Result,ExecMainStatus", name+".service").Output()
	if err != nil {
		return Unit{}, fmt.Errorf("systemctl show %s: %w", name, err)
	}
	var u Unit
	for _, line := range strings.Split(string(bytes.TrimSpace(out)), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "LoadState":
			u.Load = v
		case "ActiveState":
			u.Active = v
		case "SubState":
			u.Sub = v
		case "Result":
			u.Result = v
		case "ExecMainStatus":
			u.Status, _ = strconv.Atoi(v)
		}
	}
	return u, nil
}

// WaitUnit waits, asking every so often, until the service name no longer runs. When ctx ends first
// it returns ctx's error, and the unit runs on.
func WaitUnit(ctx context.Context, name string, every time.Duration) (Unit, error) {
	for {
		u, err := ShowUnit(ctx, name)
		if err == nil && !u.Running() {
			return u, nil
		}
		select {
		case <-ctx.Done():
			return u, ctx.Err()
		case <-time.After(every):
		}
	}
}

// ClearUnit forgets the ended service name, so the next one can take its name.
func ClearUnit(ctx context.Context, name string) {
	_ = exec.CommandContext(ctx, "systemctl", "stop", name+".service").Run()
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", name+".service").Run()
}
