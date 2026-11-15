package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/services"
)

var servicesUsage = groupUsage("services", "See and control each machine's Docker services: one per container, by its container name. "+
	"The daemon on each machine reports them once a minute and runs what you ask.", []row{
	{"list [MACHINE]", "List the services and their state"},
	{"start MACHINE SERVICE", "Start a service"},
	{"stop MACHINE SERVICE", "Stop a service"},
	{"restart MACHINE SERVICE", "Restart a service"},
	{"logs MACHINE SERVICE [--lines N] [--since D]", "Print a service's newest lines of output"},
	{"alerts MACHINE SERVICE on|off", "Turn a service's alert emails on or off"},
})

// runServices runs "tender services list|start|stop|restart|logs|alerts".
func runServices(args []string, s session) error {
	return runGroup("services", servicesUsage, args, s.out, map[string]func([]string) error{
		"list":    func(a []string) error { return servicesList(a, s) },
		"start":   func(a []string) error { return servicesAct(a, s, actions.TypeStart) },
		"stop":    func(a []string) error { return servicesAct(a, s, actions.TypeStop) },
		"restart": func(a []string) error { return servicesAct(a, s, actions.TypeRestart) },
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
			"the health check, its memory (with its limit) and CPU from the latest report, where 100% is one whole CPU, how "+
			"often Docker restarted it, when it started and whether it sends alerts.")
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
		mem, cpu := "-", "-"
		if sv.MemoryBytes != nil {
			mem = formatBytes(*sv.MemoryBytes)
			if sv.MemoryLimitBytes != nil {
				mem += " / " + formatBytes(*sv.MemoryLimitBytes)
			}
		}
		if sv.CPUPercent != nil {
			cpu = strconv.FormatFloat(*sv.CPUPercent, 'f', 1, 64) + "%"
		}
		rows[i] = []string{sv.Machine, sv.Name, state, health, mem, cpu, strconv.FormatInt(sv.RestartCount, 10), started, alerts}
	}
	printTable(s.out, []string{"MACHINE", "SERVICE", "STATE", "HEALTH", "MEM", "CPU", "RESTARTS", "STARTED", "ALERTS"}, rows)
	return nil
}

var (
	actionSummary = map[string]string{
		actions.TypeStart:   "Start a service. Starting a running service changes nothing.",
		actions.TypeStop:    "Stop a service, with the container's own stop timeout. A service Tender stopped doesn't alert as down until it runs again.",
		actions.TypeRestart: "Restart a service.",
	}
	actionDone = map[string]string{actions.TypeStart: "Started", actions.TypeStop: "Stopped", actions.TypeRestart: "Restarted"}
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
	if _, err := queueAction(hub, machine, actions.Input{Type: typ, Service: &service}); err != nil {
		return err
	}
	say(s.out, "%s %s on %s.", actionDone[typ], service, machine)
	return nil
}

func servicesLogs(args []string, s session) error {
	fs := newFlagSet("services logs", "MACHINE SERVICE [--lines N] [--since D] [--hub URL]",
		"Print a service's newest lines of output, stdout and stderr together, each line starting with Docker's timestamp. "+
			"The daemon on the machine reads them; this waits up to 60 seconds for it. Logs can hold secrets: the hub clears "+
			"them 1 hour after they were read.")
	hubURL := hubFlag(fs)
	lines := fs.Int("lines", actions.DefaultLines, "`N` how many of the newest lines, up to 2000")
	since := fs.String("since", "", "`D` only lines from this far back, such as 30m, 1h or 7d, at most 7d")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	machine, service, err := twoArgs(fs, rest)
	if err != nil {
		return err
	}
	if err := checkLogFlags(fs, *lines, *since); err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	a, err := queueAction(hub, machine, actions.Input{Type: actions.TypeLogs, Service: &service, Lines: lines, Since: optional(*since)})
	if err != nil {
		return err
	}
	return printLogs(s, a, service+" on "+machine)
}

func servicesAlerts(args []string, s session) error {
	fs := newFlagSet("services alerts", "MACHINE SERVICE on|off [--hub URL]",
		"Turn a service's alert emails on or off. They are on for every new service: an email when it stops, keeps restarting, "+
			"turns unhealthy or uses more than 90% of its memory limit. Off, its open alerts resolve within a minute. The switch follows the service's name.")
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

// formatBytes is a size in binary units, such as 512 MiB or 1.5 GiB.
func formatBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v, i = v/1024, i+1
	}
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + " " + units[i]
}
