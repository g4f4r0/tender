package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services"
)

// completeAction is the fake daemon: it does every action at once.
func completeAction(a *actions.Action) {
	a.Status = actions.StatusCompleted
	switch {
	case a.Type == actions.TypeLogs && a.Service != nil:
		a.Output = ptr("2026-11-14T11:59:00.000000000Z started\n2026-11-14T11:59:30.000000000Z listening on :8080\n")
	case a.Type == actions.TypeLogs:
		a.Output = ptr("2026-11-14T11:59:00.000000000Z tenderd.service daemon started\n")
	case a.Type == actions.TypeRemove:
		kept := "/var/lib/tender/services/.removed/" + *a.Service + "-1794657600"
		a.Output = ptr("Kept the service's folder, data and all, in " + kept + ". Delete it when you no longer need it: sudo rm -rf " + kept + "\n")
	}
}

// lone is a service of one container, named as the service, in state.
func lone(name, state string) services.Service {
	sv := services.Service{Name: name, State: services.ServiceStopped, AlertsEnabled: true,
		Containers: []services.Container{{Name: name, Image: "img", State: state, RestartPolicy: "no"}}}
	switch state {
	case services.StateRunning:
		sv.State = services.ServiceRunning
	case services.StateUnknown:
		sv.State = services.ServiceUnknown
	}
	return sv
}

func (h *fakeHub) machineServices(name string) services.MachineServices {
	ms := services.MachineServices{Machine: name, Data: append([]services.Service{}, h.services[name]...)}
	if d, ok := h.docker[name]; ok {
		ms.Docker = &d
	}
	return ms
}

func (h *fakeHub) listServices(w http.ResponseWriter, r *http.Request) {
	if h.olderHub {
		problem(w, http.StatusNotFound, "No route %s %s.", r.Method, r.URL.Path)
		return
	}
	if name := r.PathValue("machine"); name != "" {
		if h.machine(w, r) != nil {
			server.WriteJSON(w, http.StatusOK, h.machineServices(name))
		}
		return
	}
	list := []services.MachineServices{}
	for _, m := range h.sortedMachines() {
		if _, ok := h.docker[m.Name]; ok || len(h.services[m.Name]) > 0 {
			list = append(list, h.machineServices(m.Name))
		}
	}
	server.WriteJSON(w, http.StatusOK, server.List[services.MachineServices]{Data: list})
}

func (h *fakeHub) service(w http.ResponseWriter, r *http.Request) *services.Service {
	m := h.machine(w, r)
	if m == nil {
		return nil
	}
	for i, sv := range h.services[m.Name] {
		if sv.Name == r.PathValue("service") {
			return &h.services[m.Name][i]
		}
	}
	problem(w, http.StatusNotFound, "Service %q doesn't exist on this machine.", r.PathValue("service"))
	return nil
}

func (h *fakeHub) getService(w http.ResponseWriter, r *http.Request) {
	if sv := h.service(w, r); sv != nil {
		server.WriteJSON(w, http.StatusOK, sv)
	}
}

// queue makes a pending action of typ for the service and answers 202 with both, as the hub does.
func (h *fakeHub) queue(w http.ResponseWriter, machine string, sv *services.Service, typ string, container *string) {
	a := &actions.Action{Summary: actions.Summary{ID: h.id(), Machine: machine, Type: typ, Service: &sv.Name, Container: container,
		Status: actions.StatusPending, CreatedAt: h.now, UpdatedAt: h.now}}
	h.actions[a.ID] = a
	if typ == actions.TypeDeploy || typ == actions.TypeRemove {
		sv.ActionID = &a.ID
		sv.DeployStatus = ptr(map[string]string{actions.TypeDeploy: services.DeployDeploying, actions.TypeRemove: services.DeployRemoving}[typ])
	}
	w.Header().Set("Location", "/v1/machines/"+machine+"/actions/"+strconv.FormatInt(a.ID, 10))
	server.WriteJSON(w, http.StatusAccepted, services.Queued{Service: *sv, Action: a})
}

// fakeRoutes is what the hub makes of a deploy's routes: a tailscale one on the tailnet name, the
// others at their host.
func fakeRoutes(in []services.RouteInput) []services.Route {
	out := []services.Route{}
	for i, r := range in {
		url := "https://apps.tail1234.ts.net"
		if r.Host != nil {
			url = "https://" + *r.Host
		}
		out = append(out, services.Route{ID: int64(i + 1), Container: r.Container, Port: r.Port, Expose: r.Expose, Host: r.Host,
			LocalPort: 20000 + i, URL: &url})
	}
	return out
}

func hidden(env map[string]string) map[string]string {
	out := map[string]string{}
	for k := range env {
		out[k] = "***"
	}
	return out
}

func (h *fakeHub) createService(w http.ResponseWriter, r *http.Request) {
	m := h.machine(w, r)
	if m == nil {
		return
	}
	var in services.Input
	if !decode(w, r, &in) {
		return
	}
	sv := services.Service{ID: h.id(), Machine: m.Name, Name: in.Name, Managed: true, State: services.ServiceStopped, Compose: &in.Compose,
		Files: in.Files, Env: hidden(in.Env), Routes: fakeRoutes(in.Routes), AlertsEnabled: true, Containers: []services.Container{}}
	// Like the hub, a service it only watches gives its name to the new one once its containers are gone.
	for _, old := range h.services[m.Name] {
		if old.Name == in.Name && !old.Managed && slices.ContainsFunc(old.Containers, func(c services.Container) bool { return c.State != services.StateRemoved }) {
			problem(w, http.StatusConflict, "Service %q already runs on this machine, and Tender didn't deploy it. Take its containers down "+
				"(docker compose -p %s down) before Tender deploys it, or give the new service another name.", in.Name, in.Name)
			return
		}
	}
	h.services[m.Name] = slices.DeleteFunc(h.services[m.Name], func(old services.Service) bool { return old.Name == in.Name && !old.Managed })
	h.services[m.Name] = append(h.services[m.Name], sv)
	h.queue(w, m.Name, &h.services[m.Name][len(h.services[m.Name])-1], actions.TypeDeploy, nil)
}

func (h *fakeHub) patchService(w http.ResponseWriter, r *http.Request) {
	sv := h.service(w, r)
	if sv == nil {
		return
	}
	var p map[string]json.RawMessage
	if !decode(w, r, &p) {
		return
	}
	if v, ok := p["alerts_enabled"]; ok {
		sv.AlertsEnabled = string(v) == "true"
		delete(p, "alerts_enabled")
	}
	if len(p) == 0 {
		server.WriteJSON(w, http.StatusOK, sv)
		return
	}
	if !sv.Managed {
		problem(w, http.StatusConflict, "Tender didn't deploy service %q, so it can't change it.", sv.Name)
		return
	}
	if raw, ok := p["env"]; ok {
		var env map[string]*string
		_ = json.Unmarshal(raw, &env)
		for k, v := range env {
			if v == nil {
				delete(sv.Env, k)
			} else {
				sv.Env[k] = "***"
			}
		}
	}
	if raw, ok := p["routes"]; ok {
		var routes []services.RouteInput
		_ = json.Unmarshal(raw, &routes)
		sv.Routes = fakeRoutes(routes)
	}
	h.queue(w, r.PathValue("machine"), sv, actions.TypeDeploy, nil)
}

func (h *fakeHub) removeService(w http.ResponseWriter, r *http.Request) {
	if sv := h.service(w, r); sv != nil {
		h.queue(w, r.PathValue("machine"), sv, actions.TypeRemove, nil)
	}
}

// actService answers POST .../deploy, start, stop and restart.
func (h *fakeHub) actService(w http.ResponseWriter, r *http.Request) {
	sv := h.service(w, r)
	if sv == nil {
		return
	}
	typ := r.PathValue("typ")
	var in struct {
		Container *string `json:"container"`
	}
	if r.ContentLength > 0 && !decode(w, r, &in) {
		return
	}
	if in.Container != nil && !slices.ContainsFunc(sv.Containers, func(c services.Container) bool { return c.Name == *in.Container }) {
		problem(w, http.StatusNotFound, "Container %q isn't in service %s.", *in.Container, sv.Name)
		return
	}
	h.queue(w, r.PathValue("machine"), sv, typ, in.Container)
}

func (h *fakeHub) createAction(w http.ResponseWriter, r *http.Request) {
	m := h.machine(w, r)
	if m == nil {
		return
	}
	var in actions.Input
	if !decode(w, r, &in) {
		return
	}
	if in.Service != nil && !slices.ContainsFunc(h.services[m.Name], func(sv services.Service) bool { return sv.Name == *in.Service }) {
		problem(w, http.StatusNotFound, "Service %q doesn't exist on this machine.", *in.Service)
		return
	}
	if in.Type == actions.TypeLogs && in.Lines == nil {
		in.Lines = ptr(actions.DefaultLines)
	}
	a := &actions.Action{Summary: actions.Summary{ID: h.id(), Machine: m.Name, Type: in.Type, Service: in.Service, Container: in.Container,
		Unit: in.Unit, Priority: in.Priority, Since: in.Since, Lines: in.Lines, Status: actions.StatusPending, CreatedAt: h.now, UpdatedAt: h.now}}
	h.actions[a.ID] = a
	server.WriteJSON(w, http.StatusAccepted, a)
}

func (h *fakeHub) listActions(w http.ResponseWriter, r *http.Request) {
	m := h.machine(w, r)
	if m == nil {
		return
	}
	list := []actions.Summary{}
	for _, a := range h.actions {
		if a.Machine == m.Name {
			list = append(list, a.Summary)
		}
	}
	slices.SortFunc(list, func(x, y actions.Summary) int { return int(y.ID - x.ID) })
	server.WriteJSON(w, http.StatusOK, server.List[actions.Summary]{Data: list})
}

func (h *fakeHub) getAction(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a := h.actions[id]
	if a == nil || a.Machine != r.PathValue("machine") {
		problem(w, http.StatusNotFound, "Action %d doesn't exist on this machine.", id)
		return
	}
	if h.daemon != nil && !a.Finished() {
		h.daemon(a)
	}
	server.WriteJSON(w, http.StatusOK, a)
}

func (h *fakeHub) addServices(t *testing.T, machine string, list ...services.Service) {
	t.Helper()
	m := h.machines[machine]
	if m == nil {
		t.Fatalf("no machine %s", machine)
	}
	for i := range list {
		list[i].ID, list[i].Machine = h.id(), machine
		if list[i].Env == nil {
			list[i].Env = map[string]string{}
		}
		if list[i].Files == nil {
			list[i].Files = map[string]string{}
		}
	}
	h.services[machine] = append(h.services[machine], list...)
	h.docker[machine] = services.DockerRunning
}

func ptr[T any](v T) *T { return &v }

// notes is a managed service of two containers: web running, and worker stopped by Tender.
func notes(now time.Time) services.Service {
	started, deployed := now.Add(-3*time.Hour), now.Add(-2*time.Hour)
	compose := "services: {}\n"
	return services.Service{Name: "notes", Managed: true, State: services.ServiceDegraded, DeployStatus: ptr(services.DeployDeployed),
		DeployedAt: &deployed, ActionID: ptr(int64(7)), Compose: &compose, AlertsEnabled: false,
		Env:   map[string]string{"SECRET_KEY": "***", "DB_URL": "***"},
		Files: map[string]string{"config/app.toml": "x = 1\n"},
		Routes: []services.Route{{ID: 1, Container: "web", Port: 8080, Expose: "tailscale", HTTPSPort: ptr(8443), LocalPort: 20000,
			URL: ptr("https://apps.tail1234.ts.net:8443")}},
		Containers: []services.Container{
			{Name: "web", Image: "ghcr.io/me/notes:1", State: services.StateRunning, Health: ptr("healthy"), StartedAt: &started,
				MemoryBytes: ptr(int64(1536 << 20)), MemoryLimitBytes: ptr(int64(2 << 30)), CPUPercent: ptr(2.5), RestartPolicy: "unless-stopped"},
			{Name: "worker", Image: "ghcr.io/me/notes:1", State: services.StateExited, ExitCode: ptr(int64(137)), RestartCount: 4,
				StoppedByTender: true, RestartPolicy: "unless-stopped"},
		}}
}

func TestServicesList(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	mustRun(t, h, a, "machines", "add", "apps")
	mustRun(t, h, a, "machines", "add", "code")
	if out := mustRun(t, h, a, "services", "list"); out != "No services yet. Each daemon reports its machine's Docker containers once a minute.\n" {
		t.Errorf("empty list = %q", out)
	}
	if out := mustRun(t, h, a, "services", "list", "code"); !strings.HasPrefix(out, "code has no services.") {
		t.Errorf("empty machine = %q", out)
	}
	proxy := lone("cli-proxy-api", services.StateRunning)
	proxy.Containers[0].MemoryBytes, proxy.Containers[0].CPUPercent = ptr(int64(200<<20)), ptr(12.5)
	h.addServices(t, "apps", proxy, notes(h.now))
	want := "MACHINE   SERVICE         STATE      RUNNING   MEM       CPU     DEPLOY     ALERTS\n" +
		"apps      cli-proxy-api   running    1/1       200 MiB   12.5%   -          on\n" +
		"apps      notes           degraded   1/2       1.5 GiB   2.5%    deployed   off\n"
	if got := mustRun(t, h, a, "services", "list"); got != want {
		t.Errorf("list =\n%s\nwant\n%s", got, want)
	}
	if got := mustRun(t, h, a, "services", "list", "apps"); !strings.Contains(got, "notes") {
		t.Errorf("one machine's list =\n%s", got)
	}
	// A service whose containers are all gone shows as removed.
	h.addServices(t, "code", lone("old-job", services.StateRemoved))
	if got := mustRun(t, h, a, "services", "list", "code"); !strings.Contains(got, "code      old-job   stopped   removed") {
		t.Errorf("removed service =\n%s", got)
	}
	delete(h.services, "code")
	delete(h.docker, "code")
	// A one-shot container that finished, such as a seed step, doesn't count against the running ones.
	seeded := lone("seeded", services.StateRunning)
	seeded.Containers = append(seeded.Containers, services.Container{Name: "seed", Image: "busybox", State: services.StateExited,
		ExitCode: ptr(int64(0)), RestartPolicy: services.PolicyNo})
	h.addServices(t, "code", seeded)
	if got := mustRun(t, h, a, "services", "list", "code"); !strings.Contains(got, "code      seeded    running   1/1") {
		t.Errorf("service with a finished one-shot container =\n%s", got)
	}
	delete(h.services, "code")
	delete(h.docker, "code")

	// Docker stops answering: the states are unknown, and the list says why.
	h.docker["apps"] = services.DockerUnreachable
	for i := range h.services["apps"] {
		h.services["apps"][i].State = services.ServiceUnknown
	}
	if got := mustRun(t, h, a, "services", "list"); !strings.Contains(got, "apps      cli-proxy-api   unknown") ||
		!strings.HasSuffix(got, "\nDocker is unreachable on apps, so the states of its services are unknown.\n") {
		t.Errorf("list while Docker is unreachable =\n%s", got)
	}
	mustFail(t, h, a, `Machine "nope" doesn't exist. List the machines with "tender machines list".`, "services", "list", "nope")
	if got := h.sent(); got[len(got)-1].Path != "/v1/machines/nope/services" {
		t.Errorf("last request = %+v", got[len(got)-1])
	}
}

func TestServicesShow(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	mustRun(t, h, a, "machines", "add", "apps")
	sv := notes(h.now)
	sv.LastError = ptr("Container notes-web-1 is unhealthy.\nweb exited")
	h.addServices(t, "apps", sv, lone("proxy", services.StateRunning))
	want := "Service notes on apps: degraded.\n" +
		"Deploy: deployed, last succeeded 2 hours ago (action 7).\n" +
		"Why the last one failed:\nContainer notes-web-1 is unhealthy.\nweb exited\n" +
		"Its alerts are off.\n\n" +
		"CONTAINER   PORT   EXPOSE      LOCAL             URL\n" +
		"web         8080   tailscale   127.0.0.1:20000   https://apps.tail1234.ts.net:8443\n\n" +
		"Env: DB_URL, SECRET_KEY.\n" +
		"Files: config/app.toml.\n\n" +
		"CONTAINER   IMAGE                STATE                     HEALTH    MEM               CPU    RESTARTS   STARTED\n" +
		"web         ghcr.io/me/notes:1   running                   healthy   1.5 GiB / 2 GiB   2.5%   0          3 hours ago\n" +
		"worker      ghcr.io/me/notes:1   exited (137), by Tender   -         -                 -      4          -\n"
	if got := mustRun(t, h, a, "services", "show", "apps", "notes"); got != want {
		t.Errorf("show =\n%s\nwant\n%s", got, want)
	}
	if got := mustRun(t, h, a, "services", "show", "apps", "proxy"); !strings.Contains(got, "Tender found it running and didn't deploy it") {
		t.Errorf("unmanaged show =\n%s", got)
	}
	mustFail(t, h, a, `Service "nope" doesn't exist on this machine.`, "services", "show", "apps", "nope")
}

// A deploy under the name of a service Tender only watches creates it, which the hub allows once the
// old containers are down, instead of changing it, which the hub refuses.
func TestServicesDeployOverUnmanaged(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	mustRun(t, h, a, "machines", "add", "apps")
	actionCheck = time.Millisecond
	t.Cleanup(func() { actionCheck = time.Second })
	h.addServices(t, "apps", lone("cli-proxy-api", services.StateRemoved))
	compose := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(compose, []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.sent()
	if out := mustRun(t, h, a, "services", "deploy", "apps", "cli-proxy-api", "-f", compose); !strings.HasPrefix(out, "Deployed cli-proxy-api on apps.") {
		t.Errorf("deploy = %q", out)
	}
	if req := h.sent(); len(req) < 2 || req[1].Method != "POST" || req[1].Path != "/v1/machines/apps/services" {
		t.Errorf("deploy sent %+v", req)
	}
}

func TestServicesDeploy(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	mustRun(t, h, a, "machines", "add", "apps")
	actionCheck = time.Millisecond
	t.Cleanup(func() { actionCheck = time.Second })
	dir := t.TempDir()
	write := func(name, text string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	compose := write("compose.yaml", "services:\n  web:\n    image: nginx\n")
	config := write("config/app.toml", "x = 1\n")
	envFile := write(".env", "# the keys\nSECRET_KEY=\"s3cret\"\nexport DB_URL=postgres://db\n")
	h.sent()

	out := mustRun(t, h, a, "services", "deploy", "apps", "notes", "-f", compose, "--file", config, "--env-file", envFile,
		"--env", "MODE=prod", "--route", "web:80:tailscale", "--route", "web:80:public:notes.example.com")
	if out != "Deployed notes on apps.\nweb answers at https://apps.tail1234.ts.net\nweb answers at https://notes.example.com\n" {
		t.Errorf("first deploy = %q", out)
	}
	req := h.sent()
	if req[0].Method != "GET" || req[0].Path != "/v1/machines/apps/services/notes" || req[1].Method != "POST" || req[1].Path != "/v1/machines/apps/services" {
		t.Fatalf("first deploy sent %+v", req)
	}
	var in services.Input
	if err := json.Unmarshal([]byte(req[1].Body), &in); err != nil {
		t.Fatal(err)
	}
	if in.Name != "notes" || in.Compose != "services:\n  web:\n    image: nginx\n" || in.Files["config/app.toml"] != "x = 1\n" ||
		len(in.Env) != 3 || in.Env["SECRET_KEY"] != "s3cret" || in.Env["DB_URL"] != "postgres://db" || in.Env["MODE"] != "prod" ||
		len(in.Routes) != 2 || in.Routes[1].Host == nil || *in.Routes[1].Host != "notes.example.com" {
		t.Errorf("create body = %+v", in)
	}

	// Again with only the file: the compose file changes, and files, env and routes stay.
	h.sent()
	if out := mustRun(t, h, a, "services", "deploy", "apps", "notes", "-f", compose, "--no-wait"); !strings.HasPrefix(out, "Queued action ") {
		t.Errorf("--no-wait = %q", out)
	}
	req = h.sent()
	if req[1].Method != "PATCH" || strings.TrimSpace(req[1].Body) != `{"compose":"services:\n  web:\n    image: nginx\n"}` {
		t.Errorf("second deploy sent %+v", req)
	}
	// --env replaces the whole env: the keys it lacks are removed.
	h.sent()
	mustRun(t, h, a, "services", "deploy", "apps", "notes", "-f", compose, "--env", "MODE=dev")
	req = h.sent()
	var patch map[string]map[string]*string
	_ = json.Unmarshal([]byte(req[1].Body), &patch)
	if env := patch["env"]; len(env) != 3 || env["SECRET_KEY"] != nil || env["DB_URL"] != nil || env["MODE"] == nil || *env["MODE"] != "dev" {
		t.Errorf("env patch = %s", req[1].Body)
	}

	mustFail(t, h, a, `services deploy needs the compose file: -f compose.yaml. Run "tender services deploy --help" for usage.`, "services", "deploy", "apps", "notes")
	mustFail(t, h, a, `--route "web:80" must be CONTAINER:PORT:EXPOSE, CONTAINER:PORT:EXPOSE:HOST or CONTAINER:PORT:cloudflare:HOST:access, such as web:80:tailscale or web:80:public:app.example.com. Run "tender services deploy --help" for usage.`,
		"services", "deploy", "apps", "notes", "-f", compose, "--route", "web:80")
	outside := filepath.Join(t.TempDir(), "x.toml")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(t, h, a, "services", "deploy", "apps", "notes", "-f", compose, "--file", outside); err == nil || !strings.Contains(err.Error(), "is outside the compose file's folder") {
		t.Errorf("a file outside = %v", err)
	}

	// The daemon refuses the compose file: its reason is the error.
	h.daemon = func(x *actions.Action) {
		x.Status, x.Output = actions.StatusFailed, ptr(`service web in the compose file publishes ports; Tender publishes them for routes. Remove ports and add --route web:80:tailscale.`)
	}
	mustFail(t, h, a, `service web in the compose file publishes ports; Tender publishes them for routes. Remove ports and add --route web:80:tailscale.`,
		"services", "redeploy", "apps", "notes")
	h.daemon = completeAction
	h.sent()
	if out := mustRun(t, h, a, "services", "redeploy", "apps", "notes"); !strings.HasPrefix(out, "Deployed notes on apps again.\n") {
		t.Errorf("redeploy = %q", out)
	}
	if req := h.sent(); req[0].Method != "POST" || req[0].Path != "/v1/machines/apps/services/notes/deploy" {
		t.Errorf("redeploy sent %+v", req)
	}

	// An unmanaged service can't be deployed over while its containers run.
	h.addServices(t, "apps", lone("proxy", services.StateRunning))
	mustFail(t, h, a, `Service "proxy" already runs on this machine, and Tender didn't deploy it. Take its containers down (docker compose -p proxy down) before Tender deploys it, or give the new service another name.`,
		"services", "deploy", "apps", "proxy", "-f", compose)
}

// A cloudflare route's fifth part, access, requires Cloudflare Access; any other fifth part, or one
// on another expose, is refused.
func TestParseRouteAccess(t *testing.T) {
	fs := newFlagSet("services deploy", "", "")
	r, err := parseRoute(fs, "web:80:cloudflare:app.example.com:access")
	if err != nil || r.Container != "web" || r.Port != 80 || r.Expose != "cloudflare" || r.Host == nil || *r.Host != "app.example.com" || !r.Access {
		t.Errorf("an access route = %+v, %v", r, err)
	}
	if r, err := parseRoute(fs, "web:80:cloudflare:app.example.com"); err != nil || r.Access {
		t.Errorf("a route without access = %+v, %v", r, err)
	}
	for v, want := range map[string]string{
		"web:80:cloudflare:app.example.com:acess": `--route "web:80:cloudflare:app.example.com:acess": a cloudflare route's fifth part can only be access, ` +
			`which requires Cloudflare Access at its host, got "acess". Run "tender services deploy --help" for usage.`,
		"web:80:public:app.example.com:access": `--route "web:80:public:app.example.com:access": only a cloudflare route has a fifth part, access, ` +
			`such as web:80:cloudflare:app.example.com:access. Run "tender services deploy --help" for usage.`,
		"web:80:tailscale:443:access": `--route "web:80:tailscale:443:access": only a cloudflare route has a fifth part, access, ` +
			`such as web:80:cloudflare:app.example.com:access. Run "tender services deploy --help" for usage.`,
		"web:80:cloudflare:app.example.com:access:x": `--route "web:80:cloudflare:app.example.com:access:x" must be CONTAINER:PORT:EXPOSE, ` +
			`CONTAINER:PORT:EXPOSE:HOST or CONTAINER:PORT:cloudflare:HOST:access, such as web:80:tailscale or web:80:public:app.example.com. ` +
			`Run "tender services deploy --help" for usage.`,
	} {
		if _, err := parseRoute(fs, v); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", v, err, want)
		}
	}
}

func TestServicesRemove(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	mustRun(t, h, a, "machines", "add", "apps")
	actionCheck = time.Millisecond
	t.Cleanup(func() { actionCheck = time.Second })
	h.addServices(t, "apps", notes(h.now))
	h.sent()
	mustFail(t, h, a, `this removes notes from apps, keeping its data on the machine. Run "tender services remove apps notes --yes" to confirm`,
		"services", "remove", "apps", "notes")
	mustFail(t, h, a, `this removes notes from apps, and deletes its data, with no undo. Run "tender services remove apps notes --delete-data --yes" to confirm`,
		"services", "remove", "apps", "notes", "--delete-data")
	out := mustRun(t, h, a, "services", "remove", "apps", "notes", "--yes")
	if out != "Removed notes from apps.\nKept the service's folder, data and all, in /var/lib/tender/services/.removed/notes-1794657600. "+
		"Delete it when you no longer need it: sudo rm -rf /var/lib/tender/services/.removed/notes-1794657600\n" {
		t.Errorf("remove = %q", out)
	}
	if req := h.sent(); req[0].Method != "DELETE" || req[0].Path != "/v1/machines/apps/services/notes" || req[0].Query != "" {
		t.Errorf("remove sent %+v", req[0])
	}
	mustRun(t, h, a, "services", "remove", "apps", "notes", "--delete-data", "--yes")
	if req := h.sent(); req[0].Query != "delete_data=true" {
		t.Errorf("remove --delete-data sent %+v", req[0])
	}
}

func TestServicesActions(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	mustRun(t, h, a, "machines", "add", "apps")
	h.addServices(t, "apps", lone("web-1", services.StateRunning), notes(h.now))
	actionCheck = time.Millisecond
	t.Cleanup(func() { actionCheck = time.Second })

	for typ, want := range map[string]string{"start": "Started web-1 on apps.\n", "stop": "Stopped web-1 on apps.\n", "restart": "Restarted web-1 on apps.\n"} {
		h.sent()
		if out := mustRun(t, h, a, "services", typ, "apps", "web-1"); out != want {
			t.Errorf("%s = %q, want %q", typ, out, want)
		}
		req := h.sent()
		if len(req) < 2 || req[0].Method != "POST" || req[0].Path != "/v1/machines/apps/services/web-1/"+typ ||
			req[0].Body != "" || req[1].Method != "GET" || !strings.HasPrefix(req[1].Path, "/v1/machines/apps/actions/") {
			t.Errorf("%s sent %+v", typ, req)
		}
	}
	h.sent()
	if out := mustRun(t, h, a, "services", "restart", "apps", "notes", "--container", "worker"); out != "Restarted worker in notes on apps.\n" {
		t.Errorf("restart of one container = %q", out)
	}
	if req := h.sent(); strings.TrimSpace(req[0].Body) != `{"container":"worker"}` {
		t.Errorf("restart of one container sent %s", req[0].Body)
	}
	mustFail(t, h, a, `Container "nope" isn't in service notes.`, "services", "stop", "apps", "notes", "--container", "nope")
	h.sent()

	out := mustRun(t, h, a, "services", "logs", "apps", "web-1", "--lines", "50", "--since", "2h")
	if out != "2026-11-14T11:59:00.000000000Z started\n2026-11-14T11:59:30.000000000Z listening on :8080\n" {
		t.Errorf("logs = %q", out)
	}
	if req := h.sent(); strings.TrimSpace(req[0].Body) != `{"type":"logs","service":"web-1","since":"2h","lines":50}` {
		t.Errorf("logs sent %s", req[0].Body)
	}
	mustRun(t, h, a, "services", "logs", "apps", "notes", "--container", "web")
	if req := h.sent(); strings.TrimSpace(req[0].Body) != `{"type":"logs","service":"notes","container":"web","lines":200}` {
		t.Errorf("logs of one container sent %s", req[0].Body)
	}
	mustFail(t, h, a, `--lines must be from 1 to 2000. Run "tender services logs --help" for usage.`, "services", "logs", "apps", "web-1", "--lines", "0")
	mustFail(t, h, a, `--since must be a duration such as 30m, 1h or 7d, at most 7d. Run "tender services logs --help" for usage.`,
		"services", "logs", "apps", "web-1", "--since", "8d")
	mustFail(t, h, a, `services restart needs a machine name and a service name. Run "tender services restart --help" for usage.`, "services", "restart", "apps")
	mustFail(t, h, a, `Service "nope" doesn't exist on this machine.`, "services", "restart", "apps", "nope")

	// The daemon fails: its reason is the error.
	h.daemon = func(x *actions.Action) {
		x.Status, x.Output = actions.StatusFailed, ptr("Could not restart web-1: Docker answered 404: No such container: web-1.")
	}
	mustFail(t, h, a, "Could not restart web-1: Docker answered 404: No such container: web-1.", "services", "restart", "apps", "web-1")

	// The daemon never answers: the command says the action is queued and how to check it.
	h.daemon = nil
	actionWait = 20 * time.Millisecond
	t.Cleanup(func() { actionWait = actions.Wait })
	_, err := runCommand(t, h, a, "services", "restart", "apps", "web-1")
	if err == nil || !strings.Contains(err.Error(), "hasn't finished within 0 seconds. Action ") ||
		!strings.Contains(err.Error(), "is still pending: the daemon hasn't picked it up. It runs if the daemon picks it up within 15 minutes of being asked, and fails otherwise.") ||
		!strings.Contains(err.Error(), "Check it with: tender actions show apps ") {
		t.Errorf("timeout error = %v", err)
	}
	// The daemon started it but hasn't said how it ended: the error says so, not that it is queued.
	h.daemon = func(x *actions.Action) { x.Status = actions.StatusInProgress }
	_, err = runCommand(t, h, a, "services", "restart", "apps", "web-1")
	if err == nil || strings.Contains(err.Error(), "pending") ||
		!strings.Contains(err.Error(), "is in progress: the daemon started it. The hub fails it if the daemon doesn't say how it ended within 15 minutes.") {
		t.Errorf("timeout error in progress = %v", err)
	}

	// Empty logs say so.
	h.daemon = func(x *actions.Action) { x.Status, x.Output = actions.StatusCompleted, ptr("") }
	if out := mustRun(t, h, a, "services", "logs", "apps", "web-1"); out != "web-1 on apps has no logs.\n" {
		t.Errorf("empty logs = %q", out)
	}
}

func TestLogs(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	mustRun(t, h, a, "machines", "add", "apps")
	actionCheck = time.Millisecond
	t.Cleanup(func() { actionCheck = time.Second })

	out := mustRun(t, h, a, "logs", "apps", "--unit", "tenderd", "--priority", "warning", "--since", "1h", "--lines", "20")
	if out != "2026-11-14T11:59:00.000000000Z tenderd.service daemon started\n" {
		t.Errorf("logs = %q", out)
	}
	if req := h.sent(); req[len(req)-2].Path != "/v1/machines/apps/actions" ||
		strings.TrimSpace(req[len(req)-2].Body) != `{"type":"logs","unit":"tenderd","priority":"warning","since":"1h","lines":20}` {
		t.Errorf("sent %+v", req)
	}
	mustFail(t, h, a, `logs needs one machine name. Run "tender logs --help" for usage.`, "logs")
	mustFail(t, h, a, `--since must be a duration such as 30m, 1h or 7d, at most 7d. Run "tender logs --help" for usage.`, "logs", "apps", "--since", "forever")
	for _, n := range []string{"0", "-5", "2001"} {
		mustFail(t, h, a, `--lines must be from 1 to 2000. Run "tender logs --help" for usage.`, "logs", "apps", "--lines", n)
		mustFail(t, h, a, `--lines must be from 1 to 2000. Run "tender services logs --help" for usage.`, "services", "logs", "apps", "web-1", "--lines", n)
	}

	// tender actions list and show.
	want := "ID   TYPE   OF                                          STATUS      ASKED\n" +
		"2    logs   journal, unit tenderd, priority warning, since 1h   completed   just now\n"
	if got := mustRun(t, h, a, "actions", "list", "apps"); !strings.HasPrefix(got, "ID   TYPE   OF") || !strings.Contains(got, "journal, unit tenderd, priority warning, since 1h   completed") {
		t.Errorf("actions list =\n%s\nwant like\n%s", got, want)
	}
	got := mustRun(t, h, a, "actions", "show", "apps", "2")
	if !strings.HasPrefix(got, "Action 2 on apps: logs of journal, unit tenderd, priority warning, since 1h, completed, asked ") ||
		!strings.HasSuffix(got, "\n2026-11-14T11:59:00.000000000Z tenderd.service daemon started\n") {
		t.Errorf("actions show =\n%s", got)
	}
	h.actions[2].Output, h.actions[2].OutputCleared = nil, true
	h.actions[2].UpdatedAt = h.now.Add(-2 * time.Hour)
	if got := mustRun(t, h, a, "actions", "show", "apps", "2"); !strings.HasSuffix(got,
		"Its logs were cleared 2 hours ago: the hub keeps logs only for a while after they are read, because logs can hold secrets.\n") {
		t.Errorf("cleared = %q", got)
	}
	mustFail(t, h, a, `Action 9 doesn't exist on this machine.`, "actions", "show", "apps", "9")
	mustFail(t, h, a, `the action id must be a number, got "x". Run "tender actions show --help" for usage.`, "actions", "show", "apps", "x")
	mustRun(t, h, a, "machines", "add", "code")
	if got := mustRun(t, h, a, "actions", "list", "code"); got != "Nothing was asked of code yet.\n" {
		t.Errorf("empty = %q", got)
	}
}

func TestServicesAlerts(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	mustRun(t, h, a, "machines", "add", "apps")
	h.addServices(t, "apps", lone("web-1", services.StateRunning))
	if out := mustRun(t, h, a, "services", "alerts", "apps", "web-1", "off"); out != "Alerts for web-1 on apps are off. Its open problems resolve within a minute.\n" {
		t.Errorf("off = %q", out)
	}
	if h.services["apps"][0].AlertsEnabled {
		t.Error("alerts are still on")
	}
	if req := h.sent(); req[len(req)-1].Method != "PATCH" || strings.TrimSpace(req[len(req)-1].Body) != `{"alerts_enabled":false}` {
		t.Errorf("sent %+v", req[len(req)-1])
	}
	if out := mustRun(t, h, a, "services", "alerts", "apps", "web-1", "on"); out != "Alerts for web-1 on apps are on.\n" {
		t.Errorf("on = %q", out)
	}
	mustFail(t, h, a, `services alerts needs a machine, a service and on or off. Run "tender services alerts --help" for usage.`, "services", "alerts", "apps", "web-1", "maybe")
}
