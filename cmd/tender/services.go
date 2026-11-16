package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/services"
)

var servicesUsage = groupUsage("services", "See, deploy and control each machine's services. A service is a compose project, or a "+
	"container without one; its containers are named by their compose service, such as web. Tender deploys a service from a compose "+
	"file (a managed one), and watches, starts and stops the others it finds. The daemon on each machine reports them once a minute "+
	"and runs what you ask.", []row{
	{"list [MACHINE]", "List the services and their state"},
	{"show MACHINE SERVICE", "Show a service: its containers, routes, env keys and last deploy"},
	{"deploy MACHINE NAME -f FILE [options]", "Deploy a service from a compose file, or change one Tender deployed"},
	{"redeploy MACHINE SERVICE", "Deploy a service again as stored, pulling its images"},
	{"start MACHINE SERVICE [--container C]", "Start a service, or one of its containers"},
	{"stop MACHINE SERVICE [--container C]", "Stop a service, or one of its containers"},
	{"restart MACHINE SERVICE [--container C]", "Restart a service, or one of its containers"},
	{"logs MACHINE SERVICE [--container C] [--lines N] [--since D]", "Print the newest lines of a service's output"},
	{"alerts MACHINE SERVICE on|off", "Turn a service's alert emails on or off"},
	{"remove MACHINE SERVICE [--delete-data] --yes", "Remove a service Tender deployed"},
})

// runServices runs "tender services COMMAND".
func runServices(args []string, s session) error {
	return runGroup("services", servicesUsage, args, s.out, map[string]func([]string) error{
		"list":     func(a []string) error { return servicesList(a, s) },
		"show":     func(a []string) error { return servicesShow(a, s) },
		"deploy":   func(a []string) error { return servicesDeploy(a, s) },
		"redeploy": func(a []string) error { return servicesRedeploy(a, s) },
		"start":    func(a []string) error { return servicesAct(a, s, actions.TypeStart) },
		"stop":     func(a []string) error { return servicesAct(a, s, actions.TypeStop) },
		"restart":  func(a []string) error { return servicesAct(a, s, actions.TypeRestart) },
		"logs":     func(a []string) error { return servicesLogs(a, s) },
		"alerts":   func(a []string) error { return servicesAlerts(a, s) },
		"remove":   func(a []string) error { return servicesRemove(a, s) },
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

// count is n and word, plural unless n is 1: 1 container, 3 containers.
func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// serviceUsage sums the memory and CPU of a service's running containers, or "-" when none says.
func serviceUsage(sv services.Service) (string, string) {
	var mem int64
	var cpu float64
	var hasMem, hasCPU bool
	for _, c := range sv.Containers {
		if c.MemoryBytes != nil {
			mem, hasMem = mem+*c.MemoryBytes, true
		}
		if c.CPUPercent != nil {
			cpu, hasCPU = cpu+*c.CPUPercent, true
		}
	}
	m, p := "-", "-"
	if hasMem {
		m = formatBytes(mem)
	}
	if hasCPU {
		p = strconv.FormatFloat(cpu, 'f', 1, 64) + "%"
	}
	return m, p
}

func servicesList(args []string, s session) error {
	fs := newFlagSet("services list", "[MACHINE] [--hub URL]",
		"List every machine's services, or one machine's: running when every container that should run does, degraded when some "+
			"do, stopped when none do, and unknown while Docker is unreachable. It shows how many containers run, their memory and CPU "+
			"together from the latest report, where 100% is one whole CPU, whether Tender deployed the service and how its last deploy "+
			"went, and whether it sends alerts. tender services show has each container.")
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
		running, present, gone := 0, 0, 0
		for _, c := range sv.Containers {
			switch {
			case c.State == services.StateRemoved:
				gone++
			case c.Finished():
			default:
				present++
				if c.State == services.StateRunning {
					running++
				}
			}
		}
		containers := fmt.Sprintf("%d/%d", running, present)
		if gone > 0 && gone == len(sv.Containers) {
			containers = "removed"
		}
		deployed, alerts := "-", "on"
		if sv.DeployStatus != nil {
			deployed = *sv.DeployStatus
		}
		if !sv.AlertsEnabled {
			alerts = "off"
		}
		mem, cpu := serviceUsage(sv)
		rows[i] = []string{sv.Machine, sv.Name, sv.State, containers, mem, cpu, deployed, alerts}
	}
	printTable(s.out, []string{"MACHINE", "SERVICE", "STATE", "RUNNING", "MEM", "CPU", "DEPLOY", "ALERTS"}, rows)
	return nil
}

// containerState words a container's state: the exit code once stopped, a stop of Tender's, and
// when a removed one went.
func containerState(c services.Container, now time.Time) string {
	state := c.State
	switch {
	case c.State == services.StateRemoved && c.RemovedAt != nil:
		state += " " + ago(now, *c.RemovedAt)
	case c.State == services.StateUnknown, c.State == services.StateRemoved: // what Docker said last no longer holds
	default:
		if c.ExitCode != nil && (c.State == services.StateExited || c.State == services.StateDead) {
			state += fmt.Sprintf(" (%d)", *c.ExitCode)
		}
		if c.StoppedByTender {
			state += ", by Tender"
		}
	}
	return state
}

func servicesShow(args []string, s session) error {
	fs := newFlagSet("services show", "MACHINE SERVICE [--hub URL]",
		"Show one service: its state, how its last deploy went and why it failed, its routes with where they answer, its env keys "+
			"(never their values) and files, and each container with its state, health, memory and CPU from the latest report, how often "+
			"Docker restarted it and when it started.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	machine, name, err := twoArgs(fs, rest)
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var sv services.Service
	if err := hub.do(http.MethodGet, servicePath(machine, name, ""), nil, &sv); err != nil {
		return err
	}
	printService(s, sv)
	return nil
}

// printService prints a service as tender services show does.
func printService(s session, sv services.Service) {
	say(s.out, "Service %s on %s: %s.", sv.Name, sv.Machine, sv.State)
	if !sv.Managed {
		say(s.out, "Tender found it running and didn't deploy it, so it can start, stop and watch it, but not deploy it.")
	} else {
		line := "Deploy: " + *sv.DeployStatus
		if sv.DeployedAt != nil {
			line += ", last succeeded " + ago(s.now(), *sv.DeployedAt)
		}
		if sv.ActionID != nil {
			line += fmt.Sprintf(" (action %d)", *sv.ActionID)
		}
		say(s.out, "%s.", line)
		if sv.LastError != nil {
			say(s.out, "Why the last one failed:\n%s", strings.TrimRight(*sv.LastError, "\n"))
		}
	}
	if !sv.AlertsEnabled {
		say(s.out, "Its alerts are off.")
	}
	if len(sv.Routes) > 0 {
		say(s.out, "")
		rows := make([][]string, len(sv.Routes))
		for i, r := range sv.Routes {
			at := "the tailnet name isn't known yet"
			if r.URL != nil {
				at = *r.URL
			}
			expose := r.Expose
			if r.Access {
				expose += " with access"
			}
			rows[i] = []string{r.Container, strconv.Itoa(r.Port), expose, "127.0.0.1:" + strconv.Itoa(r.LocalPort), at}
		}
		printTable(s.out, []string{"CONTAINER", "PORT", "EXPOSE", "LOCAL", "URL"}, rows)
	}
	if keys := sortedKeys(sv.Env); len(keys) > 0 {
		say(s.out, "\nEnv: %s.", strings.Join(keys, ", "))
	}
	if files := sortedKeys(sv.Files); len(files) > 0 {
		say(s.out, "Files: %s.", strings.Join(files, ", "))
	}
	say(s.out, "")
	if len(sv.Containers) == 0 {
		say(s.out, "It has no containers yet.")
		return
	}
	rows := make([][]string, len(sv.Containers))
	for i, c := range sv.Containers {
		health, started := "-", "-"
		if c.Health != nil {
			health = *c.Health
		}
		if c.StartedAt != nil {
			started = ago(s.now(), *c.StartedAt)
		}
		mem, cpu := "-", "-"
		if c.MemoryBytes != nil {
			mem = formatBytes(*c.MemoryBytes)
			if c.MemoryLimitBytes != nil {
				mem += " / " + formatBytes(*c.MemoryLimitBytes)
			}
		}
		if c.CPUPercent != nil {
			cpu = strconv.FormatFloat(*c.CPUPercent, 'f', 1, 64) + "%"
		}
		rows[i] = []string{c.Name, c.Image, containerState(c, s.now()), health, mem, cpu, strconv.FormatInt(c.RestartCount, 10), started}
	}
	printTable(s.out, []string{"CONTAINER", "IMAGE", "STATE", "HEALTH", "MEM", "CPU", "RESTARTS", "STARTED"}, rows)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// deployWait is how long deploy, redeploy and remove wait for the daemon: compose may pull large
// images and wait up to 5 minutes for health checks. The hub gives the action 45 minutes.
const deployWait = 45 * time.Minute

// queued is the hub's answer to a route that queues an action for a service.
type queued struct {
	Service services.Service `json:"service"`
	Action  actions.Action   `json:"action"`
}

// awaitService waits for the service's action unless noWait, then says how it went: the URLs of
// its routes after a deploy.
func awaitService(s session, hub *hubAPI, machine string, q queued, noWait bool, done string) error {
	if noWait {
		say(s.out, "Queued action %d. Follow it with: tender actions show %s %d", q.Action.ID, machine, q.Action.ID)
		return nil
	}
	if _, err := awaitActionFor(hub, machine, q.Action, deployWait); err != nil {
		return err
	}
	say(s.out, "%s", done)
	if q.Action.Type != actions.TypeDeploy {
		return nil
	}
	var sv services.Service
	if err := hub.do(http.MethodGet, servicePath(machine, q.Service.Name, ""), nil, &sv); err != nil {
		return err
	}
	for _, r := range sv.Routes {
		if r.URL != nil {
			say(s.out, "%s answers at %s", r.Container, *r.URL)
		} else {
			say(s.out, "%s answers on the tailnet at HTTPS port %d, once the daemon says the machine's tailnet name.", r.Container, deref(r.HTTPSPort))
		}
	}
	return nil
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// parseRoute reads --route CONTAINER:PORT:EXPOSE[:HOST], or tailscale:HTTPS_PORT as its last part,
// or CONTAINER:PORT:cloudflare:HOST:access for a cloudflare route that requires Cloudflare Access.
func parseRoute(fs *flag.FlagSet, v string) (services.RouteInput, error) {
	parts := strings.Split(v, ":")
	if len(parts) < 3 || len(parts) > 5 {
		return services.RouteInput{}, usageError(fs, "--route %q must be CONTAINER:PORT:EXPOSE, CONTAINER:PORT:EXPOSE:HOST or CONTAINER:PORT:cloudflare:HOST:access, "+
			"such as web:80:tailscale or web:80:public:app.example.com", v)
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return services.RouteInput{}, usageError(fs, "--route %q: the port %q is not a number", v, parts[1])
	}
	r := services.RouteInput{Container: parts[0], Port: port, Expose: parts[2]}
	if len(parts) >= 4 {
		if r.Expose == "tailscale" {
			https, err := strconv.Atoi(parts[3])
			if err != nil {
				return services.RouteInput{}, usageError(fs, "--route %q: a tailscale route's last part is its HTTPS port, a number", v)
			}
			r.HTTPSPort = &https
		} else {
			r.Host = &parts[3]
		}
	}
	if len(parts) == 5 {
		switch {
		case r.Expose != "cloudflare":
			return services.RouteInput{}, usageError(fs, "--route %q: only a cloudflare route has a fifth part, access, such as web:80:cloudflare:app.example.com:access", v)
		case parts[4] != "access":
			return services.RouteInput{}, usageError(fs, "--route %q: a cloudflare route's fifth part can only be access, which requires Cloudflare Access at its host, got %q", v, parts[4])
		}
		r.Access = true
	}
	return r, nil
}

// readEnvFile reads KEY=VALUE lines, skipping blank lines and comments, and takes one pair of
// quotes off a value.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			return nil, fmt.Errorf("%s line %d isn't KEY=VALUE", path, n)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, sc.Err()
}

func servicesDeploy(args []string, s session) error {
	fs := newFlagSet("services deploy", "MACHINE NAME -f FILE [--file PATH ...] [--env-file PATH] [--env KEY=VALUE ...] "+
		"[--route CONTAINER:PORT:EXPOSE[:HOST[:access]] ...] [--no-wait] [--hub URL]",
		"Deploy a service from a docker compose file, through the daemon on the machine, and wait for it. NAME is its compose project: "+
			"lowercase letters, digits and dashes, starting with a letter, at most 32. The daemon checks the file against Tender's rules first "+
			"(images only, no ports, no host namespaces, privileges or devices, mounts inside the service's folder, a memory limit on "+
			"each compose service) and says which rule failed and how to fix it. A service Tender deployed already changes: the file "+
			"replaces its compose file, and files, env and routes are replaced when their options are given and kept when not. "+
			"A route publishes a container's port on 127.0.0.1 and exposes it: tailscale on the machine's tailnet name "+
			"(CONTAINER:PORT:tailscale, or :tailscale:HTTPS_PORT for a port of your choice), cloudflare or public at a host. "+
			"CONTAINER:PORT:cloudflare:HOST:access also requires Cloudflare Access: the tunnel lets only requests with a valid Access token reach the container. "+
			"Env values go to the service's .env for the file's ${KEY}s; the hub keeps them encrypted.")
	hubURL := hubFlag(fs)
	compose := fs.String("f", "", "`FILE` the docker compose file")
	var files, envs, routes listFlag
	fs.Var(&files, "file", "`PATH` a config file the compose file mounts, relative to it; repeat for each")
	envFile := fs.String("env-file", "", "`PATH` a file of KEY=VALUE lines for the env")
	fs.Var(&envs, "env", "`KEY=VALUE` one env value, over the env file's; repeat for each")
	fs.Var(&routes, "route", "`CONTAINER:PORT:EXPOSE[:HOST[:access]]` a route, such as web:80:tailscale or web:80:cloudflare:app.example.com:access; repeat for each")
	noWait := fs.Bool("no-wait", false, "queue the deploy and return, without waiting for it")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	machine, name, err := twoArgs(fs, rest)
	if err != nil {
		return err
	}
	if *compose == "" {
		return usageError(fs, "services deploy needs the compose file: -f compose.yaml")
	}
	text, err := os.ReadFile(*compose)
	if err != nil {
		return fmt.Errorf("cannot read the compose file: %w", err)
	}
	base := filepath.Dir(*compose)
	fileSet := map[string]string{}
	for _, p := range files {
		rel, err := filepath.Rel(base, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return usageError(fs, "--file %s is outside the compose file's folder, %s; a service's files are relative to its compose file", p, base)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("cannot read %s: %w", p, err)
		}
		fileSet[filepath.ToSlash(rel)] = string(b)
	}
	envSet := map[string]string{}
	if *envFile != "" {
		if envSet, err = readEnvFile(*envFile); err != nil {
			return fmt.Errorf("cannot read the env file: %w", err)
		}
	}
	for _, kv := range envs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return usageError(fs, "--env %q must be KEY=VALUE", kv)
		}
		envSet[k] = v
	}
	routeList := []services.RouteInput{}
	for _, v := range routes {
		r, err := parseRoute(fs, v)
		if err != nil {
			return err
		}
		routeList = append(routeList, r)
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var current services.Service
	err = hub.do(http.MethodGet, servicePath(machine, name, ""), nil, &current)
	var q queued
	switch {
	// A service Tender only watches is created too: the hub takes its name over once its containers
	// are down, and otherwise says how to take them down.
	case answered(err, http.StatusNotFound), err == nil && !current.Managed:
		in := services.Input{Name: name, Compose: string(text), Files: fileSet, Env: envSet, Routes: routeList}
		if err := hub.do(http.MethodPost, machinePath(machine, "/services"), in, &q); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		patch := map[string]any{"compose": string(text)}
		if len(files) > 0 {
			patch["files"] = replacing(current.Files, fileSet)
		}
		if *envFile != "" || len(envs) > 0 {
			patch["env"] = replacing(current.Env, envSet)
		}
		if len(routes) > 0 {
			patch["routes"] = routeList
		}
		if err := hub.do(http.MethodPatch, servicePath(machine, name, ""), patch, &q); err != nil {
			return err
		}
	}
	return awaitService(s, hub, machine, q, *noWait, fmt.Sprintf("Deployed %s on %s.", name, machine))
}

// replacing is the merge patch that turns old's keys into exactly set's: each of set, and null
// for each key of old that set lacks.
func replacing(old, set map[string]string) map[string]*string {
	out := map[string]*string{}
	for k := range old {
		out[k] = nil
	}
	for k, v := range set {
		out[k] = &v
	}
	return out
}

func servicesRedeploy(args []string, s session) error {
	fs := newFlagSet("services redeploy", "MACHINE SERVICE [--no-wait] [--hub URL]",
		"Deploy a service Tender deployed again, as the hub stores it, and wait for it: compose pulls the images again, so a new "+
			"image under the same tag runs, and recreates the containers that changed.")
	hubURL := hubFlag(fs)
	noWait := fs.Bool("no-wait", false, "queue the deploy and return, without waiting for it")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	machine, name, err := twoArgs(fs, rest)
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var q queued
	if err := hub.do(http.MethodPost, servicePath(machine, name, "/deploy"), nil, &q); err != nil {
		return err
	}
	return awaitService(s, hub, machine, q, *noWait, fmt.Sprintf("Deployed %s on %s again.", name, machine))
}

func servicesRemove(args []string, s session) error {
	fs := newFlagSet("services remove", "MACHINE SERVICE [--delete-data] --yes [--no-wait] [--hub URL]",
		"Remove a service Tender deployed, through the daemon on the machine: docker compose down stops and removes its containers, "+
			"its routes stop answering, and the hub forgets it. Its folder, with its named volumes' data, is kept in "+
			"/var/lib/tender/services/.removed/ on the machine, unless --delete-data deletes it. There is no undo, so the command needs --yes.")
	hubURL := hubFlag(fs)
	deleteData := fs.Bool("delete-data", false, "delete the service's folder and its data too")
	yes := fs.Bool("yes", false, "confirm the removal")
	noWait := fs.Bool("no-wait", false, "queue the remove and return, without waiting for it")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	machine, name, err := twoArgs(fs, rest)
	if err != nil {
		return err
	}
	if !*yes {
		what := "keeping its data on the machine"
		if *deleteData {
			what = "and deletes its data, with no undo"
		}
		return fmt.Errorf("this removes %s from %s, %s. Run \"tender services remove %s %s%s --yes\" to confirm", name, machine, what, machine, name,
			map[bool]string{true: " --delete-data"}[*deleteData])
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	path := servicePath(machine, name, "")
	if *deleteData {
		path += "?delete_data=true"
	}
	var q queued
	if err := hub.do(http.MethodDelete, path, nil, &q); err != nil {
		return err
	}
	if err := awaitService(s, hub, machine, q, *noWait, fmt.Sprintf("Removed %s from %s.", name, machine)); err != nil {
		return err
	}
	if !*noWait {
		var a actions.Action
		if err := hub.do(http.MethodGet, actionsPath(machine, q.Action.ID), nil, &a); err == nil && a.Output != nil {
			_, err = io.WriteString(s.out, *a.Output)
			return err
		}
	}
	return nil
}

var (
	actionSummary = map[string]string{
		actions.TypeStart:   "Start a service, or one of its containers. Starting a running one changes nothing.",
		actions.TypeStop:    "Stop a service, or one of its containers, with each container's own stop timeout. A container Tender stopped doesn't alert as down until it runs again.",
		actions.TypeRestart: "Restart a service, or one of its containers.",
	}
	actionDone = map[string]string{actions.TypeStart: "Started", actions.TypeStop: "Stopped", actions.TypeRestart: "Restarted"}
)

func servicesAct(args []string, s session, typ string) error {
	fs := newFlagSet("services "+typ, "MACHINE SERVICE [--container C] [--hub URL]",
		actionSummary[typ]+" A service Tender deployed goes through docker compose, any other container by container. The daemon on "+
			"the machine does it; this waits up to 60 seconds for it.")
	hubURL := hubFlag(fs)
	container := fs.String("container", "", "`C` only this container of the service, such as web")
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
	var body any
	what := service
	if *container != "" {
		body = map[string]string{"container": *container}
		what = *container + " in " + service
	}
	var q queued
	if err := hub.do(http.MethodPost, servicePath(machine, service, "/"+typ), body, &q); err != nil {
		return err
	}
	if _, err := awaitAction(hub, machine, q.Action); err != nil {
		return err
	}
	say(s.out, "%s %s on %s.", actionDone[typ], what, machine)
	return nil
}

func servicesLogs(args []string, s session) error {
	fs := newFlagSet("services logs", "MACHINE SERVICE [--container C] [--lines N] [--since D] [--hub URL]",
		"Print the newest lines of a service's output, stdout and stderr together, each line starting with Docker's timestamp. With "+
			"several containers, their lines are merged by time, each with its container's name. The daemon on the machine reads them; "+
			"this waits up to 60 seconds for it. Logs can hold secrets: the hub clears them 1 hour after they were read.")
	hubURL := hubFlag(fs)
	container := fs.String("container", "", "`C` only this container of the service, such as web")
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
	what := service + " on " + machine
	if *container != "" {
		what = *container + " in " + what
	}
	a, err := queueAction(hub, machine, actions.Input{Type: actions.TypeLogs, Service: &service, Container: optional(*container), Lines: lines,
		Since: optional(*since)})
	if err != nil {
		return err
	}
	return printLogs(s, a, what)
}

func servicesAlerts(args []string, s session) error {
	fs := newFlagSet("services alerts", "MACHINE SERVICE on|off [--hub URL]",
		"Turn a service's alert emails on or off, for all its containers. They are on for every new service: an email when a "+
			"container stops, keeps restarting, turns unhealthy or uses more than 90% of its memory limit, and when a deploy fails. "+
			"Off, its open problems resolve within a minute.")
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
	if err := hub.do(http.MethodPatch, servicePath(machine, service, ""), map[string]bool{"alerts_enabled": on}, &sv); err != nil {
		return err
	}
	if on {
		say(s.out, "Alerts for %s on %s are on.", service, machine)
	} else {
		say(s.out, "Alerts for %s on %s are off. Its open problems resolve within a minute.", service, machine)
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

var registriesUsage = groupUsage("registries", "Keep the logins of the container registries that services Tender deploys pull "+
	"private images from. A deploy uses the login of the registry whose host an image is on; the hub keeps the passwords encrypted.", []row{
	{"list", "List the registries"},
	{"add NAME --host HOST --username USER [--password-file PATH]", "Add a registry's login"},
	{"update NAME [--host HOST] [--username USER] [--password] [--password-file PATH]", "Change a registry's login"},
	{"remove NAME", "Remove a registry's login"},
})

// runRegistries runs "tender registries list|add|update|remove".
func runRegistries(args []string, s session) error {
	return runGroup("registries", registriesUsage, args, s.out, map[string]func([]string) error{
		"list":   func(a []string) error { return registriesList(a, s) },
		"add":    func(a []string) error { return registriesAdd(a, s) },
		"update": func(a []string) error { return registriesUpdate(a, s) },
		"remove": func(a []string) error { return registriesRemove(a, s) },
	})
}

func registryPath(name string) string { return "/v1/registries/" + url.PathEscape(name) }

func registriesList(args []string, s session) error {
	fs := newFlagSet("registries list", "[--hub URL]", "List the registries: name, host and username. Passwords are never shown.")
	hubURL := hubFlag(fs)
	if err := parseFlags(fs, args, s.out); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError(fs, "registries list takes no arguments")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	list, err := listAll[services.Registry](hub, "/v1/registries")
	if err != nil {
		return err
	}
	if len(list) == 0 {
		say(s.out, "No registries yet. Add one with: tender registries add NAME --host ghcr.io --username USER")
		return nil
	}
	rows := make([][]string, len(list))
	for i, g := range list {
		rows[i] = []string{g.Name, g.Host, g.Username}
	}
	printTable(s.out, []string{"NAME", "HOST", "USERNAME"}, rows)
	return nil
}

func registriesAdd(args []string, s session) error {
	fs := newFlagSet("registries add", "NAME --host HOST --username USER [--password-file PATH] [--hub URL]",
		"Add the login of a container registry, such as ghcr.io, for services to pull private images. It asks for the password or "+
			"access token, with typing hidden, unless --password-file has it.")
	hubURL := hubFlag(fs)
	host := fs.String("host", "", "`HOST` the registry's host, as image names start with it, such as ghcr.io; docker.io for Docker Hub")
	username := fs.String("username", "", "`USER` the login name")
	passwordFile := fs.String("password-file", "", "`PATH` a file whose first line is the password or access token")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a registry name")
	if err != nil {
		return err
	}
	if *host == "" || *username == "" {
		return usageError(fs, "registries add needs --host and --username")
	}
	password, err := s.secret("password", *passwordFile, "password-file")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var g services.Registry
	if err := hub.do(http.MethodPost, "/v1/registries", services.RegistryInput{Name: name, Host: *host, Username: *username, Password: password}, &g); err != nil {
		return err
	}
	say(s.out, "Added registry %s for %s.", g.Name, g.Host)
	return nil
}

func registriesUpdate(args []string, s session) error {
	fs := newFlagSet("registries update", "NAME [--host HOST] [--username USER] [--password] [--password-file PATH] [--hub URL]",
		"Change a registry's host, username or password; what isn't given stays. --password asks for a new one, with typing hidden.")
	hubURL := hubFlag(fs)
	host := fs.String("host", "", "`HOST` a new host")
	username := fs.String("username", "", "`USER` a new login name")
	ask := fs.Bool("password", false, "ask for a new password or access token")
	passwordFile := fs.String("password-file", "", "`PATH` a file whose first line is a new password or access token")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a registry name")
	if err != nil {
		return err
	}
	var p services.RegistryPatch
	if *host != "" {
		p.Host = host
	}
	if *username != "" {
		p.Username = username
	}
	if *ask || *passwordFile != "" {
		password, err := s.secret("password", *passwordFile, "password-file")
		if err != nil {
			return err
		}
		p.Password = &password
	}
	if p.Host == nil && p.Username == nil && p.Password == nil {
		return usageError(fs, "registries update needs --host, --username, --password or --password-file")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if err := hub.do(http.MethodPatch, registryPath(name), p, nil); err != nil {
		return err
	}
	say(s.out, "Updated registry %s.", name)
	return nil
}

func registriesRemove(args []string, s session) error {
	fs := newFlagSet("registries remove", "NAME [--hub URL]",
		"Remove a registry's login. Services whose images are on it can no longer pull them, unless they are public.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a registry name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if err := hub.do(http.MethodDelete, registryPath(name), nil, nil); err != nil {
		return err
	}
	say(s.out, "Removed registry %s.", name)
	return nil
}
