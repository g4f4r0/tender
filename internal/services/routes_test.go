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
		{"POST", "/v1/machines/a/services/web/actions", "", map[string]any{"type": "explode"}, 422, "type must be start, stop, restart or logs."},
		{"POST", "/v1/machines/a/services/web/actions", "", map[string]any{"type": "logs", "lines": 5000}, 422, "lines must be"},
		{"POST", "/v1/machines/a/services/web/actions", "", map[string]any{"type": "restart"}, 202, `"status":"pending"`},
		{"GET", "/v1/machines/a/service-actions/1", "", nil, 200, `"type":"restart"`},
		{"GET", "/v1/machines/b/service-actions/1", "", nil, 404, "Action 1 doesn't exist on this machine."},
		{"GET", "/v1/machines/a/service-actions/x", "", nil, 404, "is not an id"},
		{"GET", "/v1/machines/a/service-actions", "", nil, 401, "Bearer"},
		{"GET", "/v1/machines/a/service-actions?wait=31", "token-a", nil, 400, "wait must be a whole number of seconds from 0 to 30."},
		{"GET", "/v1/machines/b/service-actions", "token-b", nil, 200, `{"data":[]}`},
		{"GET", "/v1/machines/a/service-actions", "token-a", nil, 200, `"status":"in_progress"`},
		{"PATCH", "/v1/machines/a/service-actions/1", "", map[string]any{"status": "completed"}, 401, "Bearer"},
		{"PATCH", "/v1/machines/a/service-actions/1", "token-a", map[string]any{"status": "pending"}, 422, "status must be completed or failed."},
		{"PATCH", "/v1/machines/a/service-actions/1", "token-a", map[string]any{"status": "completed"}, 204, ""},
		{"PATCH", "/v1/machines/a/service-actions/1", "token-a", map[string]any{"status": "completed"}, 409, "not in_progress"},
		{"GET", "/v1/machines/a/service-actions/1", "", nil, 200, `"status":"completed"`},
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
		{"POST", "/v1/machines/a/services/actions/actions", map[string]any{"type": "logs"}, 202, `"service":"actions"`},
		{"GET", "/v1/machines/a/service-actions/1", nil, 200, `"service":"actions"`},
	} {
		if status, body := h.call(t, c.method, c.path, "", c.body); status != c.status || !strings.Contains(body, c.has) {
			t.Errorf("%s %s: %d %s, want %d with %q", c.method, c.path, status, body, c.status, c.has)
		}
	}
}

func TestLongPollEndsWhenTheHubStops(t *testing.T) {
	h := newTestHub(t)
	done := make(chan int)
	go func() {
		status, _ := h.call(t, "GET", "/v1/machines/a/service-actions?wait=30", "token-a", nil)
		done <- status
	}()
	time.Sleep(300 * time.Millisecond)
	h.srv.StopWaiting()
	select {
	case status := <-done:
		if status != 200 {
			t.Errorf("status %d", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the long poll held on after the hub stopped waiting")
	}
}

// TestDaemon runs the daemon against the real routes and a fake Docker: it reports, runs each
// action and its result reaches the hub, and the logs never reach the hub's log or the daemon's.
func TestDaemon(t *testing.T) {
	h := newTestHub(t)
	f := newFakeDocker(t,
		&fakeContainer{ID: "aaa", Name: "web", Image: "nginx", State: "running", Logs: []string{"secret-looking log line"}},
		&fakeContainer{ID: "bbb", Name: "worker", Image: "w", State: "exited", ExitCode: 3},
	)
	var daemonLogs syncBuffer
	d := NewDaemon(client.New(h.URL, "token-a"), "a", f.socket, slog.New(slog.NewTextHandler(&daemonLogs, nil)))
	d.Every = time.Hour // reports come at the start and after each action that changes a container
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { d.Run(ctx); close(stopped) }()
	t.Cleanup(func() { cancel(); <-stopped })

	s := NewStore(h.db)
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("the first report", func() bool { l, _ := s.List(ctx, 1); return len(l) == 2 })

	do := func(in ActionInput, service string) Action {
		t.Helper()
		a, err := s.CreateAction(ctx, 1, service, in)
		if err != nil {
			t.Fatal(err)
		}
		a, err = s.Await(ctx, 1, a.ID, 5*time.Second)
		if err != nil || !a.Finished() {
			t.Fatalf("%s %s = %+v, %v", in.Type, service, a, err)
		}
		return a
	}
	if a := do(ActionInput{Type: TypeStop}, "web"); a.Status != StatusCompleted || a.Output != nil {
		t.Errorf("stop = %+v", a)
	}
	waitFor("the report after the stop", func() bool {
		sv, _ := s.Get(ctx, 1, "web")
		return sv.State == StateExited && sv.StoppedByTender
	})
	if a := do(ActionInput{Type: TypeStart}, "web"); a.Status != StatusCompleted {
		t.Errorf("start = %+v", a)
	}
	waitFor("running again", func() bool { sv, _ := s.Get(ctx, 1, "web"); return sv.State == StateRunning && !sv.StoppedByTender })
	if a := do(ActionInput{Type: TypeRestart}, "worker"); a.Status != StatusCompleted {
		t.Errorf("restart = %+v", a)
	}
	a := do(ActionInput{Type: TypeLogs, Lines: ptr(10)}, "web")
	if a.Status != StatusCompleted || a.Output == nil || !strings.Contains(*a.Output, "secret-looking log line") {
		t.Errorf("logs = %+v", a)
	}

	// The container goes away before the daemon gets to it: the action fails with Docker's reason.
	f.remove("worker")
	a = do(ActionInput{Type: TypeRestart}, "worker")
	if a.Status != StatusFailed || a.Output == nil || *a.Output != "Could not restart worker: Docker answered 404: No such container: worker." {
		t.Errorf("restart of a gone container = %+v", a)
	}
	// A failed action doesn't bring the report forward, so the result is there to read before the
	// report removes the service, and its actions with it. The next start does.
	do(ActionInput{Type: TypeStart}, "web")
	waitFor("worker to leave the list", func() bool { l, _ := s.List(ctx, 1); return len(l) == 1 })

	for name, logs := range map[string]string{"hub": h.logs.String(), "daemon": daemonLogs.String()} {
		if strings.Contains(logs, "secret-looking") {
			t.Errorf("the %s log shows the service's logs:\n%s", name, logs)
		}
	}
	if !strings.Contains(daemonLogs.String(), "service action finished") {
		t.Errorf("daemon log:\n%s", daemonLogs.String())
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

	stop, err := s.CreateAction(ctx, 1, "web", ActionInput{Type: TypeStop})
	if err != nil {
		t.Fatal(err)
	}
	taken, err := s.Take(ctx, 1)
	if err != nil || len(taken) != 1 || taken[0].ID != stop.ID {
		t.Fatalf("take = %+v, %v", taken, err)
	}
	ran := make(chan struct{})
	go func() { d.run(ctx, taken[0]); close(ran) }()
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
	if strings.Contains(h.logs.String(), "service-actions") {
		t.Errorf("polled for actions while Docker is unreachable:\n%s", h.logs.String())
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
