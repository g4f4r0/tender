package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/install"
	"github.com/g4f4r0/tender/internal/updates"
)

// runUpdate runs "tender update". Without a machine it updates this one, as root. With machine
// names or --all it asks the hub for a rollout, which needs no root.
func runUpdate(args []string, version string, s session) error {
	fs := newFlagSet("update", "[MACHINE ...|--all] [--version X.Y.Z] [--hub URL]",
		"Update Tender to the latest stable release, or to --version.\n\n"+
			"With machine names or --all, the hub rolls the release out: one machine at a time, the hub's own machine last, "+
			"each checking the release's signature, waiting for a running backup, and rolling back if it isn't healthy within "+
			"2 minutes. That needs no sudo. Follow it with: tender updates show\n\n"+
			"Without a machine, it updates this machine, as root: sudo tender update. Only that way can --version go back to an "+
			"older release.")
	hubURL := hubFlag(fs)
	all := fs.Bool("all", false, "update every machine through the hub")
	target := fs.String("version", "", "`X.Y.Z` the release to install (default: the latest)")
	apply := fs.String("apply", "", "") // the update job's own call, from the daemon: never in the usage
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	switch {
	case *apply != "":
		if len(rest) > 0 || *all || *target != "" {
			return usageError(fs, "--apply takes nothing else")
		}
		in, err := localInstaller(version, s)
		if err != nil {
			return err
		}
		return in.Apply(context.Background(), *apply)
	case *all && len(rest) > 0:
		return usageError(fs, "name machines or pass --all, not both")
	case *all || len(rest) > 0:
		return startRollout(fs, *hubURL, *target, rest, s)
	}
	in, err := localInstaller(version, s)
	if err != nil {
		return err
	}
	return in.Update(context.Background(), *target)
}

// isRoot reports whether this process runs as root, or under the end-to-end test's TENDER_TEST_ROOT,
// whose paths need no root.
func isRoot() bool { return os.Geteuid() == 0 || os.Getenv(config.EnvTestRoot) != "" }

func localInstaller(version string, s session) (*install.Installer, error) {
	if !isRoot() {
		return nil, errors.New("updating this machine needs root. Run it again with sudo, or update it through the hub, which needs " +
			"no sudo: tender update MACHINE, or tender update --all")
	}
	in, err := install.New(s.out)
	if err != nil {
		return nil, err
	}
	in.Version = version
	return in, nil
}

func startRollout(fs *flag.FlagSet, hubURL, version string, machines []string, s session) error {
	hub, err := newHubAPI(fs, hubURL)
	if err != nil {
		return err
	}
	var r updates.Rollout
	if err := hub.do(http.MethodPost, "/v1/updates/rollouts", updates.RolloutInput{Version: version, Machines: machines}, &r); err != nil {
		return err
	}
	names := make([]string, len(r.Machines))
	for i, m := range r.Machines {
		names[i] = m.Machine
	}
	say(s.out, "Started rollout %d of Tender %s, one machine at a time: %s.", r.ID, r.Version, strings.Join(names, ", "))
	say(s.out, "Follow it with: tender updates show")
	return nil
}

var updatesUsage = groupUsage("updates", "See and set how Tender updates itself on every machine.", []row{
	{"show", "Show the settings, each machine's version and the rollouts"},
	{"set [--mode auto|manual] [--window HH:MM-HH:MM]", "Change the update settings"},
})

// runUpdates runs "tender updates show|set".
func runUpdates(args []string, s session) error {
	return runGroup("updates", updatesUsage, args, s.out, map[string]func([]string) error{
		"show": func(a []string) error { return updatesShow(a, s) },
		"set":  func(a []string) error { return updatesSet(a, s) },
	})
}

func updatesShow(args []string, s session) error {
	fs := newFlagSet("updates show", "[--hub URL]",
		"Show the update settings, the newest release the hub found on GitHub, each machine's version and mode, and the last rollouts.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var o updates.Overview
	if err := hub.do(http.MethodGet, "/v1/updates", nil, &o); err != nil {
		return err
	}
	window := "any time"
	if o.Window != nil {
		window = *o.Window + " UTC"
	}
	say(s.out, "Mode: %s, window: %s.", o.Mode, window)
	switch l := o.Latest; {
	case l.Version == nil && l.Error != nil:
		say(s.out, "The hub could not ask GitHub for the latest release: %s", *l.Error)
	case l.Version == nil:
		say(s.out, "The hub hasn't asked GitHub for the latest release yet.")
	default:
		line := "Latest release: " + *l.Version
		if !l.Auto {
			line += ", which auto mode leaves to a person"
		}
		if l.CheckedAt != nil {
			line += ", checked " + ago(s.now(), *l.CheckedAt)
		}
		say(s.out, "%s.", line)
	}
	say(s.out, "")
	rows := make([][]string, len(o.Data))
	for i, m := range o.Data {
		v, mode, avail := "before 0.3.0", m.Mode, "-"
		if m.Version != nil {
			v = *m.Version
		}
		if m.Updates != updates.ModeInherit {
			mode += " (its own)"
		}
		if m.UpdateAvailable {
			avail = "yes"
		}
		rows[i] = []string{m.Machine, v, mode, avail}
	}
	printTable(s.out, []string{"MACHINE", "VERSION", "MODE", "UPDATE"}, rows)

	list, err := listAll[updates.Rollout](hub, "/v1/updates/rollouts")
	if err != nil {
		return err
	}
	if len(list) > 5 {
		list = list[:5]
	}
	for _, r := range list {
		say(s.out, "\nRollout %d of %s, %s, started %s:", r.ID, r.Version, strings.ReplaceAll(r.Status, "_", " "), ago(s.now(), r.CreatedAt))
		for _, m := range r.Machines {
			line := fmt.Sprintf("  %s: %s", m.Machine, strings.ReplaceAll(m.Status, "_", " "))
			if m.Error != nil {
				line += ". " + strings.Join(strings.Fields(*m.Error), " ") // the whole reason, which is why it stopped
			}
			say(s.out, "%s", line)
		}
	}
	return nil
}

func updatesSet(args []string, s session) error {
	fs := newFlagSet("updates set", "[--mode auto|manual] [--window HH:MM-HH:MM] [--hub URL]",
		"Change how Tender updates itself on every machine that doesn't have its own mode. In auto mode the hub rolls out each "+
			"new stable release by itself, inside the window. In manual mode nothing updates until you run tender update. "+
			"A machine's own mode comes from: tender machines set NAME --updates auto|manual|inherit")
	hubURL := hubFlag(fs)
	mode := fs.String("mode", "", "`MODE` auto or manual")
	window := fs.String("window", "", "`HH:MM-HH:MM` when auto mode may start a rollout, in UTC, such as 02:00-05:00; \"\" for any time")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["mode"] && !set["window"] {
		return usageError(fs, "updates set needs --mode or --window")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var st updates.Settings
	if err := hub.do(http.MethodGet, "/v1/updates/settings", nil, &st); err != nil {
		return err
	}
	in := updates.SettingsInput{Mode: st.Mode, Window: st.Window}
	if set["mode"] {
		in.Mode = *mode
	}
	if set["window"] {
		in.Window = window
	}
	if err := hub.do(http.MethodPut, "/v1/updates/settings", in, &st); err != nil {
		return err
	}
	w := "any time"
	if st.Window != nil {
		w = *st.Window + " UTC"
	}
	say(s.out, "Updates are %s, window: %s.", st.Mode, w)
	return nil
}
