package services

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeContainer is one container of the fake Engine API.
type fakeContainer struct {
	ID, Name, Image, State, Health, StartedAt string
	ExitCode, RestartCount                    int64
	Project, Policy                           string
	Tty                                       bool
	Logs                                      []string // lines; even ones go to stdout, odd ones to stderr
}

// fakeDocker serves the Engine API routes the daemon uses on a Unix socket in a temporary folder.
type fakeDocker struct {
	socket string
	mu     sync.Mutex
	byName map[string]*fakeContainer
	order  []string
	calls  []string
}

func newFakeDocker(t *testing.T, cs ...*fakeContainer) *fakeDocker {
	t.Helper()
	dir, err := os.MkdirTemp("", "docker") // t.TempDir() can be longer than a socket path may be
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fakeDocker{socket: filepath.Join(dir, "docker.sock"), byName: map[string]*fakeContainer{}}
	for _, c := range cs {
		f.add(c)
	}
	ln, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

func (f *fakeDocker) add(c *fakeContainer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byName[c.Name] = c
	f.order = append(f.order, c.Name)
}

func (f *fakeDocker) remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byName, name)
}

func (f *fakeDocker) get(name string) *fakeContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byName[name]
}

func (f *fakeDocker) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

// find looks a container up by name or id.
func (f *fakeDocker) find(ref string) *fakeContainer {
	if c := f.byName[ref]; c != nil {
		return c
	}
	for _, c := range f.byName {
		if c.ID == ref {
			return c
		}
	}
	return nil
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
	notFound := func(ref string) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such container: " + ref})
	}
	if r.Method == http.MethodGet && r.URL.Path == "/containers/json" {
		if r.URL.Query().Get("all") != "1" {
			http.Error(w, "want all=1", http.StatusBadRequest)
			return
		}
		list := []map[string]any{}
		for _, name := range f.order {
			c := f.byName[name]
			if c == nil {
				continue
			}
			labels := map[string]string{}
			if c.Project != "" {
				labels["com.docker.compose.project"] = c.Project
			}
			list = append(list, map[string]any{"Id": c.ID, "Names": []string{"/" + c.Name}, "Image": c.Image, "State": c.State, "Labels": labels})
		}
		_ = json.NewEncoder(w).Encode(list)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/containers/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	ref, op, _ := strings.Cut(rest, "/")
	c := f.find(ref)
	if c == nil {
		notFound(ref)
		return
	}
	switch {
	case r.Method == http.MethodGet && op == "json":
		state := map[string]any{"Status": c.State, "ExitCode": c.ExitCode, "StartedAt": c.StartedAt}
		if c.StartedAt == "" {
			state["StartedAt"] = "0001-01-01T00:00:00Z"
		}
		if c.Health != "" {
			state["Health"] = map[string]string{"Status": c.Health}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": c.ID, "Name": "/" + c.Name, "RestartCount": c.RestartCount,
			"State": state, "Config": map[string]any{"Tty": c.Tty}, "HostConfig": map[string]any{"RestartPolicy": map[string]string{"Name": c.Policy}}})
	case r.Method == http.MethodPost && op == "start":
		if c.State == StateRunning {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		c.State, c.ExitCode = StateRunning, 0
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && op == "stop":
		if r.URL.RawQuery != "" {
			http.Error(w, "the stop timeout must be the container's own", http.StatusBadRequest)
			return
		}
		if c.State != StateRunning {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		c.State, c.ExitCode = StateExited, 0
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && op == "restart":
		c.State = StateRunning
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && op == "logs":
		q := r.URL.Query()
		if q.Get("stdout") != "1" || q.Get("stderr") != "1" || q.Get("timestamps") != "1" {
			http.Error(w, "want stdout, stderr and timestamps", http.StatusBadRequest)
			return
		}
		n, err := strconv.Atoi(q.Get("tail"))
		if err != nil {
			http.Error(w, "bad tail", http.StatusBadRequest)
			return
		}
		lines := c.Logs
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		for i, l := range lines {
			line := []byte("2026-11-15T10:00:00.000000000Z " + l + "\n")
			if c.Tty {
				_, _ = w.Write(line)
				continue
			}
			var header [8]byte
			header[0] = byte(1 + i%2)
			binary.BigEndian.PutUint32(header[4:], uint32(len(line)))
			_, _ = w.Write(header[:])
			_, _ = w.Write(line)
		}
	default:
		http.NotFound(w, r)
	}
}

func TestContainers(t *testing.T) {
	f := newFakeDocker(t,
		&fakeContainer{ID: "aaa", Name: "shop-web-1", Image: "nginx:1.27", State: "running", Health: "healthy", StartedAt: "2026-11-15T09:00:00.123456789Z", RestartCount: 2, Project: "shop", Policy: "unless-stopped"},
		&fakeContainer{ID: "bbb", Name: "worker", Image: "worker:latest", State: "exited", ExitCode: 137},
		&fakeContainer{ID: "ccc", Name: "odd", Image: "x", State: "hibernating"},
		&fakeContainer{ID: "ddd", Name: "check", Image: "x", State: "running", Health: "none"},
	)
	got, err := NewDocker(f.socket).Containers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d containers, want 3 (an unknown state is left out): %+v", len(got), got)
	}
	web, worker, check := got[0], got[1], got[2]
	if web.Name != "shop-web-1" || web.ContainerID != "aaa" || web.Image != "nginx:1.27" || web.State != "running" ||
		web.Health == nil || *web.Health != "healthy" || web.ExitCode != nil || web.RestartCount != 2 ||
		web.Project == nil || *web.Project != "shop" || web.RestartPolicy != "unless-stopped" || web.StartedAt == nil || !web.StartedAt.Equal(time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("web = %+v", web)
	}
	if worker.RestartPolicy != "no" { // Docker's empty name
		t.Errorf("worker's policy = %q", worker.RestartPolicy)
	}
	if worker.State != "exited" || worker.ExitCode == nil || *worker.ExitCode != 137 || worker.Project != nil || worker.StartedAt != nil || worker.Health != nil {
		t.Errorf("worker = %+v", worker)
	}
	if check.Health != nil {
		t.Errorf("a health Tender doesn't know is null, got %q", *check.Health)
	}
	if calls := f.called(); calls[0] != "GET /containers/json?all=1" || calls[1] != "GET /containers/aaa/json" {
		t.Errorf("calls = %q", calls)
	}
}

func TestActs(t *testing.T) {
	f := newFakeDocker(t, &fakeContainer{ID: "aaa", Name: "web", State: "running"})
	d := NewDocker(f.socket)
	ctx := context.Background()
	for _, c := range []struct{ typ, want string }{{"stop", "exited"}, {"stop", "exited"}, {"start", "running"}, {"start", "running"}, {"restart", "running"}} {
		if err := d.Act(ctx, c.typ, "web"); err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		if got := f.get("web").State; got != c.want {
			t.Errorf("after %s state = %s, want %s", c.typ, got, c.want)
		}
	}
	err := d.Act(ctx, "restart", "gone")
	if err == nil || err.Error() != "Docker answered 404: No such container: gone" {
		t.Errorf("unknown container: %v", err)
	}
}

func TestLogs(t *testing.T) {
	var many []string
	for i := range 3000 {
		many = append(many, fmt.Sprintf("line %d", i))
	}
	f := newFakeDocker(t,
		&fakeContainer{ID: "aaa", Name: "web", State: "running", Logs: []string{"out one", "err one", "out two"}},
		&fakeContainer{ID: "bbb", Name: "tty", State: "running", Tty: true, Logs: []string{"raw one", "raw two"}},
		&fakeContainer{ID: "ccc", Name: "many", State: "running", Logs: many},
		&fakeContainer{ID: "ddd", Name: "wide", State: "running", Logs: []string{strings.Repeat("a", 200<<10), strings.Repeat("b", 100<<10), "last"}},
		&fakeContainer{ID: "eee", Name: "quiet", State: "running"},
	)
	d := NewDocker(f.socket)
	ctx := context.Background()
	ts := "2026-11-15T10:00:00.000000000Z "

	got, err := d.Logs(ctx, "web", 200)
	if want := ts + "out one\n" + ts + "err one\n" + ts + "out two\n"; err != nil || got != want {
		t.Errorf("multiplexed logs = %q, %v; want %q", got, err, want)
	}
	if got, err := d.Logs(ctx, "web", 2); err != nil || got != ts+"err one\n"+ts+"out two\n" {
		t.Errorf("last 2 lines = %q, %v", got, err)
	}
	if got, err := d.Logs(ctx, "tty", 200); err != nil || got != ts+"raw one\n"+ts+"raw two\n" {
		t.Errorf("tty logs = %q, %v", got, err)
	}
	got, err = d.Logs(ctx, "many", 5000)
	if lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n"); err != nil || len(lines) != MaxLines || lines[MaxLines-1] != ts+"line 2999" {
		t.Errorf("many logs: %d lines, last %q, %v", len(lines), lines[len(lines)-1], err)
	}
	if calls := f.called(); !strings.Contains(strings.Join(calls, "\n"), "tail=2000") {
		t.Errorf("asked Docker for more than 2000 lines: %q", calls)
	}
	got, err = d.Logs(ctx, "wide", 200)
	if err != nil || len(got) > MaxOutputBytes || !strings.HasSuffix(got, ts+"last\n") || strings.Contains(got, "aaaa") || !strings.HasPrefix(got, ts+"bbbb") {
		t.Errorf("wide logs: %d bytes, starts %q, %v", len(got), got[:min(len(got), 40)], err)
	}
	if got, err := d.Logs(ctx, "quiet", 200); err != nil || got != "" {
		t.Errorf("no logs = %q, %v", got, err)
	}
	if _, err := d.Logs(ctx, "gone", 200); err == nil || !strings.Contains(err.Error(), "No such container: gone") {
		t.Errorf("unknown container: %v", err)
	}
}

func TestNoDocker(t *testing.T) {
	dir, err := os.MkdirTemp("", "docker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx := context.Background()

	// No socket at all.
	_, err = NewDocker(filepath.Join(dir, "missing.sock")).Containers(ctx)
	if !errors.Is(err, errNoDocker) {
		t.Errorf("missing socket: %v, want errNoDocker", err)
	}

	// A socket that nothing listens on: Docker stopped.
	stale := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err = NewDocker(stale).Containers(ctx); !errors.Is(err, errUnreachable) {
		t.Errorf("refused: %v, want errUnreachable", err)
	}

	// A socket that takes connections but never answers: Docker is stuck.
	stuck := filepath.Join(dir, "stuck.sock")
	hang, err := net.Listen("unix", stuck)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hang.Close() }()
	tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err = NewDocker(stuck).Containers(tctx); !errors.Is(err, errUnreachable) {
		t.Errorf("no answer: %v, want errUnreachable", err)
	}

	// A socket the daemon may not use.
	if os.Geteuid() == 0 {
		t.Skip("root may use any socket")
	}
	f := newFakeDocker(t)
	if err := os.Chmod(f.socket, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = NewDocker(f.socket).Containers(ctx); !errors.Is(err, errDenied) {
		t.Errorf("permission denied: %v, want errDenied", err)
	}
}

func TestTail(t *testing.T) {
	var tl tail
	chunk := strings.Repeat("x", 1000) + "\n"
	for range 1000 { // about 1 MB, more than twice the limit
		_, _ = tl.Write([]byte(chunk))
	}
	if len(tl.buf) > 2*MaxOutputBytes {
		t.Errorf("tail holds %d bytes", len(tl.buf))
	}
	got := tl.text(MaxLines)
	if len(got) > MaxOutputBytes || !strings.HasPrefix(got, "xxx") || len(got)%len(chunk) != 0 {
		t.Errorf("text: %d bytes, whole lines only", len(got))
	}
	invalid := tail{buf: []byte("ok \xff\n")}
	if got := invalid.text(10); got != "ok �\n" {
		t.Errorf("invalid UTF-8 = %q", got)
	}
}

func TestFitJSON(t *testing.T) {
	line := strings.Repeat("\x1b", 1000) + "\n" // escapes to six bytes a character
	big := strings.Repeat(line, 250)
	got := fitJSON(big)
	b, _ := json.Marshal(got)
	if len(b) > maxEncoded || got == "" || !strings.HasSuffix(big, got) {
		t.Errorf("fitted: %d encoded bytes, %d characters", len(b), len(got))
	}
	if fitJSON("short\n") != "short\n" {
		t.Error("short text changed")
	}
}

// chmod0 takes every permission off the socket, so a user other than root can't connect.
func chmod0(socket string) error {
	if os.Geteuid() == 0 {
		return errors.New("root may use any socket")
	}
	return os.Chmod(socket, 0)
}
