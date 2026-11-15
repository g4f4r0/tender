package main

import (
	"net/http"
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
	a := &actions.Action{Summary: actions.Summary{ID: h.id(), Machine: m.Name, Type: in.Type, Service: in.Service, Unit: in.Unit,
		Priority: in.Priority, Since: in.Since, Lines: in.Lines, Status: actions.StatusPending, CreatedAt: h.now, UpdatedAt: h.now}}
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
		services.Service{Name: "cli-proxy-api", State: "running", Health: ptr("healthy"), StartedAt: &started, AlertsEnabled: true,
			MemoryBytes: ptr(int64(200 << 20)), MemoryLimitBytes: ptr(int64(1 << 30)), CPUPercent: ptr(12.5)},
		services.Service{Name: "web", State: "running", MemoryBytes: ptr(int64(1536 << 20)), AlertsEnabled: true},
		services.Service{Name: "worker", State: "exited", ExitCode: ptr(int64(137)), RestartCount: 4, StoppedByTender: true},
	)
	want := "MACHINE   SERVICE         STATE                     HEALTH    MEM               CPU     RESTARTS   STARTED       ALERTS\n" +
		"apps      cli-proxy-api   running                   healthy   200 MiB / 1 GiB   12.5%   0          3 hours ago   on\n" +
		"apps      web             running                   -         1.5 GiB           -       0          -             on\n" +
		"apps      worker          exited (137), by Tender   -         -                 -       4          -             off\n"
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
		if len(req) < 2 || req[0].Method != "POST" || req[0].Path != "/v1/machines/apps/actions" ||
			strings.TrimSpace(req[0].Body) != `{"type":"`+typ+`","service":"web-1"}` || req[1].Method != "GET" || !strings.HasPrefix(req[1].Path, "/v1/machines/apps/actions/") {
			t.Errorf("%s sent %+v", typ, req)
		}
	}

	out := mustRun(t, h, a, "services", "logs", "apps", "web-1", "--lines", "50", "--since", "2h")
	if out != "2026-11-14T11:59:00.000000000Z started\n2026-11-14T11:59:30.000000000Z listening on :8080\n" {
		t.Errorf("logs = %q", out)
	}
	if req := h.sent(); strings.TrimSpace(req[0].Body) != `{"type":"logs","service":"web-1","since":"2h","lines":50}` {
		t.Errorf("logs sent %s", req[0].Body)
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
		!strings.Contains(err.Error(), "stays queued for 15 minutes") || !strings.Contains(err.Error(), "Check it with: tender actions show apps ") {
		t.Errorf("timeout error = %v", err)
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
	if got := mustRun(t, h, a, "actions", "show", "apps", "2"); !strings.HasSuffix(got, "Its logs were cleared 1 hour after it finished, because logs can hold secrets.\n") {
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
