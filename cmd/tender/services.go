package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/services"
)

var servicesUsage = groupUsage("services", "See and control each machine's Docker services: one per container, by its container name. "+
	"The daemon on each machine reports them once a minute and runs what you ask.", []row{
	{"list [MACHINE]", "List the services and their state"},
	{"start MACHINE SERVICE", "Start a service"},
	{"stop MACHINE SERVICE", "Stop a service"},
	{"restart MACHINE SERVICE", "Restart a service"},
	{"logs MACHINE SERVICE [--lines N]", "Print a service's last lines of output"},
	{"alerts MACHINE SERVICE on|off", "Turn a service's alert emails on or off"},
})

// How long a command waits for the daemon to finish an action, and how often it asks. Tests shorten them.
var (
	actionWait  = services.ActionWait
	actionCheck = time.Second
)

// runServices runs "tender services list|start|stop|restart|logs|alerts".
func runServices(args []string, s session) error {
	return runGroup("services", servicesUsage, args, s.out, map[string]func([]string) error{
		"list":    func(a []string) error { return servicesList(a, s) },
		"start":   func(a []string) error { return servicesAct(a, s, services.TypeStart) },
		"stop":    func(a []string) error { return servicesAct(a, s, services.TypeStop) },
		"restart": func(a []string) error { return servicesAct(a, s, services.TypeRestart) },
		"logs":    func(a []string) error { return servicesLogs(a, s) },
		"alerts":  func(a []string) error { return servicesAlerts(a, s) },
	})
}

func servicePath(machine, service, rest string) string {
	return machinePath(machine, "/services/"+url.PathEscape(service)+rest)
}

// twoArgs returns MACHINE and SERVICE, or a usage error that names what is missing.
func twoArgs(fs *flag.FlagSet, args []string) (string, string, error) {
	switch len(args) {
	case 2:
		return args[0], args[1], nil
	case 0, 1:
		return "", "", usageError(fs, "%s needs a machine name and a service name", fs.Name())
	default:
		return "", "", usageError(fs, "%s takes a machine and a service, got %d arguments: %s", fs.Name(), len(args), strings.Join(args, " "))
	}
}

func servicesList(args []string, s session) error {
	fs := newFlagSet("services list", "[MACHINE] [--hub URL]",
		"List every machine's Docker services, or one machine's: the state Docker reports (with the exit code once stopped), "+
			"the health check, how often Docker restarted it, when it started and whether it sends alerts.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usageError(fs, "services list takes at most one machine name, got %d", len(rest))
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var machines []services.MachineServices
	if len(rest) == 1 {
		var one services.MachineServices
		if err := hub.do(http.MethodGet, machinePath(rest[0], "/services"), nil, &one); err != nil {
			return err
		}
		machines = []services.MachineServices{one}
	} else if machines, err = listAll[services.MachineServices](hub, "/v1/services"); err != nil {
		return err
	}
	var list []services.Service
	var unreachable []string
	for _, m := range machines {
		list = append(list, m.Data...)
		if m.Docker != nil && *m.Docker == services.DockerUnreachable {
			unreachable = append(unreachable, m.Machine)
		}
	}
	defer func() {
		for _, m := range unreachable {
			say(s.out, "\nDocker is unreachable on %s, so the states of its services are unknown.", m)
		}
	}()
	if len(list) == 0 {
		if len(rest) == 1 {
			say(s.out, "%s has no services. Its daemon reports the Docker containers once a minute; a machine without Docker has none.", rest[0])
		} else {
			say(s.out, "No services yet. Each daemon reports its machine's Docker containers once a minute.")
		}
		return nil
	}
	rows := make([][]string, len(list))
	for i, sv := range list {
		state := sv.State
		if sv.ExitCode != nil {
			state += fmt.Sprintf(" (%d)", *sv.ExitCode)
		}
		if sv.StoppedByTender {
			state += ", by Tender"
		}
		health, started, alerts := "-", "-", "on"
		if sv.Health != nil {
			health = *sv.Health
		}
		if sv.StartedAt != nil {
			started = ago(s.now(), *sv.StartedAt)
		}
		if !sv.AlertsEnabled {
			alerts = "off"
		}
		rows[i] = []string{sv.Machine, sv.Name, state, health, strconv.FormatInt(sv.RestartCount, 10), started, alerts}
	}
	printTable(s.out, []string{"MACHINE", "SERVICE", "STATE", "HEALTH", "RESTARTS", "STARTED", "ALERTS"}, rows)
	return nil
}

var (
	actionSummary = map[string]string{
		services.TypeStart:   "Start a service. Starting a running service changes nothing.",
		services.TypeStop:    "Stop a service, with the container's own stop timeout. A service Tender stopped doesn't alert as down until it runs again.",
		services.TypeRestart: "Restart a service.",
	}
	actionDone = map[string]string{services.TypeStart: "Started", services.TypeStop: "Stopped", services.TypeRestart: "Restarted"}
)

func servicesAct(args []string, s session, typ string) error {
	fs := newFlagSet("services "+typ, "MACHINE SERVICE [--hub URL]",
		actionSummary[typ]+" The daemon on the machine does it; this waits up to 60 seconds for it.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	machine, service, err := twoArgs(fs, rest)
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if _, err := runAction(hub, machine, service, services.ActionInput{Type: typ}); err != nil {
		return err
	}
	say(s.out, "%s %s on %s.", actionDone[typ], service, machine)
	return nil
}

func servicesLogs(args []string, s session) error {
	fs := newFlagSet("services logs", "MACHINE SERVICE [--lines N] [--hub URL]",
		"Print a service's last lines of output, stdout and stderr together, each line starting with Docker's timestamp. "+
			"The daemon on the machine reads them; this waits up to 60 seconds for it.")
	hubURL := hubFlag(fs)
	lines := fs.Int("lines", services.DefaultLines, "`N` how many of the last lines, up to 2000")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	machine, service, err := twoArgs(fs, rest)
	if err != nil {
		return err
	}
	if *lines < 1 || *lines > services.MaxLines {
		return usageError(fs, "--lines must be from 1 to %d", services.MaxLines)
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	a, err := runAction(hub, machine, service, services.ActionInput{Type: services.TypeLogs, Lines: lines})
	if err != nil {
		return err
	}
	if a.Output == nil || *a.Output == "" {
		say(s.out, "%s on %s has no logs.", service, machine)
		return nil
	}
	_, err = io.WriteString(s.out, *a.Output)
	return err
}

// runAction queues an action and waits for the daemon to finish it. A failed action is an error
// with the daemon's reason; one still waiting after actionWait is an error that says how to check.
func runAction(hub *hubAPI, machine, service string, in services.ActionInput) (services.Action, error) {
	var a services.Action
	if err := hub.do(http.MethodPost, servicePath(machine, service, "/actions"), in, &a); err != nil {
		return a, err
	}
	path := machinePath(machine, "/service-actions/"+strconv.FormatInt(a.ID, 10))
	deadline := time.Now().Add(actionWait)
	for !a.Finished() && time.Now().Before(deadline) {
		time.Sleep(min(actionCheck, time.Until(deadline)))
		if err := hub.do(http.MethodGet, path, nil, &a); err != nil {
			return a, err
		}
	}
	switch {
	case a.Status == services.StatusFailed && a.Output != nil:
		return a, errors.New(*a.Output)
	case a.Status == services.StatusFailed:
		return a, fmt.Errorf("the daemon on %s could not do this.", machine)
	case a.Finished():
		return a, nil
	}
	check := "Check with: tender services list " + machine
	if in.Type == services.TypeLogs {
		check = "Its logs will be at GET " + path + " on the hub, or run this again later."
	}
	return a, fmt.Errorf("the daemon on %s hasn't finished within %d seconds. The %s of %s stays queued for 15 minutes and runs "+
		"when the daemon picks it up. %s", machine, int(actionWait.Seconds()), in.Type, service, check)
}

func servicesAlerts(args []string, s session) error {
	fs := newFlagSet("services alerts", "MACHINE SERVICE on|off [--hub URL]",
		"Turn a service's alert emails on or off. They are on for every new service: an email when it stops, keeps restarting "+
			"or turns unhealthy. Off, its open alerts resolve within a minute. The switch follows the service's name.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if len(rest) != 3 || (rest[2] != "on" && rest[2] != "off") {
		return usageError(fs, "services alerts needs a machine, a service and on or off")
	}
	machine, service, on := rest[0], rest[1], rest[2] == "on"
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var sv services.Service
	if err := hub.do(http.MethodPatch, servicePath(machine, service, ""), services.ServicePatch{AlertsEnabled: &on}, &sv); err != nil {
		return err
	}
	if on {
		say(s.out, "Alerts for %s on %s are on.", service, machine)
	} else {
		say(s.out, "Alerts for %s on %s are off. Its open alerts resolve within a minute.", service, machine)
	}
	return nil
}
