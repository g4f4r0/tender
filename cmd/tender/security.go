package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/security"
)

// runSecurity runs "tender security [MACHINE]".
func runSecurity(args []string, s session) error {
	fs := newFlagSet("security", "[MACHINE] [--hub URL]",
		"Show what each machine's daemon found in its security checks, every 15 minutes: the firewall, the ports open on a public "+
			"address, SSH password login, pending updates, unattended-upgrades and a needed reboot. With a machine, show its "+
			"checks in full. The checks run on Debian and Ubuntu; elsewhere they say unsupported.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usageError(fs, "security takes at most one machine name")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if len(rest) == 1 {
		var st security.State
		if err := hub.do(http.MethodGet, machinePath(rest[0], "/security"), nil, &st); err != nil {
			return err
		}
		printSecurity(s, st)
		return nil
	}
	list, err := listAll[machines.Overview](hub, "/v1/machines")
	if err != nil {
		return err
	}
	if len(list) == 0 {
		say(s.out, "No machines yet. Add one with: tender machines add NAME")
		return nil
	}
	rows := make([][]string, len(list))
	for i, m := range list {
		var st security.State
		err := hub.do(http.MethodGet, machinePath(m.Name, "/security"), nil, &st)
		switch {
		case answered(err, http.StatusNotFound):
			rows[i] = []string{m.Name, "no report yet", "-", "-", "-", "-"}
			continue
		case err != nil:
			return err
		}
		rows[i] = []string{m.Name, firewallText(st.Firewall), portsText(st.Listeners), sshText(st.SSH), updatesText(st.Updates), rebootText(st.Reboot)}
	}
	printTable(s.out, []string{"MACHINE", "FIREWALL", "EXPOSED", "SSH", "UPDATES", "REBOOT"}, rows)
	return nil
}

func printSecurity(s session, st security.State) {
	say(s.out, "Security checks of %s, collected %s.\n", st.Machine, ago(s.now(), st.CollectedAt))
	say(s.out, "Firewall: %s", firewallText(st.Firewall))
	say(s.out, "SSH: %s", sshText(st.SSH))
	say(s.out, "Updates: %s", updatesText(st.Updates))
	if st.SecurityUpdatesSince != nil {
		say(s.out, "Security updates pending since %s.", ago(s.now(), *st.SecurityUpdatesSince))
	}
	say(s.out, "Reboot: %s", rebootText(st.Reboot))
	switch l := st.Listeners; {
	case l.Check != security.CheckOK:
		say(s.out, "Listeners: %s", checkText(l.Check, l.Error))
	case len(l.Ports) == 0:
		say(s.out, "Listeners: nothing listens on a public address.")
	default:
		say(s.out, "\nListening on a public address:")
		rows := make([][]string, len(l.Ports))
		for i, p := range l.Ports {
			who, note := "-", "exposed"
			if p.Process != nil {
				who = *p.Process
			}
			switch {
			case p.Allowed:
				note = "allowed"
			case p.Docker && p.Service != nil:
				note = "published by Docker for " + *p.Service + ", around ufw"
			case p.Docker:
				note = "published by Docker, around ufw"
			}
			rows[i] = []string{p.Key(), p.Address, who, note}
		}
		printTable(s.out, []string{"PORT", "ADDRESS", "PROCESS", "NOTE"}, rows)
		say(s.out, "\nAllow an expected port with: tender machines set %s --allow-port PORT, or all of a program's with --allow-port PROGRAM", st.Machine)
	}
}

func checkText(check string, msg *string) string {
	if check == security.CheckUnsupported {
		return "unsupported on this system"
	}
	if msg != nil {
		return "could not check: " + *msg
	}
	return "could not check"
}

func firewallText(f security.Firewall) string {
	if f.Check != security.CheckOK {
		return checkText(f.Check, f.Error)
	}
	tool := "none"
	if f.Tool != nil {
		tool = *f.Tool
	}
	switch {
	case tool == "none":
		return "none"
	case !f.Active:
		return tool + " inactive"
	case f.DefaultIncoming != nil:
		return tool + ", incoming " + *f.DefaultIncoming
	}
	return tool + " active"
}

func portsText(l security.Listeners) string {
	if l.Check != security.CheckOK {
		return checkText(l.Check, l.Error)
	}
	var exposed []string
	for _, p := range l.Ports {
		if !p.Allowed {
			exposed = append(exposed, p.Key())
		}
	}
	if len(exposed) == 0 {
		return "none"
	}
	return strings.Join(exposed, " ")
}

func sshText(s security.SSH) string {
	switch {
	case s.Check != security.CheckOK:
		return checkText(s.Check, s.Error)
	case !s.Installed:
		return "no SSH server"
	}
	root := s.PermitRootLogin != nil && *s.PermitRootLogin == "yes"
	switch passwords := s.PasswordAuthentication != nil && *s.PasswordAuthentication; {
	case passwords && root:
		return "passwords, root with a password"
	case passwords:
		return "passwords"
	}
	return "keys only"
}

func updatesText(u security.Updates) string {
	if u.Check != security.CheckOK {
		return checkText(u.Check, u.Error)
	}
	text := strconv.Itoa(u.Pending) + " pending"
	if u.Security > 0 {
		text = fmt.Sprintf("%d pending, %d security", u.Pending, u.Security)
	}
	if !u.UnattendedUpgrades {
		text += ", unattended-upgrades off"
	}
	return text
}

func rebootText(r security.Reboot) string {
	switch {
	case r.Check != security.CheckOK:
		return checkText(r.Check, r.Error)
	case !r.Required:
		return "not needed"
	case r.Since != nil:
		return "needed since " + r.Since.Format("2006-01-02")
	}
	return "needed"
}
