package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/install"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/security"
	"github.com/g4f4r0/tender/internal/services"
	"github.com/g4f4r0/tender/internal/updates"
)

// This file wires the security and updates domains to the others: the hub's side, then the daemon's.

// newSecurity returns the security store, which reads each machine's allowlist from the machines store.
func newSecurity(d *db.DB, ms *machines.Store) *security.Store {
	s := security.NewStore(d)
	s.Allowed = func(ctx context.Context, machineID int64) ([]string, error) {
		list, err := ms.List(ctx)
		if err != nil {
			return nil, err
		}
		if i := slices.IndexFunc(list, func(o machines.Overview) bool { return o.ID == machineID }); i >= 0 {
			return list[i].SecurityAllowedPorts, nil
		}
		return nil, nil
	}
	return s
}

// newUpdates returns the updates store: it reads the machines from the machines store, queues
// updates through the actions store and asks GitHub, or the test's fake of it, about releases.
func newUpdates(d *db.DB, ms *machines.Store, as *actions.Store) *updates.Store {
	list := func(ctx context.Context) ([]updates.Machine, error) {
		overviews, err := ms.List(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]updates.Machine, len(overviews))
		for i, o := range overviews {
			out[i] = updates.Machine{ID: o.ID, Name: o.Name, Version: o.Version, Updates: o.Updates, LastSeen: o.LastSeen}
		}
		return out, nil
	}
	api, download := install.ReleaseURLs(os.Getenv)
	s := updates.NewStore(d, list, updateActions{as}, updates.GitHub{API: api, Download: download, Keys: install.ReleaseKeys()})
	s.HubMachine = hubMachine()
	return s
}

// hubMachine is the name of the machine the hub runs on, from its daemon's config, or "" when the
// hub's machine has no daemon.
func hubMachine() string {
	cfg, err := config.LoadDaemon(filepath.Join(config.Root(), config.DefaultDaemonPath))
	if err != nil {
		return ""
	}
	return cfg.Machine
}

// updateActions carries the updates domain's updates to the actions domain.
type updateActions struct{ as *actions.Store }

func (u updateActions) CreateUpdate(ctx context.Context, machineID int64, version string) (int64, error) {
	a, err := u.as.CreateUpdate(ctx, machineID, version)
	return a.ID, err
}

func (u updateActions) Status(ctx context.Context, machineID, id int64) (string, string, time.Time, error) {
	a, err := u.as.Get(ctx, machineID, id)
	if err != nil {
		return "", "", time.Time{}, err
	}
	output := ""
	if a.Output != nil {
		output = *a.Output
	}
	return a.Status, output, a.UpdatedAt, nil
}

// systemRules adds to each machine's findings its security and update problems at now: from its
// security reports and settings, and its last update when it failed and the machine is still on an
// older version.
func systemRules(ctx context.Context, ms []machines.Overview, sec *security.Store, up *updates.Store, out []notifications.Machine, now time.Time) error {
	states, err := sec.All(ctx)
	if err != nil {
		return err
	}
	failures, err := up.Failures(ctx)
	if err != nil {
		return err
	}
	for i, o := range ms {
		if st, ok := states[o.ID]; ok {
			s := &machineSecurity{Enabled: o.SecurityAlerts, AllowedPorts: o.SecurityAllowedPorts,
				Latest: securityReport(st.Report), SecurityUpdatesSince: st.SecurityUpdatesSince}
			if st.Previous != nil {
				s.Previous = securityReport(*st.Previous)
			}
			out[i].Findings = append(out[i].Findings, securityFindings(o.Name, s, now)...)
		}
		if f, ok := failures[o.ID]; ok && (o.Version == nil || install.CompareVersions(*o.Version, f.Version) < 0) {
			out[i].Findings = append(out[i].Findings, updateFindings(o.Name, &updateFailure{Version: f.Version, Reason: f.Reason})...)
		}
	}
	return nil
}

// securityReport is a security report in the security rules' terms.
func securityReport(r security.Report) *securityFacts {
	out := &securityFacts{}
	if f := r.Firewall; f.Check == security.CheckOK {
		out.FirewallChecked = true
		deny := f.DefaultIncoming != nil && (*f.DefaultIncoming == "deny" || *f.DefaultIncoming == "reject")
		out.FirewallOn = f.Active && deny
		tool := "none"
		if f.Tool != nil {
			tool = *f.Tool
		}
		switch {
		case tool == "none":
			out.Firewall = "no ufw, nftables or iptables rules filter incoming traffic"
		case !f.Active:
			out.Firewall = tool + " is inactive"
		default:
			policy := "accept"
			if f.DefaultIncoming != nil {
				policy = *f.DefaultIncoming
			}
			out.Firewall = tool + " lets in what no rule matches (default incoming: " + policy + ")"
		}
	}
	if l := r.Listeners; l.Check == security.CheckOK {
		out.ListenersChecked = true
		for _, p := range l.Ports {
			nl := listener{Port: p.Key(), Address: p.Address, Docker: p.Docker}
			if p.Process != nil {
				nl.Process = *p.Process
			}
			if p.Service != nil {
				nl.Service = *p.Service
			}
			out.Listeners = append(out.Listeners, nl)
		}
	}
	if s := r.SSH; s.Check == security.CheckOK && s.Installed {
		out.SSHChecked = true
		passwords := s.PasswordAuthentication != nil && *s.PasswordAuthentication
		root := ""
		if s.PermitRootLogin != nil {
			root = *s.PermitRootLogin
		}
		// Root login with PermitRootLogin yes needs a password only while password login is on.
		out.SSHPasswords = passwords
		out.SSH = sshWeakText(passwords, root)
	}
	if u := r.Updates; u.Check == security.CheckOK {
		out.UpdatesChecked, out.SecurityUpdates, out.AutoUpdates = true, u.Security, u.UnattendedUpgrades
	}
	if b := r.Reboot; b.Check == security.CheckOK {
		out.RebootChecked = true
		if b.Required {
			out.RebootSince = b.Since
		}
	}
	return out
}

// The daemon's side.

// daemonVersion is the version the daemon reports, or "" for a build that isn't a release, such as dev.
func daemonVersion() string {
	v := strings.TrimPrefix(version, "v")
	if !install.ValidVersion(v) {
		return ""
	}
	return v
}

// systemActions does the actions on the machine itself: the update job, and upgrades, after which
// the security checks run again.
type systemActions struct {
	updates.Job
	collect func()
}

func (s systemActions) Upgrade(ctx context.Context, packages string) (string, error) {
	out, err := security.Upgrade(ctx, s.StateDir, packages, false)
	s.collect()
	return out, err
}

func (s systemActions) ResumeUpgrade(ctx context.Context) (string, error) {
	out, err := security.Upgrade(ctx, s.StateDir, actions.PackagesSecurity, true)
	s.collect()
	return out, err
}

// publishedPorts lists Docker's published ports for the security checks.
func publishedPorts(docker *services.Docker) func(context.Context) ([]security.Published, error) {
	return func(ctx context.Context) ([]security.Published, error) {
		ports, err := docker.Published(ctx)
		out := make([]security.Published, len(ports))
		for i, p := range ports {
			out[i] = security.Published{Protocol: p.Protocol, Address: p.Address, Port: p.Port, Service: p.Service}
		}
		return out, err
	}
}

// busy marks the daemon busy in its state folder while a backup or an action runs.
func busy(state string) func() (func(), error) {
	return func() (func(), error) { return config.HoldBusy(state) }
}
