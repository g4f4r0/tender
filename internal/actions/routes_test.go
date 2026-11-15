package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
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

type hub struct {
	*httptest.Server
	srv   *server.Server
	store *Store
	logs  *syncBuffer
}

func newTestHub(t *testing.T) *hub {
	t.Helper()
	d := newDB(t)
	logs := &syncBuffer{}
	srv := server.New(d, slog.New(slog.NewTextHandler(logs, nil)), identity{d})
	st := NewStore(d, &fakeServices{})
	st.Routes(srv)
	h := &hub{Server: httptest.NewServer(srv), srv: srv, store: st, logs: logs}
	t.Cleanup(h.Close)
	return h
}

func (h *hub) call(t *testing.T, method, path, token string, body any) (int, http.Header, string) {
	t.Helper()
	var buf bytes.Buffer
	if s, ok := body.(string); ok {
		buf.WriteString(s)
	} else if body != nil {
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
	return res.StatusCode, res.Header, string(b)
}

func TestRoutes(t *testing.T) {
	h := newTestHub(t)
	for _, c := range []struct {
		method, path, token string
		body                any
		status              int
		has                 string
	}{
		{"GET", "/v1/machines/a/actions", "", nil, 200, `{"data":[]}`},
		{"GET", "/v1/machines/nope/actions", "", nil, 404, `Machine \"nope\" doesn't exist.`},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "explode"}, 422, "type must be start, stop, restart, logs or upgrade."},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "restart"}, 422, "service is required for restart."},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "restart", "service": "web", "lines": 5}, 422, "only for a logs action"},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "logs", "service": "web", "priority": "err"}, 422, "only for the machine's logs"},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "logs", "lines": 5000}, 422, "lines must be"},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "logs", "since": "2w"}, 422, "since must be"},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "logs", "colour": "red"}, 400, "unknown field"},
		{"POST", "/v1/machines/a/actions", "", `{"type": "logs"`, 400, ""},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "stop", "service": "db"}, 404, `Service \"db\" doesn't exist`},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "restart", "service": "web"}, 202, `"status":"pending"`},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "logs", "unit": "tenderd", "priority": "warning", "since": "1h", "lines": 50}, 202,
			`"service":null,"unit":"tenderd","priority":"warning","since":"1h","lines":50`},
		// A service may be called "actions": actions aren't under /services, so nothing is in its way.
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "logs", "service": "actions"}, 202, `"service":"actions"`},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "upgrade"}, 202, `"version":null,"packages":"security"`},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "upgrade", "packages": "most"}, 422, "packages must be security"},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "upgrade", "service": "web"}, 422, "An upgrade takes only packages"},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "logs", "packages": "all"}, 422, "packages is only for an upgrade"},
		{"POST", "/v1/machines/a/actions", "", map[string]any{"type": "update"}, 422, "POST /v1/updates/rollouts"},
		{"GET", "/v1/machines/a/actions", "", nil, 200, `"id":3`},
		{"GET", "/v1/machines/a/actions?wait=5", "", nil, 400, "wait is only for the machine's daemon"},
		{"GET", "/v1/machines/a/actions/1", "", nil, 200, `"type":"restart"`},
		{"GET", "/v1/machines/b/actions/1", "", nil, 404, "Action 1 doesn't exist on this machine."},
		{"GET", "/v1/machines/a/actions/x", "", nil, 404, "is not an id"},
		{"GET", "/v1/machines/a/actions?wait=31", "token-a", nil, 400, "wait must be a whole number of seconds from 0 to 30."},
		{"GET", "/v1/machines/a/actions", "token-b", nil, 403, "another machine"},
		{"GET", "/v1/machines/b/actions", "token-b", nil, 200, `{"data":[]}`},
		{"GET", "/v1/machines/a/actions", "token-a", nil, 200, `"id":1,"machine":"a","type":"restart","service":"web",`},
		{"GET", "/v1/machines/a/actions", "token-a", nil, 200, `"status":"pending"`},
		{"PATCH", "/v1/machines/a/actions/1", "", map[string]any{"status": "completed"}, 401, "Bearer"},
		{"PATCH", "/v1/machines/a/actions/1", "token-a", map[string]any{"status": "pending"}, 422, "status must be in_progress, completed or failed."},
		{"PATCH", "/v1/machines/a/actions/1", "token-a", map[string]any{"status": "completed"}, 409, "pending, not in_progress"},
		{"PATCH", "/v1/machines/a/actions/1", "token-a", map[string]any{"status": "in_progress", "output": "x"}, 422, "output is only for"},
		{"PATCH", "/v1/machines/a/actions/1", "token-a", map[string]any{"status": "in_progress"}, 204, ""},
		{"PATCH", "/v1/machines/a/actions/1", "token-a", map[string]any{"status": "in_progress"}, 409, "in_progress, not pending"},
		{"PATCH", "/v1/machines/a/actions/1", "token-a", map[string]any{"status": "completed"}, 204, ""},
		{"PATCH", "/v1/machines/a/actions/1", "token-a", map[string]any{"status": "completed"}, 409, "not in_progress"},
		{"GET", "/v1/machines/a/actions/1", "", nil, 200, `"status":"completed"`},
	} {
		status, header, body := h.call(t, c.method, c.path, c.token, c.body)
		if status != c.status || !strings.Contains(body, c.has) {
			t.Errorf("%s %s %v: %d %s, want %d with %q", c.method, c.path, c.body, status, body, c.status, c.has)
		}
		if status >= 400 && (!strings.Contains(body, `"detail"`) || header.Get("Content-Type") != "application/problem+json") {
			t.Errorf("%s %s: no problem details: %s %s", c.method, c.path, header.Get("Content-Type"), body)
		}
		if status == 202 && !strings.HasPrefix(header.Get("Location"), "/v1/machines/a/actions/") {
			t.Errorf("%s %s: Location %q", c.method, c.path, header.Get("Location"))
		}
	}
	// The people's list leaves the output out; one action has it.
	if _, _, body := h.call(t, "GET", "/v1/machines/a/actions", "", nil); strings.Contains(body, `"output"`) {
		t.Errorf("the list has output: %s", body)
	}
}

func TestLongPollEndsWhenTheHubStops(t *testing.T) {
	h := newTestHub(t)
	done := make(chan int)
	go func() {
		status, _, _ := h.call(t, "GET", "/v1/machines/a/actions?wait=30", "token-a", nil)
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

// fakeRunner is the services domain's daemon: it records what it was asked and when it held the turn.
type fakeRunner struct {
	mu      sync.Mutex
	events  []string
	held    bool
	changed int
}

func (f *fakeRunner) Lock() {
	f.mu.Lock()
	f.events, f.held = append(f.events, "lock"), true
	f.mu.Unlock()
}

func (f *fakeRunner) Unlock() {
	f.mu.Lock()
	f.events, f.held = append(f.events, "unlock"), false
	f.mu.Unlock()
}

func (f *fakeRunner) Changed() {
	f.mu.Lock()
	f.changed++
	f.mu.Unlock()
}

func (f *fakeRunner) Service(_ context.Context, typ, service string, lines int, since time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.held {
		f.events = append(f.events, "ran without the turn")
	}
	f.events = append(f.events, typ+" "+service)
	switch {
	case service == "gone":
		return "", errorString("Docker answered 404: No such container: gone")
	case typ == TypeLogs:
		return "secret-looking log line, " + strings.Join([]string{time.Duration(since).String(), itoa(lines)}, " ") + "\n", nil
	}
	return "", nil
}

type errorString string

func (e errorString) Error() string { return string(e) }

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// fakeJournal puts a journalctl on a temporary PATH that prints two entries and saves its arguments.
func fakeJournal(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "journalctl")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" > "+filepath.Join(dir, "args")+"\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	was := journalctl
	journalctl = bin
	t.Cleanup(func() { journalctl = was })
	return dir
}

const twoEntries = `cat <<'EOF'
{"__REALTIME_TIMESTAMP":"1763200800000000","_SYSTEMD_UNIT":"tenderd.service","MESSAGE":"daemon started","PRIORITY":"6"}
{"__REALTIME_TIMESTAMP":"1763200801500000","SYSLOG_IDENTIFIER":"kernel","MESSAGE":[104,105,255]}
{"__REALTIME_TIMESTAMP":"1763200802000000","_SYSTEMD_UNIT":"nginx.service"}
EOF
`

// TestDaemon runs the actions daemon against the real routes: it takes each action, runs a service
// action in the services daemon's turn and asks for a report after it, reads the journal, and its
// results reach the hub. The output never reaches the daemon's log or the hub's.
func TestDaemon(t *testing.T) {
	h := newTestHub(t)
	dir := fakeJournal(t, twoEntries)
	runner := &fakeRunner{}
	var daemonLogs syncBuffer
	d := NewDaemon(client.New(h.URL, "token-a"), "a", slog.New(slog.NewTextHandler(&daemonLogs, nil)))
	d.Services = runner
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { d.Run(ctx); close(stopped) }()
	t.Cleanup(func() { cancel(); <-stopped })

	do := func(in Input) Action {
		t.Helper()
		a, err := h.store.Create(ctx, 1, in)
		if err != nil {
			t.Fatal(err)
		}
		a, err = h.store.Await(ctx, 1, a.ID, 5*time.Second)
		if err != nil || !a.Finished() {
			t.Fatalf("%+v = %+v, %v", in, a, err)
		}
		return a
	}
	if a := do(Input{Type: TypeStop, Service: ptr("web")}); a.Status != StatusCompleted || a.Output != nil {
		t.Errorf("stop = %+v", a)
	}
	a := do(Input{Type: TypeLogs, Service: ptr("web"), Lines: ptr(10), Since: ptr("1h")})
	if a.Status != StatusCompleted || a.Output == nil || *a.Output != "secret-looking log line, 1h0m0s 10\n" {
		t.Errorf("service logs = %+v", a)
	}
	a = do(Input{Type: TypeLogs, Unit: ptr("tenderd"), Priority: ptr("warning"), Since: ptr("1h"), Lines: ptr(50)})
	want := "2025-11-15T10:00:00.000000000Z tenderd.service daemon started\n2025-11-15T10:00:01.500000000Z kernel hi�\n"
	if a.Status != StatusCompleted || a.Output == nil || *a.Output != want {
		t.Errorf("machine logs = %q, want %q", deref(a.Output), want)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(args), "--output=json --no-pager --quiet --lines=50 --unit=tenderd --priority=warning --since=@") {
		t.Errorf("journalctl got %q", args)
	}
	a = do(Input{Type: TypeRestart, Service: ptr("gone")})
	if a.Status != StatusFailed || deref(a.Output) != "Could not restart gone: Docker answered 404: No such container: gone." {
		t.Errorf("restart of a gone container = %+v", a)
	}

	runner.mu.Lock()
	events, changed := slices.Clone(runner.events), runner.changed
	runner.mu.Unlock()
	wantEvents := []string{"lock", "stop web", "unlock", "lock", "logs web", "unlock", "lock", "restart gone", "unlock"}
	if !slices.Equal(events, wantEvents) || changed != 2 {
		t.Errorf("runner events = %q, %d reports asked; want %q and 2", events, changed, wantEvents)
	}
	for name, logs := range map[string]string{"hub": h.logs.String(), "daemon": daemonLogs.String()} {
		if strings.Contains(logs, "secret-looking") || strings.Contains(logs, "daemon started") {
			t.Errorf("the %s log shows logs:\n%s", name, logs)
		}
	}
	if !strings.Contains(daemonLogs.String(), `msg="action finished"`) {
		t.Errorf("daemon log:\n%s", daemonLogs.String())
	}
}

func TestReadJournal(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1763200900, 0)

	fakeJournal(t, twoEntries)
	if got, err := ReadJournal(ctx, Query{Lines: 5}, now); err != nil || strings.Count(got, "\n") != 2 {
		t.Errorf("entries = %q, %v", got, err)
	}
	dir := fakeJournal(t, "")
	if _, err := ReadJournal(ctx, Query{Since: time.Hour, Lines: 9000}, now); err != nil {
		t.Fatal(err)
	}
	if args, _ := os.ReadFile(filepath.Join(dir, "args")); string(args) != "--output=json --no-pager --quiet --lines=2000 --since=@1763197300\n" {
		t.Errorf("args = %q", args)
	}
	if got, err := ReadJournal(ctx, Query{Lines: 5}, now); err != nil || got != "" {
		t.Errorf("no entries = %q, %v", got, err)
	}
	if _, err := ReadJournal(ctx, Query{Unit: "--output=cat", Lines: 5}, now); err == nil {
		t.Error("a flag got through as the unit")
	}

	// Big entries: the newest 256 KiB, whole lines.
	fakeJournal(t, `i=0; while [ $i -lt 400 ]; do printf '{"__REALTIME_TIMESTAMP":"1","_SYSTEMD_UNIT":"x","MESSAGE":"%01000d"}\n' $i; i=$((i+1)); done`)
	got, err := ReadJournal(ctx, Query{Lines: 2000}, now)
	if err != nil || len(got) > MaxOutputBytes || len(got) < MaxOutputBytes-2000 || !strings.HasSuffix(got, "399\n") {
		t.Errorf("big: %d bytes, %v", len(got), err)
	}

	fakeJournal(t, "echo 'Failed to open journal: Permission denied' >&2; exit 1")
	if _, err := ReadJournal(ctx, Query{Lines: 5}, now); err == nil || err.Error() != "journalctl failed: Failed to open journal: Permission denied" {
		t.Errorf("failure: %v", err)
	}

	was := journalctl
	journalctl = filepath.Join(t.TempDir(), "journalctl")
	defer func() { journalctl = was }()
	if _, err := ReadJournal(ctx, Query{Lines: 5}, now); err != errNoJournal {
		t.Errorf("missing journalctl: %v", err)
	}
	if got := failure(Action{Summary: Summary{Type: TypeLogs}}, errNoJournal); got !=
		"Could not read the machine's logs: journalctl is not installed on this machine, so Tender can't read its logs." {
		t.Errorf("failure = %q", got)
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
