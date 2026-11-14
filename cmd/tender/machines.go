package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/g4f4r0/tender/internal/machines"
)

var machinesUsage = groupUsage("machines", "Add the machines that back up, and manage their tokens.", []row{
	{"list", "List the machines and when each last reported"},
	{"add NAME", "Add a machine and print its token once"},
	{"remove NAME --yes", "Remove a machine and its history from the hub"},
	{"rotate-token NAME", "Replace a machine's token and print the new one"},
})

// runMachines runs "tender machines list|add|remove|rotate-token".
func runMachines(args []string, s session) error {
	return runGroup("machines", machinesUsage, args, s.out, map[string]func([]string) error{
		"list":         func(a []string) error { return machinesList(a, s) },
		"add":          func(a []string) error { return machinesAdd(a, s) },
		"remove":       func(a []string) error { return machinesRemove(a, s) },
		"rotate-token": func(a []string) error { return machinesRotate(a, s) },
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
		rows[i] = []string{m.Name, "never", "-", "-"}
		if m.LastSeen != nil {
			rows[i][1] = ago(s.now(), *m.LastSeen)
		}
		if m.Usage != nil {
			rows[i][2], rows[i][3] = fmt.Sprintf("%.0f%%", m.Usage.MemoryUsedPercent), fmt.Sprintf("%.0f%%", m.Usage.DiskUsedPercent)
		}
	}
	printTable(s.out, []string{"NAME", "LAST SEEN", "MEMORY", "DISK"}, rows)
	return nil
}

func machinesAdd(args []string, s session) error {
	fs := newFlagSet("machines add", "NAME [--hub URL]",
		"Add a machine and print its token. The token is shown once: save it, then give it to \"tender install agent\" on that machine.")
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
	say(s.out, "Install the agent on that machine; it asks for the token:")
	say(s.out, "  curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh \\")
	say(s.out, "    | sudo sh -s -- agent --hub %s --machine %s", agentHubURL(hub.url), created.Name)
	return nil
}

func machinesRotate(args []string, s session) error {
	fs := newFlagSet("machines rotate-token", "NAME [--hub URL]",
		"Replace a machine's token and print the new one, once. The old token stops working at once, so the agent stays "+
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
	say(s.out, "Give it to the agent on that machine: sudo tender install agent --token-file PATH")
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

// agentHubURL is the hub address to print in an agent install command: the address the person
// used, unless it points at this machine, which another machine can't reach.
func agentHubURL(hub string) string {
	u, err := url.Parse(hub)
	if err != nil {
		return "HUB_URL"
	}
	if host := u.Hostname(); host == "localhost" || net.ParseIP(host).IsLoopback() {
		return "HUB_URL"
	}
	return hub
}
