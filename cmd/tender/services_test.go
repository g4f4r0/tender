package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services"
)

// completeAction is the fake daemon: it does every action at once.
func completeAction(a *services.Action) {
	a.Status = services.StatusCompleted
	if a.Type == services.TypeLogs {
		out := "2026-11-14T11:59:00Z started\n2026-11-14T11:59:30Z listening on :8080\n"
		a.Output = &out
	}
}

func (h *fakeHub) machineServices(name string) services.MachineServices {
	ms := services.MachineServices{Machine: name, Data: append([]services.Service{}, h.services[name]...)}
	if d, ok := h.docker[name]; ok {
		ms.Docker = &d
	}
	return ms
}

func (h *fakeHub) listServices(w http.ResponseWriter, r *http.Request) {
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

func (h *fakeHub) patchService(w http.ResponseWriter, r *http.Request) {
	sv := h.service(w, r)
	if sv == nil {
		return
	}
	var p services.ServicePatch
	if !decode(w, r, &p) {
		return
	}
	if p.AlertsEnabled != nil {
		sv.AlertsEnabled = *p.AlertsEnabled
	}
	server.WriteJSON(w, http.StatusOK, sv)
}

func (h *fakeHub) createAction(w http.ResponseWriter, r *http.Request) {
	sv := h.service(w, r)
	if sv == nil {
		return
	}
	var in services.ActionInput
	if !decode(w, r, &in) {
		return
	}
	if in.Type == services.TypeLogs && in.Lines == nil {
		n := services.DefaultLines
		in.Lines = &n
	}
	a := &services.Action{ID: h.id(), Machine: sv.Machine, Service: sv.Name, Type: in.Type, Status: services.StatusPending,
		Lines: in.Lines, CreatedAt: h.now, UpdatedAt: h.now}
	h.actions[a.ID] = a
	server.WriteJSON(w, http.StatusAccepted, a)
}

func (h *fakeHub) getAction(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a := h.actions[id]
	if a == nil {
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
	}
	h.services[machine] = append(h.services[machine], list...)
	h.docker[machine] = services.DockerRunning
}

func ptr[T any](v T) *T { return &v }

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
	started := h.now.Add(-3 * time.Hour)
	h.addServices(t, "apps",
		services.Service{Name: "cli-proxy-api", State: "running", Health: ptr("healthy"), StartedAt: &started, AlertsEnabled: true},
		services.Service{Name: "worker", State: "exited", ExitCode: ptr(int64(137)), RestartCount: 4, StoppedByTender: true},
	)
	want := "MACHINE   SERVICE         STATE                     HEALTH    RESTARTS   STARTED       ALERTS\n" +
		"apps      cli-proxy-api   running                   healthy   0          3 hours ago   on\n" +
		"apps      worker          exited (137), by Tender   -         4          -             off\n"
	if got := mustRun(t, h, a, "services", "list"); got != want {
		t.Errorf("list =\n%s\nwant\n%s", got, want)
	}
	if got := mustRun(t, h, a, "services", "list", "apps"); !strings.Contains(got, "worker") {
		t.Errorf("one machine's list =\n%s", got)
	}

	// Docker stops answering: the states are unknown, and the list says why.
	h.docker["apps"] = services.DockerUnreachable
	for i := range h.services["apps"] {
		h.services["apps"][i].State = services.StateUnknown
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

func TestServicesActions(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	mustRun(t, h, a, "machines", "add", "apps")
	h.addServices(t, "apps", services.Service{Name: "web-1", State: "running", AlertsEnabled: true})
	actionCheck = time.Millisecond
	t.Cleanup(func() { actionCheck = time.Second })

	for typ, want := range map[string]string{"start": "Started web-1 on apps.\n", "stop": "Stopped web-1 on apps.\n", "restart": "Restarted web-1 on apps.\n"} {
		h.sent()
		if out := mustRun(t, h, a, "services", typ, "apps", "web-1"); out != want {
			t.Errorf("%s = %q, want %q", typ, out, want)
		}
		req := h.sent()
		if len(req) < 2 || req[0].Method != "POST" || req[0].Path != "/v1/machines/apps/services/web-1/actions" ||
			strings.TrimSpace(req[0].Body) != `{"type":"`+typ+`"}` || req[1].Method != "GET" || !strings.HasPrefix(req[1].Path, "/v1/machines/apps/service-actions/") {
			t.Errorf("%s sent %+v", typ, req)
		}
	}

	out := mustRun(t, h, a, "services", "logs", "apps", "web-1", "--lines", "50")
	if out != "2026-11-14T11:59:00Z started\n2026-11-14T11:59:30Z listening on :8080\n" {
		t.Errorf("logs = %q", out)
	}
	if req := h.sent(); strings.TrimSpace(req[0].Body) != `{"type":"logs","lines":50}` {
		t.Errorf("logs sent %s", req[0].Body)
	}
	mustFail(t, h, a, `--lines must be from 1 to 2000. Run "tender services logs --help" for usage.`, "services", "logs", "apps", "web-1", "--lines", "0")
	mustFail(t, h, a, `services restart needs a machine name and a service name. Run "tender services restart --help" for usage.`, "services", "restart", "apps")
	mustFail(t, h, a, `Service "nope" doesn't exist on this machine.`, "services", "restart", "apps", "nope")

	// The daemon fails: its reason is the error.
	h.daemon = func(x *services.Action) {
		x.Status, x.Output = services.StatusFailed, ptr("Could not restart web-1: Docker answered 404: No such container: web-1.")
	}
	mustFail(t, h, a, "Could not restart web-1: Docker answered 404: No such container: web-1.", "services", "restart", "apps", "web-1")

	// The daemon never answers: the command says the action is queued and how to check it.
	h.daemon = nil
	actionWait = 20 * time.Millisecond
	t.Cleanup(func() { actionWait = services.ActionWait })
	_, err := runCommand(t, h, a, "services", "restart", "apps", "web-1")
	if err == nil || !strings.Contains(err.Error(), "hasn't finished within 0 seconds. The restart of web-1 stays queued for 15 minutes") ||
		!strings.HasSuffix(err.Error(), "Check with: tender services list apps") {
		t.Errorf("timeout error = %v", err)
	}
	_, err = runCommand(t, h, a, "services", "logs", "apps", "web-1")
	if err == nil || !strings.Contains(err.Error(), "Its logs will be at GET /v1/machines/apps/service-actions/") {
		t.Errorf("logs timeout error = %v", err)
	}

	// Empty logs say so.
	h.daemon = func(x *services.Action) { x.Status, x.Output = services.StatusCompleted, ptr("") }
	if out := mustRun(t, h, a, "services", "logs", "apps", "web-1"); out != "web-1 on apps has no logs.\n" {
		t.Errorf("empty logs = %q", out)
	}
}

func TestServicesAlerts(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	mustRun(t, h, a, "machines", "add", "apps")
	h.addServices(t, "apps", services.Service{Name: "web-1", State: "running", AlertsEnabled: true})
	if out := mustRun(t, h, a, "services", "alerts", "apps", "web-1", "off"); out != "Alerts for web-1 on apps are off. Its open alerts resolve within a minute.\n" {
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
