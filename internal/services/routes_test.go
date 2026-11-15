package services

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

// identity knows machine a by token "token-a" and machine b by "token-b".
type identity struct{ d *db.DB }

func (i identity) Authenticate(_ context.Context, token string) (int64, string, error) {
	switch token {
	case "token-a":
		return 1, "a", nil
	case "token-b":
		return 2, "b", nil
	}
	return 0, "", server.NotFound("no such token")
}

func (i identity) Lookup(ctx context.Context, name string) (int64, error) {
	var id int64
	if err := i.d.Read.QueryRowContext(ctx, `SELECT id FROM machines WHERE name = ?`, name).Scan(&id); err != nil {
		return 0, server.NotFound("Machine %q doesn't exist.", name)
	}
	return id, nil
}

type hub struct {
	*httptest.Server
	srv  *server.Server
	db   *db.DB
	logs *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newTestHub(t *testing.T) *hub {
	t.Helper()
	d := newDB(t)
	logs := &syncBuffer{}
	srv := server.New(d, slog.New(slog.NewTextHandler(logs, nil)), identity{d})
	NewStore(d).Routes(srv)
	h := &hub{Server: httptest.NewServer(srv), srv: srv, db: d, logs: logs}
	t.Cleanup(h.Close)
	return h
}

// call makes one request, as a person unless token is set, and returns the status and body.
func (h *hub) call(t *testing.T, method, path, token string, body any) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, h.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestRoutes(t *testing.T) {
	h := newTestHub(t)
	report := Report{Docker: DockerRunning, Services: []ServiceInput{running("web", "c1"), running("worker", "c2")}}

	for _, c := range []struct {
		method, path, token string
		body                any
		status              int
		has                 string
	}{
		{"GET", "/v1/machines/a/services", "", nil, 200, `{"machine":"a","docker":null,"data":[]}`},
		{"GET", "/v1/services", "", nil, 200, `{"data":[]}`},
		{"PUT", "/v1/machines/a/services", "", report, 401, "Bearer"},
		{"PUT", "/v1/machines/a/services", "token-b", report, 403, "another machine"},
		{"PUT", "/v1/machines/a/services", "token-a", []ServiceInput{}, 422, "must be services.Report"},
		{"PUT", "/v1/machines/a/services", "token-a", map[string]any{"docker": "sleeping"}, 422, "docker must be running or unreachable."},
		{"PUT", "/v1/machines/a/services", "token-a", map[string]any{"docker": "running"}, 422, "services is required when docker is running"},
		{"PUT", "/v1/machines/a/services", "token-a", map[string]any{"docker": "unreachable", "services": []any{}}, 422, "Leave services out"},
		{"PUT", "/v1/machines/a/services", "token-a", map[string]any{"docker": "running", "services": []map[string]any{
			{"name": "x", "container_id": "c", "image": "i", "state": "running", "restart_policy": "sometimes"}}}, 422, "restart_policy"},
		{"PUT", "/v1/machines/a/services", "token-a", report, 204, ""},
		{"PUT", "/v1/machines/b/services", "token-b", Report{Docker: DockerRunning, Services: []ServiceInput{running("db", "c3")}}, 204, ""},
		{"GET", "/v1/services", "", nil, 200, `"machine":"b","docker":"running","data":[{"id":3`},
		{"GET", "/v1/machines/a/services", "", nil, 200, `{"machine":"a","docker":"running","data":[{"id":1`},
		{"GET", "/v1/machines/nope/services", "", nil, 404, `Machine \"nope\" doesn't exist.`},
		{"GET", "/v1/machines/a/services/web", "", nil, 200, `"restart_policy":"no"`},
		{"GET", "/v1/machines/a/services/db", "", nil, 404, `Service \"db\" doesn't exist on this machine.`},
		{"PATCH", "/v1/machines/a/services/web", "", map[string]any{"alerts_enabled": false}, 200, `"alerts_enabled":false`},
		{"PATCH", "/v1/machines/a/services/web", "", map[string]any{"state": "running"}, 400, "unknown field"},
		{"PATCH", "/v1/machines/a/services/web", "", map[string]any{}, 200, `"alerts_enabled":false`},
		// Docker stops answering: the list stays, with every state unknown.
		{"PUT", "/v1/machines/a/services", "token-a", Report{Docker: DockerUnreachable}, 204, ""},
		{"GET", "/v1/machines/a/services", "", nil, 200, `"docker":"unreachable"`},
		{"GET", "/v1/machines/a/services/worker", "", nil, 200, `"state":"unknown"`},
		{"PUT", "/v1/machines/a/services", "token-a", report, 204, ""},
		{"GET", "/v1/machines/a/services/worker", "", nil, 200, `"state":"running"`},
	} {
		status, body := h.call(t, c.method, c.path, c.token, c.body)
		if status != c.status || !strings.Contains(body, c.has) {
			t.Errorf("%s %s: %d %s, want %d with %q", c.method, c.path, status, body, c.status, c.has)
		}
		if status >= 400 && !strings.Contains(body, `"detail"`) {
			t.Errorf("%s %s: no problem details: %s", c.method, c.path, body)
		}
	}
	if status, body := h.call(t, "GET", "/v1/machines/a/services/web", "", nil); status != 200 || strings.Contains(body, "_reports") {
		t.Errorf("internal counts are shown: %d %s", status, body)
	}
}

// A service may be called "actions": every service route still reaches it.
func TestServiceCalledActions(t *testing.T) {
	h := newTestHub(t)
	if status, body := h.call(t, "PUT", "/v1/machines/a/services", "token-a",
		Report{Docker: DockerRunning, Services: []ServiceInput{running("actions", "c1")}}); status != 204 {
		t.Fatalf("report: %d %s", status, body)
	}
	for _, c := range []struct {
		method, path string
		body         any
		status       int
		has          string
	}{
		{"GET", "/v1/machines/a/services/actions", nil, 200, `"name":"actions"`},
		{"PATCH", "/v1/machines/a/services/actions", map[string]any{"alerts_enabled": false}, 200, `"alerts_enabled":false`},
	} {
		if status, body := h.call(t, c.method, c.path, "", c.body); status != c.status || !strings.Contains(body, c.has) {
			t.Errorf("%s %s: %d %s, want %d with %q", c.method, c.path, status, body, c.status, c.has)
		}
	}
}

// TestDaemon runs the daemon against the real routes and a fake Docker: it reports the containers
// with their usage, does what the actions daemon hands it, and its next report shows the change.
func TestDaemon(t *testing.T) {
	h := newTestHub(t)
	f := newFakeDocker(t,
		&fakeContainer{ID: "aaa", Name: "web", Image: "nginx", State: "running", Logs: []string{"a log line"},
			MemLimit: 1 << 30, Memory: 300 << 20, Cache: 100 << 20, CPU: 1e9, System: 1e11, CPUs: 2},
		&fakeContainer{ID: "bbb", Name: "worker", Image: "w", State: "exited", ExitCode: 3},
		&fakeContainer{ID: "ccc", Name: "cron", Image: "c", State: "running", StatsFail: true},
	)
	var logs syncBuffer
	d := NewDaemon(client.New(h.URL, "token-a"), "a", f.socket, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := context.Background()
	s := NewStore(h.db)
	get := func(name string) Service {
		t.Helper()
		sv, err := s.Get(ctx, 1, name)
		if err != nil {
			t.Fatal(err)
		}
		return sv
	}

	d.reportNow(ctx)
	web, worker, cron := get("web"), get("worker"), get("cron")
	if web.MemoryBytes == nil || *web.MemoryBytes != 200<<20 || web.MemoryLimitBytes == nil || *web.MemoryLimitBytes != 1<<30 || web.CPUPercent != nil {
		t.Errorf("web's first usage = %+v; want memory and limit, no CPU yet", web)
	}
	if worker.MemoryBytes != nil || worker.MemoryLimitBytes != nil || worker.CPUPercent != nil {
		t.Errorf("a stopped container has usage: %+v", worker)
	}
	if cron.MemoryBytes != nil || cron.State != StateRunning {
		t.Errorf("a failed stats read: %+v; want the service with null usage", cron)
	}
	f.mu.Lock()
	c := f.byName["web"]
	c.CPU, c.System = c.CPU+5e8, c.System+4e9
	f.mu.Unlock()
	d.reportNow(ctx)
	if web = get("web"); web.CPUPercent == nil || *web.CPUPercent != 25 {
		t.Errorf("web's second usage = %+v; want 25%% CPU", web)
	}
	// A restart keeps the container id but starts its CPU counter again, so the reading before it
	// can't measure the one after.
	f.mu.Lock()
	c.StartedAt, c.CPU, c.System = "2026-11-15T10:00:00Z", c.CPU+1e8, c.System+4e9
	f.mu.Unlock()
	d.reportNow(ctx)
	if web = get("web"); web.CPUPercent != nil {
		t.Errorf("web's CPU right after a restart = %v; want null", *web.CPUPercent)
	}
	f.mu.Lock()
	c.CPU, c.System = c.CPU+1e9, c.System+4e9
	f.mu.Unlock()
	d.reportNow(ctx)
	if web = get("web"); web.CPUPercent == nil || *web.CPUPercent != 50 {
		t.Errorf("web's CPU on the next report = %+v; want 50%%", web.CPUPercent)
	}

	if _, err := d.Service(ctx, actStop, "web", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Finished(ctx, 1, "web", actStop); err != nil {
		t.Fatal(err)
	}
	d.reportNow(ctx)
	if web = get("web"); web.State != StateExited || !web.StoppedByTender || web.MemoryBytes != nil {
		t.Errorf("after the stop: %+v", web)
	}
	if out, err := d.Service(ctx, actLogs, "web", 10, time.Hour); err != nil || !strings.Contains(out, "a log line") {
		t.Errorf("logs = %q, %v", out, err)
	}
	if _, err := d.Service(ctx, actRestart, "gone", 0, 0); err == nil || err.Error() != "Docker answered 404: No such container: gone" {
		t.Errorf("restart of a gone container: %v", err)
	}
	if _, err := d.Service(ctx, "explode", "web", 0, 0); err == nil {
		t.Error("an unknown action worked")
	}
	if strings.Contains(logs.String()+h.logs.String(), "a log line") {
		t.Errorf("the logs reached a log:\n%s%s", logs.String(), h.logs.String())
	}
	d.Changed()
	d.Changed() // a second ask while one waits is the same report
	if len(d.report) != 1 {
		t.Errorf("%d reports asked for", len(d.report))
	}
}

// A report whose list was read before a stop must not reach the hub after the stop's result, or
// it would show the service running and clear stopped_by_tender: reports and actions take turns.
func TestDaemonReportsAndActionsTakeTurns(t *testing.T) {
	h := newTestHub(t)
	f := newFakeDocker(t, &fakeContainer{ID: "aaa", Name: "web", Image: "nginx", State: "running", StartedAt: "2026-11-15T09:00:00Z"})
	s := NewStore(h.db)
	ctx := context.Background()
	if _, err := s.Replace(ctx, 1, []ServiceInput{running("web", "aaa")}); err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(client.New(h.URL, "token-a"), "a", f.socket, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// The report's inspect of web answers "running", then waits until released.
	inspected, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.setHold(func(r *http.Request) {
		if r.URL.Path == "/containers/aaa/json" {
			once.Do(func() { close(inspected); <-release })
		}
	})
	reported := make(chan struct{})
	go func() { d.reportNow(ctx); close(reported) }()
	<-inspected

	// What the actions daemon does with a stop: take the turn, stop, tell the hub, give the turn back.
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		d.Lock()
		defer d.Unlock()
		if _, err := d.Service(ctx, actStop, "web", 0, 0); err != nil {
			t.Error(err)
		}
		if err := s.Finished(ctx, 1, "web", actStop); err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(300 * time.Millisecond)
	if slices.Contains(f.called(), "POST /containers/web/stop") {
		t.Error("the stop ran while a report was being made")
	}
	close(release)
	<-reported
	<-ran
	if sv, err := s.Get(ctx, 1, "web"); err != nil || !sv.StoppedByTender {
		t.Errorf("after the stop: %+v, %v; want stopped_by_tender", sv, err)
	}
}

// Once this daemon has seen Docker, losing its socket, or the right to use it, is an outage the
// hub hears about; a machine that never had Docker stays silent.
func TestDaemonLosesDocker(t *testing.T) {
	for _, c := range []struct {
		name string
		lose func(f *fakeDocker) error
	}{
		{"socket gone", func(f *fakeDocker) error { return os.Remove(f.socket) }},
		{"permission denied", func(f *fakeDocker) error { return chmod0(f.socket) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newTestHub(t)
			f := newFakeDocker(t, &fakeContainer{ID: "aaa", Name: "web", State: "running"})
			d := NewDaemon(client.New(h.URL, "token-a"), "a", f.socket, slog.New(slog.NewTextHandler(io.Discard, nil)))
			ctx := context.Background()
			d.reportNow(ctx)
			if err := c.lose(f); err != nil {
				t.Skip(err)
			}
			d.Docker.http.CloseIdleConnections() // a kept connection would still work; the next one doesn't
			d.reportNow(ctx)
			ms, err := NewStore(h.db).Machines(ctx, 1)
			if err != nil || ms[0].Docker == nil || *ms[0].Docker != DockerUnreachable || ms[0].Data[0].State != StateUnknown {
				t.Errorf("after losing Docker: %+v, %v", ms, err)
			}
		})
	}
}

// A slow list is logged, and the hub keeps its list: Docker answered its ping, so it isn't down.
func TestDaemonSlowList(t *testing.T) {
	h := newTestHub(t)
	f := newFakeDocker(t, &fakeContainer{ID: "aaa", Name: "web", State: "running"})
	var logs syncBuffer
	d := NewDaemon(client.New(h.URL, "token-a"), "a", f.socket, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := context.Background()
	d.reportNow(ctx)
	defer func(was time.Duration) { listTimeout = was }(listTimeout)
	listTimeout = 100 * time.Millisecond
	f.setHold(func(r *http.Request) {
		if r.URL.Path == "/containers/json" {
			time.Sleep(300 * time.Millisecond)
		}
	})
	d.reportNow(ctx)
	ms, err := NewStore(h.db).Machines(ctx, 1)
	if err != nil || *ms[0].Docker != DockerRunning || ms[0].Data[0].State != StateRunning {
		t.Errorf("after a slow list: %+v, %v", ms, err)
	}
	if !strings.Contains(logs.String(), `msg="could not list the Docker containers"`) {
		t.Errorf("log:\n%s", logs.String())
	}
}

func TestDaemonWithoutDocker(t *testing.T) {
	h := newTestHub(t)
	var logs syncBuffer
	d := NewDaemon(client.New(h.URL, "token-a"), "a", "/nonexistent/docker.sock", slog.New(slog.NewTextHandler(&logs, nil)))
	d.Every = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	d.Run(ctx)
	if n := strings.Count(logs.String(), `msg="this machine has no Docker"`); n != 1 || !strings.Contains(logs.String(), "level=INFO") {
		t.Errorf("logged %d times:\n%s", n, logs.String())
	}
	if strings.Contains(h.logs.String(), "path=") {
		t.Errorf("a machine without Docker called the hub:\n%s", h.logs.String())
	}
}

func TestDaemonPermissionDenied(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	f := newFakeDocker(t)
	if err := chmod0(f.socket); err != nil {
		t.Skip(err)
	}
	h := newTestHub(t)
	var logs syncBuffer
	d := NewDaemon(client.New(h.URL, "token-a"), "a", f.socket, slog.New(slog.NewTextHandler(&logs, nil)))
	d.Every = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	d.Run(ctx)
	if n := strings.Count(logs.String(), `msg="Tender may not use Docker`); n != 1 || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("logged %d times:\n%s", n, logs.String())
	}
}

// With Docker's socket there but refusing connections, the daemon tells the hub, once a report,
// and doesn't poll for actions; when Docker answers again, the next report says so.
func TestDaemonDockerUnreachable(t *testing.T) {
	h := newTestHub(t)
	dir, err := os.MkdirTemp("", "docker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := NewStore(h.db).Replace(context.Background(), 1, []ServiceInput{running("web", "c1")}); err != nil {
		t.Fatal(err)
	}

	var logs syncBuffer
	d := NewDaemon(client.New(h.URL, "token-a"), "a", socket, slog.New(slog.NewTextHandler(&logs, nil)))
	d.Every = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	d.Run(ctx)
	ms, err := NewStore(h.db).Machines(context.Background(), 1)
	if err != nil || *ms[0].Docker != DockerUnreachable || ms[0].UnreachableReports() < 2 || ms[0].Data[0].State != StateUnknown {
		t.Errorf("after the outage: %+v, %v", ms, err)
	}
	if n := strings.Count(logs.String(), `msg="Docker is not answering`); n != 1 {
		t.Errorf("logged %d times:\n%s", n, logs.String())
	}

	// Docker is back.
	_ = os.Remove(socket)
	f := &fakeDocker{socket: socket, byName: map[string]*fakeContainer{}}
	f.add(&fakeContainer{ID: "c1", Name: "web", State: "running"})
	ln, err = net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	d = NewDaemon(client.New(h.URL, "token-a"), "a", socket, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	d.Run(ctx2)
	ms, _ = NewStore(h.db).Machines(context.Background(), 1)
	if *ms[0].Docker != DockerRunning || ms[0].UnreachableReports() != 0 || ms[0].Data[0].State != StateRunning {
		t.Errorf("after Docker is back: %+v", ms)
	}
}
