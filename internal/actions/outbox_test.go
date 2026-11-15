package actions

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/client"
)

// proxy stands between the daemon and the hub, so a test can lose answers and refuse requests.
// intercept sees each request with its body; it returns false to let the request through.
type proxy struct {
	*httptest.Server
	mu        sync.Mutex
	requests  []string // "PATCH <status>"
	intercept func(w http.ResponseWriter, r *http.Request, body string) bool
}

func newProxy(t *testing.T, h *hub) *proxy {
	t.Helper()
	target, _ := url.Parse(h.URL)
	rp := httputil.NewSingleHostReverseProxy(target)
	p := &proxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		p.mu.Lock()
		if r.Method == http.MethodPatch {
			p.requests = append(p.requests, "PATCH "+string(b))
		}
		intercept := p.intercept
		p.mu.Unlock()
		if intercept != nil && intercept(w, r, string(b)) {
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *proxy) set(f func(w http.ResponseWriter, r *http.Request, body string) bool) {
	p.mu.Lock()
	p.intercept = f
	p.mu.Unlock()
}

func (p *proxy) patches(has string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, r := range p.requests {
		if strings.Contains(r, has) {
			n++
		}
	}
	return n
}

func startDaemon(t *testing.T, base, stateDir string, runner Runner) (*Daemon, *syncBuffer, context.CancelFunc) {
	t.Helper()
	logs := &syncBuffer{}
	d := NewDaemon(client.New(base, "token-a"), "a", slog.New(slog.NewTextHandler(logs, nil)))
	d.Services, d.StateDir, d.Every = runner, stateDir, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { d.Run(ctx); close(stopped) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-stopped }) }
	t.Cleanup(stop)
	return d, logs, stop
}

func ran(r *fakeRunner, event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == event {
			n++
		}
	}
	return n
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// A claim that reached the hub but whose answer was lost is retried with the same key, so the hub
// replays its 204: the daemon runs the action once.
func TestClaimAnswerLost(t *testing.T) {
	h := newTestHub(t)
	p := newProxy(t, h)
	var lost atomic.Bool
	p.set(func(w http.ResponseWriter, r *http.Request, body string) bool {
		if !strings.Contains(body, StatusInProgress) || !lost.CompareAndSwap(false, true) {
			return false
		}
		rec := httptest.NewRecorder()
		httputil.NewSingleHostReverseProxy(mustURL(h.URL)).ServeHTTP(rec, r) // the hub claims it
		conn, _, _ := w.(http.Hijacker).Hijack()                           // and the answer is lost
		_ = conn.Close()
		return true
	})
	runner := &fakeRunner{}
	_, daemonLogs, _ := startDaemon(t, p.URL, t.TempDir(), runner)
	a, _ := h.store.Create(context.Background(), 1, Input{Type: TypeStop, Service: ptr("web")})
	got, err := h.store.Await(context.Background(), 1, a.ID, 10*time.Second)
	if err != nil || got.Status != StatusCompleted {
		t.Fatalf("stop = %+v, %v; daemon log:\n%s\nhub log:\n%s", got, err, daemonLogs, h.logs)
	}
	if n := ran(runner, "stop web"); n != 1 || !lost.Load() || p.patches(StatusInProgress) < 2 {
		t.Errorf("ran %d times, answer lost %v, %d claims; want once after a retried claim", n, lost.Load(), p.patches(StatusInProgress))
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

// A report the hub can't take waits and is sent until it is: here after the hub gave up on the
// action as unanswered, and the late completed stop still reaches the services domain.
func TestReportWaitsForTheHub(t *testing.T) {
	h := newTestHub(t)
	p := newProxy(t, h)
	down := func(w http.ResponseWriter, r *http.Request, body string) bool {
		if r.Method == http.MethodPatch && !strings.Contains(body, StatusInProgress) {
			http.Error(w, "hub restarting", http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	p.set(down)
	state := t.TempDir()
	runner := &fakeRunner{}
	startDaemon(t, p.URL, state, runner)
	ctx := context.Background()
	a, _ := h.store.Create(ctx, 1, Input{Type: TypeStop, Service: ptr("web")})
	eventually(t, "two refused reports", func() bool { return p.patches(StatusCompleted) >= 2 })
	if files, _ := filepath.Glob(filepath.Join(state, "actions", "*.json")); len(files) != 1 {
		t.Errorf("waiting reports on disk = %q", files)
	}
	old := time.Now().Add(-16 * time.Minute).Unix()
	if _, err := h.store.db.Write.Exec(`UPDATE actions SET updated_at = ? WHERE id = ?`, old, a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.store.Get(ctx, 1, a.ID); got.Status != StatusFailed || deref(got.Output) != notAnswered {
		t.Fatalf("expired = %+v", got)
	}
	p.set(nil)
	var got Action
	var err error
	eventually(t, "the late report", func() bool { got, err = h.store.Get(ctx, 1, a.ID); return got.Status == StatusCompleted })
	if err != nil || got.Output != nil || ran(runner, "stop web") != 1 {
		t.Errorf("after the hub came back = %+v, %v", got, err)
	}
	svc := h.store.services.(*fakeServices)
	svc.mu.Lock()
	heard := slices.Clone(svc.finished)
	svc.mu.Unlock()
	if !slices.Equal(heard, []string{"stop web"}) {
		t.Errorf("the services domain heard %q", heard)
	}
	eventually(t, "the report to leave the disk", func() bool {
		files, _ := filepath.Glob(filepath.Join(state, "actions", "*"))
		return len(files) == 0
	})
}

// A report still waiting when the daemon stops is sent by the next one. Logs never go to disk: a
// logs report that waited across a restart says to ask again. An action the daemon was running
// when it stopped is reported as failed.
func TestReportsOutliveTheDaemon(t *testing.T) {
	h := newTestHub(t)
	p := newProxy(t, h)
	p.set(func(w http.ResponseWriter, r *http.Request, body string) bool {
		if r.Method == http.MethodPatch && !strings.Contains(body, StatusInProgress) {
			http.Error(w, "hub restarting", http.StatusServiceUnavailable)
			return true
		}
		return false
	})
	state := t.TempDir()
	runner := &fakeRunner{}
	_, _, stop := startDaemon(t, p.URL, state, runner)
	ctx := context.Background()
	logs, _ := h.store.Create(ctx, 1, Input{Type: TypeLogs, Service: ptr("web")})
	eventually(t, "a refused report", func() bool { return p.patches(StatusCompleted) >= 1 })
	stop()

	running, _ := h.store.Create(ctx, 1, Input{Type: TypeRestart, Service: ptr("web")})
	if err := h.store.Claim(ctx, 1, running.ID); err != nil {
		t.Fatal(err)
	}
	rec := []byte(`{"id":` + itoa(int(running.ID)) + `,"key":"k-running","type":"restart"}`)
	if err := os.WriteFile(filepath.Join(state, "actions", itoa(int(running.ID))+".json"), rec, 0o600); err != nil {
		t.Fatal(err)
	}
	err := filepath.Walk(state, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(path)
			if bytes.Contains(b, []byte("secret-looking")) {
				t.Errorf("%s holds the logs: %s", path, b)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("%s is %v", path, info.Mode().Perm())
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	p.set(nil)
	_, daemonLogs, _ := startDaemon(t, p.URL, state, runner)
	for id, want := range map[int64]string{logs.ID: lostLogs, running.ID: stoppedWhileRunning} {
		var got Action
		eventually(t, "the waiting report", func() bool { got, _ = h.store.Get(ctx, 1, id); return got.Finished() })
		if got.Status != StatusFailed || deref(got.Output) != want {
			t.Errorf("action %d = %+v, want failed with %q", id, got, want)
		}
	}
	if strings.Contains(daemonLogs.String(), "secret-looking") || ran(runner, "logs web") != 1 || ran(runner, "restart web") != 0 {
		t.Errorf("ran %q; log:\n%s", runner.events, daemonLogs.String())
	}
}
