package logs

import (
	"context"
	"encoding/binary"
	"encoding/json"
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

type fakeLine struct {
	at     time.Time
	stderr bool
	text   string
}

type fakeContainer struct {
	id, name string
	tty      bool
	lines    []fakeLine
	ended    chan struct{} // closed when the container stops
	unlisted bool          // gone from the list already, as a container that ran for a moment
}

// fakeDocker serves the Engine API's container list, inspect and follow logs on a Unix socket.
type fakeDocker struct {
	mu         sync.Mutex
	containers []*fakeContainer
	sinces     []string    // the since parameters of the logs requests
	events     chan string // container ids, streamed as start events on GET /events
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/events" {
		if f.events == nil || r.URL.Query().Get("filters") != `{"type":["container"],"event":["start"]}` {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case id := <-f.events:
				f.mu.Lock()
				name := ""
				for _, c := range f.containers {
					if c.id == id {
						name = c.name
					}
				}
				f.mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"Type": "container", "Action": "start", "id": id,
					"Actor": map[string]any{"ID": id, "Attributes": map[string]string{"name": name}}})
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
	f.mu.Lock()
	if r.URL.Path == "/containers/json" {
		var out []map[string]any
		for _, c := range f.containers {
			if !c.unlisted {
				out = append(out, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}})
			}
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/containers/")
	id, op, _ := strings.Cut(rest, "/")
	var c *fakeContainer
	for _, x := range f.containers {
		if x.id == id {
			c = x
		}
	}
	if c == nil {
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if op == "json" {
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Tty": c.tty}})
		return
	}
	q := r.URL.Query()
	f.sinces = append(f.sinces, c.name+"@"+q.Get("since"))
	since := time.Time{}
	if s := q.Get("since"); s != "" {
		sec, frac, _ := strings.Cut(s, ".")
		n, _ := strconv.ParseInt(sec, 10, 64)
		ns, _ := strconv.ParseInt(frac, 10, 64)
		since = time.Unix(n, ns)
	}
	lines, ended := append([]fakeLine(nil), c.lines...), c.ended
	f.mu.Unlock()
	if q.Get("follow") != "1" || q.Get("timestamps") != "1" {
		http.Error(w, "follow with timestamps only", 400)
		return
	}
	for _, l := range lines {
		if l.at.Before(since) {
			continue
		}
		text := l.at.UTC().Format(time.RFC3339Nano) + " " + l.text + "\n"
		if c.tty {
			_, _ = w.Write([]byte(text))
			continue
		}
		var h [8]byte
		h[0] = 1
		if l.stderr {
			h[0] = 2
		}
		// Split the line over two frames, as Docker may.
		half := len(text) / 2
		binary.BigEndian.PutUint32(h[4:], uint32(half))
		_, _ = w.Write(h[:])
		_, _ = w.Write([]byte(text[:half]))
		binary.BigEndian.PutUint32(h[4:], uint32(len(text)-half))
		_, _ = w.Write(h[:])
		_, _ = w.Write([]byte(text[half:]))
	}
	w.(http.Flusher).Flush()
	select {
	case <-ended:
	case <-r.Context().Done():
	}
}

func serveDocker(t *testing.T, f *fakeDocker) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return socket
}

// The source follows each running container from after its cursor, picks up new and re-created
// containers, and keeps to the sink's names.
func TestDockerSource(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	web := &fakeContainer{id: "w1", name: "web", ended: make(chan struct{}), lines: []fakeLine{
		{at: t0.Add(time.Second), text: "old line"}, {at: t0.Add(2 * time.Second), stderr: true, text: "web error"}}}
	db := &fakeContainer{id: "d1", name: "db", ended: make(chan struct{}), lines: []fakeLine{{at: t0.Add(time.Second), text: "db line"}}}
	f := &fakeDocker{containers: []*fakeContainer{web, db}}
	src := dockerSource{socket: serveDocker(t, f), filter: ServicesSource{Names: []string{"web", "api", "tty"}}, log: quiet, poll: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan item, 20)
	done := make(chan struct{})
	go func() {
		src.run(ctx, cursors{ServicesSince: t0, Services: map[string]time.Time{"w1": t0.Add(time.Second)}}, out)
		close(done)
	}()

	got := collect(out, 1, 3*time.Second)
	if len(got) != 1 || got[0].rec.Message != "web error" || got[0].rec.Priority != "err" || got[0].rec.Service != "web" ||
		got[0].rec.Source != "service" || !got[0].rec.Time.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("got %+v, want only the line after the cursor", got)
	}

	// a new container, and web made again under a new id, with a line from after it was.
	f.mu.Lock()
	api := &fakeContainer{id: "a1", name: "api", ended: make(chan struct{}), lines: []fakeLine{{at: t0.Add(-time.Hour), text: "before the sink"}, {at: t0.Add(3 * time.Second), text: "api up"}}}
	tty := &fakeContainer{id: "t1", name: "tty", tty: true, ended: make(chan struct{}), lines: []fakeLine{{at: t0.Add(4 * time.Second), text: "raw line"}}}
	web2 := &fakeContainer{id: "w2", name: "web", ended: make(chan struct{}), lines: []fakeLine{{at: t0.Add(5 * time.Second), text: "web again"}}}
	close(web.ended)
	f.containers = []*fakeContainer{web2, db, api, tty}
	f.mu.Unlock()

	got = collect(out, 3, 3*time.Second)
	msgs := map[string]bool{}
	for _, it := range got {
		msgs[it.rec.Message] = true
	}
	if len(got) != 3 || !msgs["api up"] || !msgs["raw line"] || !msgs["web again"] {
		t.Errorf("got %+v", got)
	}
	if extra := collect(out, 1, 200*time.Millisecond); len(extra) != 0 {
		t.Errorf("then %+v: a line sent twice, or db's", extra)
	}
	f.mu.Lock()
	sinces := strings.Join(f.sinces, " ")
	f.mu.Unlock()
	if !strings.Contains(sinces, "web@1794744001.000000000") || !strings.Contains(sinces, "api@1794744000.000000000") || strings.Contains(sinces, "db@") {
		t.Errorf("logs requests %s", sinces)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("run didn't return when ctx ended")
	}
}

// Without Docker the source waits and tries again, until ctx ends.
func TestDockerSourceNoDocker(t *testing.T) {
	src := dockerSource{socket: "/nonexistent/docker.sock", log: quiet, poll: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	src.run(ctx, cursors{}, make(chan item))
}

// Cursors are kept by container id: a container re-created under the same name doesn't lose the
// old one's last lines, though they are later than the new one's first.
func TestDockerSourceRecreatedKeepsBoth(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	old := &fakeContainer{id: "w1", name: "web", ended: make(chan struct{}), lines: []fakeLine{{at: t0.Add(10 * time.Second), text: "old last"}}}
	recreated := &fakeContainer{id: "w2", name: "web", ended: make(chan struct{}), lines: []fakeLine{{at: t0.Add(5 * time.Second), text: "new first"}}}
	f := &fakeDocker{containers: []*fakeContainer{old}}
	src := dockerSource{socket: serveDocker(t, f), log: quiet, poll: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan item, 10)
	go src.run(ctx, cursors{ServicesSince: t0}, out)
	if got := collect(out, 1, 3*time.Second); len(got) != 1 || got[0].rec.Message != "old last" || got[0].container != "w1" {
		t.Fatalf("got %+v", got)
	}
	f.mu.Lock()
	close(old.ended)
	f.containers = []*fakeContainer{recreated}
	f.mu.Unlock()
	if got := collect(out, 1, 3*time.Second); len(got) != 1 || got[0].rec.Message != "new first" || got[0].container != "w2" {
		t.Errorf("after the re-creation: %+v", got)
	}
}

// A container Docker's events say started is followed at once, even one that is gone from the
// list before the next poll.
func TestDockerSourceFollowsStartEvents(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	ended := make(chan struct{})
	close(ended)
	brief := &fakeContainer{id: "j1", name: "job", unlisted: true, ended: ended, lines: []fakeLine{{at: t0.Add(time.Second), text: "job ran"}}}
	f := &fakeDocker{containers: []*fakeContainer{brief}, events: make(chan string, 1)}
	src := dockerSource{socket: serveDocker(t, f), log: quiet, poll: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan item, 10)
	go src.run(ctx, cursors{ServicesSince: t0}, out)
	time.Sleep(100 * time.Millisecond) // the first poll is done, and the events stream open
	f.events <- "j1"
	if got := collect(out, 1, 3*time.Second); len(got) != 1 || got[0].rec.Message != "job ran" || got[0].rec.Service != "job" {
		t.Errorf("got %+v, want the brief container's line from its start event", got)
	}
}

// A stopped source leaves no connection to Docker open, so restarting pipelines leaks nothing.
func TestDockerSourceClosesItsConnections(t *testing.T) {
	f := &fakeDocker{containers: []*fakeContainer{{id: "w1", name: "web", ended: make(chan struct{})}}, events: make(chan string)}
	dir, err := os.MkdirTemp("", "tdk")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ln, err := net.Listen("unix", filepath.Join(dir, "d.sock"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	open := map[net.Conn]bool{}
	srv := &http.Server{Handler: f, ConnState: func(c net.Conn, s http.ConnState) {
		mu.Lock()
		defer mu.Unlock()
		switch s {
		case http.StateNew:
			open[c] = true
		case http.StateClosed, http.StateHijacked:
			delete(open, c)
		}
	}}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	src := dockerSource{socket: filepath.Join(dir, "d.sock"), log: quiet, poll: 10 * time.Millisecond}
	for range 3 {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { src.run(ctx, cursors{ServicesSince: time.Now()}, make(chan item, 10)); close(done) }()
		time.Sleep(100 * time.Millisecond)
		cancel()
		<-done
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(open)
		mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections to Docker are still open after the sources stopped", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
