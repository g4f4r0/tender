package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/machines"
)

var machinesUsage = groupUsage("machines", "Add the machines that back up, manage their tokens and settings, and upgrade their packages.", []row{
	{"list", "List the machines and when each last reported"},
	{"add NAME", "Add a machine and print its token once"},
	{"remove NAME --yes", "Remove a machine and its history from the hub"},
	{"rotate-token NAME", "Replace a machine's token and print the new one"},
	{"set NAME [--updates MODE ...]", "Change a machine's update mode and security settings"},
	{"upgrade NAME [--all-packages]", "Upgrade a machine's system packages"},
})

// runMachines runs "tender machines list|add|remove|rotate-token".
func runMachines(args []string, s session) error {
	return runGroup("machines", machinesUsage, args, s.out, map[string]func([]string) error{
		"list":         func(a []string) error { return machinesList(a, s) },
		"add":          func(a []string) error { return machinesAdd(a, s) },
		"remove":       func(a []string) error { return machinesRemove(a, s) },
		"rotate-token": func(a []string) error { return machinesRotate(a, s) },
		"set":          func(a []string) error { return machinesSet(a, s) },
		"upgrade":      func(a []string) error { return machinesUpgrade(a, s) },
	})
}

func machinesList(args []string, s session) error {
	fs := newFlagSet("machines list", "[--hub URL]", "List the machines and when each last reported.")
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
		rows[i] = []string{m.Name, "never", "-", "-", "-"}
		if m.LastSeen != nil {
			rows[i][1] = ago(s.now(), *m.LastSeen)
		}
		if m.Usage != nil {
			rows[i][2], rows[i][3] = fmt.Sprintf("%.0f%%", m.Usage.MemoryUsedPercent), fmt.Sprintf("%.0f%%", m.Usage.DiskUsedPercent)
		}
		if m.Version != nil {
			rows[i][4] = *m.Version
		}
	}
	printTable(s.out, []string{"NAME", "LAST SEEN", "MEMORY", "DISK", "VERSION"}, rows)
	return nil
}

func machinesAdd(args []string, s session) error {
	fs := newFlagSet("machines add", "NAME [--hub URL]",
		"Add a machine and print its token. The token is shown once: save it, then give it to \"tender install daemon\" on that machine.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a machine name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var created machines.MachineCreated
	if err := hub.do(http.MethodPost, "/v1/machines", machines.MachineInput{Name: name}, &created); err != nil {
		return err
	}
	say(s.out, "Added machine %s.\n", created.Name)
	say(s.out, "Its hub token, shown only now. Save it before you go on:")
	say(s.out, "  %s\n", created.Token)
	say(s.out, "Install the daemon on that machine; it asks for the token:")
	say(s.out, "  curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh \\")
	say(s.out, "    | sudo sh -s -- daemon --hub %s --machine %s", daemonHubURL(hub.url), created.Name)
	return nil
}

func machinesRotate(args []string, s session) error {
	fs := newFlagSet("machines rotate-token", "NAME [--hub URL]",
		"Replace a machine's token and print the new one, once. The old token stops working at once, so the daemon stays "+
			"offline until it has the new one.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a machine name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var t machines.Token
	if err := hub.do(http.MethodPost, machinePath(name, "/token"), nil, &t); err != nil {
		return err
	}
	say(s.out, "Replaced the token of %s. The old one no longer works.\n", name)
	say(s.out, "The new token, shown only now. Save it before you go on:")
	say(s.out, "  %s\n", t.Token)
	say(s.out, "Give it to the daemon on that machine: sudo tender install daemon --token-file PATH")
	return nil
}

func machinesRemove(args []string, s session) error {
	fs := newFlagSet("machines remove", "NAME --yes [--hub URL]",
		"Remove a machine and its history from the hub. Its token stops working. Its backups stay where they are, "+
			"in the destination. There is no undo, so the command needs --yes.")
	hubURL := hubFlag(fs)
	yes := fs.Bool("yes", false, "confirm the removal")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a machine name")
	if err != nil {
		return err
	}
	if !*yes {
		return fmt.Errorf("this removes %s and its history from the hub, with no undo. Run \"tender machines remove %s --yes\" to confirm", name, name)
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if err := hub.do(http.MethodDelete, "/v1/machines/"+url.PathEscape(name), nil, nil); err != nil {
		return err
	}
	say(s.out, "Removed machine %s and its history. Its backups stay in the destination.", name)
	return nil
}

// daemonHubURL is the hub address to print in a daemon install command: the address the person
// used, unless it points at this machine, which another machine can't reach.
func daemonHubURL(hub string) string {
	u, err := url.Parse(hub)
	if err != nil {
		return "HUB_URL"
	}
	if host := u.Hostname(); host == "localhost" || net.ParseIP(host).IsLoopback() {
		return "HUB_URL"
	}
	return hub
}

// listFlag is a flag that may be given more than once.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func machinesSet(args []string, s session) error {
	fs := newFlagSet("machines set", "NAME [--updates inherit|auto|manual] [--allow-port PORT|PROGRAM ...]\n[--deny-port PORT|PROGRAM ...] [--security-alerts on|off] [--hub URL]",
		"Change a machine's settings. --updates gives it its own update mode, or inherit to follow the hub's (tender updates set). "+
			"--allow-port puts a port, such as 41641/udp, on its security allowlist, so it doesn't alert as exposed, or a program "+
			"as ss names it, such as cloudflared, so none of its ports do; --deny-port takes one off. --security-alerts turns its security alert emails on or off.")
	hubURL := hubFlag(fs)
	mode := fs.String("updates", "", "`MODE` inherit, auto or manual")
	var allow, deny listFlag
	fs.Var(&allow, "allow-port", "`PORT|PROGRAM` a port with its protocol, such as 41641/udp, or a program, such as cloudflared, "+
		"that may listen on a public address; repeat for more")
	fs.Var(&deny, "deny-port", "`PORT|PROGRAM` a port or program to take off the allowlist; repeat for more")
	alerts := fs.String("security-alerts", "", "`on|off` the machine's security alert emails")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a machine name")
	if err != nil {
		return err
	}
	var p machines.Patch
	if *mode != "" {
		p.Updates = mode
	}
	switch *alerts {
	case "":
	case "on", "off":
		on := *alerts == "on"
		p.SecurityAlerts = &on
	default:
		return usageError(fs, "--security-alerts takes on or off, got %q", *alerts)
	}
	if p.Updates == nil && p.SecurityAlerts == nil && len(allow) == 0 && len(deny) == 0 {
		return usageError(fs, "machines set needs --updates, --allow-port, --deny-port or --security-alerts")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if len(allow) > 0 || len(deny) > 0 {
		var m machines.Overview
		if err := hub.do(http.MethodGet, machinePath(name, ""), nil, &m); err != nil {
			return err
		}
		ports := append(slices.Clone(m.SecurityAllowedPorts), allow...)
		ports = slices.DeleteFunc(ports, func(p string) bool { return slices.Contains(deny, p) })
		p.SecurityAllowedPorts = &ports
	}
	var m machines.Overview
	if err := hub.do(http.MethodPatch, machinePath(name, ""), p, &m); err != nil {
		return err
	}
	ports := "none"
	if len(m.SecurityAllowedPorts) > 0 {
		ports = strings.Join(m.SecurityAllowedPorts, ", ")
	}
	state := "on"
	if !m.SecurityAlerts {
		state = "off"
	}
	say(s.out, "%s: updates %s, allowed ports %s, security alerts %s.", m.Name, m.Updates, ports, state)
	return nil
}

// upgradeWait is how long "tender machines upgrade" waits for the upgrade, which the hub gives 45 minutes.
var upgradeWait = 45 * time.Minute

func machinesUpgrade(args []string, s session) error {
	fs := newFlagSet("machines upgrade", "NAME [--all-packages] [--hub URL]",
		"Upgrade a Debian or Ubuntu machine's system packages: the security updates, with unattended-upgrade, or every package with "+
			"--all-packages, with apt-get upgrade, keeping changed config files. The daemon runs it outside its sandbox, in the "+
			"tenderupgrade unit, and this waits for it and prints the end of its output. Tender never reboots the machine: "+
			"tender security says when it needs one.")
	hubURL := hubFlag(fs)
	all := fs.Bool("all-packages", false, "upgrade every package, not only the security updates")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a machine name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	packages := actions.PackagesSecurity
	if *all {
		packages = actions.PackagesAll
	}
	say(s.out, "Upgrading %s on %s. This can take a few minutes.", actions.PackagesText(packages), name)
	a, err := queueActionFor(hub, name, actions.Input{Type: actions.TypeUpgrade, Packages: &packages}, upgradeWait)
	if err != nil {
		return err
	}
	if a.Output != nil && *a.Output != "" {
		_, _ = io.WriteString(s.out, strings.TrimRight(*a.Output, "\n")+"\n")
	}
	say(s.out, "Upgraded %s on %s.", actions.PackagesText(packages), name)
	return nil
}
