package backups

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/server"
)

type request struct {
	Method, Path, Key string
	Body              []byte
}

func (r request) String() string { return r.Method + " " + r.Path }

// hub is a fake hub that records every request. down closes connections without answering;
// fail answers the next n requests to "METHOD path" with a status.
type hub struct {
	t        *testing.T
	mu       sync.Mutex
	srv      *httptest.Server
	requests []request
	down     bool
	fail     map[string]failure
	nextID   int64
	settings *Settings
}

type failure struct {
	status, times int
}

func newHub(t *testing.T) *hub {
	h := &hub{t: t, fail: map[string]failure{}, nextID: 1, settings: &Settings{
		Destination: Destination{Name: "r2-test", Type: "s3", Endpoint: "https://x.example", Bucket: "b"},
		Schedule:    "30 3 * * *", RetentionDays: 30,
	}}
	h.srv = httptest.NewServer(h)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hub) client() *client.Client { return client.New(h.srv.URL, "hub-token") }

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	defer h.mu.Unlock()
	req := request{r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), body}
	h.requests = append(h.requests, req)
	if r.Header.Get("Authorization") != "Bearer hub-token" {
		h.t.Errorf("%s: bad Authorization header", req)
	}
	if h.down {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
		return
	}
	if f := h.fail[req.String()]; f.times > 0 {
		h.fail[req.String()] = failure{f.status, f.times - 1}
		server.WriteProblem(w, f.status, "Injected", "injected failure")
		return
	}
	switch {
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/backups/settings"):
		if h.settings == nil {
			server.WriteProblem(w, 404, "Not Found", "no backup settings for this machine")
			return
		}
		server.WriteJSON(w, 200, h.settings)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/backups/history"):
		var in HistoryInput
		_ = json.Unmarshal(body, &in)
		id := h.nextID
		h.nextID++
		server.WriteJSON(w, 201, History{ID: id, Type: in.Type, Status: in.Status, SnapshotID: in.SnapshotID, CreatedAt: in.CreatedAt})
	case r.Method == "PATCH":
		id, _ := strconv.ParseInt(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], 10, 64)
		server.WriteJSON(w, 200, History{ID: id})
	case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/backups/snapshots"):
		var in []SnapshotInput
		_ = json.Unmarshal(body, &in)
		list := server.List[Snapshot]{Data: []Snapshot{}}
		for i, s := range in {
			list.Data = append(list.Data, Snapshot{ID: int64(100 + i), ResticID: s.ResticID, SizeBytes: s.SizeBytes, Paths: s.Paths, CreatedAt: s.CreatedAt})
		}
		server.WriteJSON(w, 200, list)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/usage"):
		server.WriteJSON(w, 201, map[string]int{"id": 1})
	default:
		server.WriteProblem(w, 404, "Not Found", "unexpected "+req.String())
	}
}

func (h *hub) setDown(down bool) {
	h.mu.Lock()
	h.down = down
	h.mu.Unlock()
}

func (h *hub) failNext(route string, status, times int) {
	h.mu.Lock()
	h.fail[route] = failure{status, times}
	h.mu.Unlock()
}

// seen returns the requests so far as "METHOD path" and clears the record.
func (h *hub) seen() []request {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.requests
	h.requests = nil
	return r
}

func routes(rs []request) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.String()
	}
	return out
}

// stubRunner answers for restic and counts its calls.
type stubRunner struct {
	backup Outcome
	snaps  []SnapshotInput
	calls  []string
}

func (s *stubRunner) Backup(context.Context, []string, []string) Outcome {
	s.calls = append(s.calls, "backup")
	return s.backup
}
func (s *stubRunner) Cleanup(context.Context, int) Outcome {
	s.calls = append(s.calls, "cleanup")
	return Outcome{Status: StatusCompleted}
}
func (s *stubRunner) Check(context.Context) Outcome {
	s.calls = append(s.calls, "check")
	return Outcome{Status: StatusCompleted}
}
func (s *stubRunner) Snapshots(context.Context) ([]SnapshotInput, error) { return s.snaps, nil }

const (
	m1         = "/v1/machines/m1/backups/"
	resticID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resticID01 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func testAgent(t *testing.T, h *hub) (*Agent, *stubRunner) {
	t.Helper()
	size := int64(42)
	stub := &stubRunner{
		backup: Outcome{Status: StatusCompleted, ResticID: resticID},
		snaps: []SnapshotInput{
			{ResticID: resticID01, SizeBytes: &size, Paths: []string{"/"}, CreatedAt: time.Unix(1000, 0).UTC()},
			{ResticID: resticID, SizeBytes: &size, Paths: []string{"/"}, CreatedAt: time.Unix(2000, 0).UTC()},
		},
	}
	a := NewAgent(h.client(), "m1", t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.Paths = []string{"/"}
	a.newRunner = func(Settings) (Runner, error) { return stub, nil }
	now := time.Date(2026, 11, 14, 4, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	return a, stub
}

// Right after "tender install agent" the destination's keys aren't saved yet. The agent waits for
// them instead of recording a failed backup, and backs up as soon as they're there.
func TestWaitsForCredentials(t *testing.T) {
	h := newHub(t)
	a, stub := testAgent(t, h)
	ctx := context.Background()
	a.refreshSettings(ctx)
	h.seen()

	a.CredentialsDir = t.TempDir()
	a.newRunner = a.newRestic // the real one, which reads the credential files
	a.runDue(ctx)
	a.runDue(ctx)
	if got := routes(h.seen()); len(got) != 0 || len(stub.calls) != 0 {
		t.Fatalf("without credentials: requests %v, restic calls %v; want none", got, stub.calls)
	}
	st, err := loadState(filepath.Join(a.StateDir, "agent-state.json"))
	if err != nil || !st.LastBackup.IsZero() {
		t.Fatalf("last backup = %v, %v; want still due", st.LastBackup, err)
	}

	a.newRunner = func(Settings) (Runner, error) { return stub, nil } // the keys are saved now
	a.runDue(ctx)
	if !strings.HasPrefix(strings.Join(stub.calls, ","), "backup") {
		t.Errorf("after the keys arrived, restic calls = %v, want a backup", stub.calls)
	}
}

func TestOnlineFlow(t *testing.T) {
	h := newHub(t)
	a, stub := testAgent(t, h)
	ctx := context.Background()

	a.refreshSettings(ctx)
	if a.settings == nil || a.settings.RetentionDays != 30 {
		t.Fatalf("settings = %+v", a.settings)
	}
	if got := routes(h.seen()); len(got) != 1 || got[0] != "GET "+m1+"settings" {
		t.Fatalf("settings requests = %v", got)
	}

	a.runDue(ctx)
	reqs := h.seen()
	want := []string{
		"POST " + m1 + "history", "PUT " + m1 + "snapshots", "PATCH " + m1 + "history/1", // backup
		"POST " + m1 + "history", "PUT " + m1 + "snapshots", "PATCH " + m1 + "history/2", // cleanup
		"POST " + m1 + "history", "PATCH " + m1 + "history/3", // check
	}
	if got := routes(reqs); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Join(stub.calls, ",") != "backup,cleanup,check" {
		t.Errorf("restic calls = %v", stub.calls)
	}

	var start HistoryInput
	_ = json.Unmarshal(reqs[0].Body, &start)
	if start.Type != TypeBackup || start.Status != StatusInProgress || start.Error != nil || start.SnapshotID != nil || start.CreatedAt.IsZero() {
		t.Errorf("start = %+v", start)
	}
	var put []SnapshotInput
	_ = json.Unmarshal(reqs[1].Body, &put)
	if len(put) != 2 || put[1].ResticID != resticID {
		t.Errorf("snapshot sync body = %s", reqs[1].Body)
	}
	var patch HistoryPatch
	_ = json.Unmarshal(reqs[2].Body, &patch)
	if patch.Status == nil || *patch.Status != StatusCompleted || patch.SnapshotID == nil || *patch.SnapshotID != 101 {
		t.Errorf("patch = %s, want completed with snapshot_id 101 (the hub id of %s)", reqs[2].Body, resticID)
	}
	if reqs[0].Key == "" || reqs[2].Key == "" || reqs[0].Key == reqs[2].Key {
		t.Errorf("keys: post %q patch %q", reqs[0].Key, reqs[2].Key)
	}
	if reqs[1].Key != "" {
		t.Errorf("PUT carries key %q; a full replace is idempotent already", reqs[1].Key)
	}

	// The run is recorded, so nothing is owed until the next scheduled time.
	h.seen()
	a.runDue(ctx)
	if got := h.seen(); len(got) != 0 {
		t.Errorf("second runDue sent %v", routes(got))
	}
	if names, _ := a.queue.names(); len(names) != 0 {
		t.Errorf("queue = %v, want empty", names)
	}
}

func TestOfflineFlowQueuesThenReplays(t *testing.T) {
	h := newHub(t)
	a, _ := testAgent(t, h)
	ctx := context.Background()
	a.refreshSettings(ctx)
	h.seen()

	h.setDown(true)
	a.runDue(ctx)
	startReqs := h.seen()
	for _, r := range startReqs {
		if r.Method == "POST" && !strings.HasSuffix(r.Path, "/history") {
			t.Errorf("unexpected %s", r)
		}
	}
	// The start POST is tried twice with one key.
	if len(startReqs) < 2 || startReqs[0].Key == "" || startReqs[0].Key != startReqs[1].Key {
		t.Fatalf("start attempts: %+v", startReqs)
	}
	names, err := a.queue.names()
	if err != nil || len(names) != 3 {
		t.Fatalf("queue = %v, %v; want the backup, cleanup and check runs", names, err)
	}
	first, err := a.queue.read(names[0])
	if err != nil {
		t.Fatal(err)
	}
	if first.Type != TypeBackup || first.Status != StatusCompleted || first.ResticID != resticID || first.HistoryID != nil ||
		first.PostKey == "" || first.PatchKey == "" || first.CreatedAt.IsZero() {
		t.Errorf("queued run = %+v", first)
	}

	// Still down: replay keeps everything and stops at the first failure.
	a.replay(ctx)
	h.seen()
	if names, _ := a.queue.names(); len(names) != 3 {
		t.Fatalf("queue after a failed replay = %v", names)
	}

	// The hub answers the final POST of the backup run with a 503 once, then recovers.
	h.setDown(false)
	h.failNext("POST "+m1+"history", 503, 1)
	a.replay(ctx)
	failed := h.seen()
	if names, _ := a.queue.names(); len(names) != 3 {
		t.Fatalf("a retryable failure must keep the queue, got %v", names)
	}
	a.replay(ctx)
	replayed := h.seen()
	if names, _ := a.queue.names(); len(names) != 0 {
		t.Fatalf("queue after replay = %v, want empty", names)
	}

	var post1, post2 request
	for _, r := range failed {
		if r.String() == "POST "+m1+"history" {
			post1 = r
		}
	}
	for _, r := range replayed {
		if r.String() == "POST "+m1+"history" {
			post2 = r
			break
		}
	}
	if post1.Key == "" || post1.Key != post2.Key || post1.Key != first.PostKey {
		t.Errorf("idempotency keys across the retry: %q then %q, queued %q", post1.Key, post2.Key, first.PostKey)
	}
	var final HistoryInput
	_ = json.Unmarshal(post2.Body, &final)
	if final.Type != TypeBackup || final.Status != StatusCompleted || final.SnapshotID == nil || *final.SnapshotID != 101 || !final.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("final run = %s", post2.Body)
	}
	if got := routes(replayed); got[0] != "PUT "+m1+"snapshots" || got[1] != "POST "+m1+"history" {
		t.Errorf("replay order = %v", got)
	}
}

func TestFinishedOnlineRunWithLostPatchIsQueuedAndPatchedLater(t *testing.T) {
	h := newHub(t)
	a, _ := testAgent(t, h)
	ctx := context.Background()
	a.refreshSettings(ctx)
	h.seen()

	h.failNext("PATCH "+m1+"history/1", 503, 1)
	a.runDue(ctx)
	failed := h.seen()
	names, _ := a.queue.names()
	if len(names) != 1 {
		t.Fatalf("queue = %v, want the backup's PATCH", names)
	}
	item, _ := a.queue.read(names[0])
	if item.HistoryID == nil || *item.HistoryID != 1 {
		t.Fatalf("queued run = %+v, want history id 1", item)
	}

	a.replay(ctx)
	var patches []request
	for _, r := range append(failed, h.seen()...) {
		if r.Method == "PATCH" && strings.HasSuffix(r.Path, "/history/1") {
			patches = append(patches, r)
		}
	}
	if len(patches) != 2 || patches[0].Key == "" || patches[0].Key != patches[1].Key {
		t.Errorf("PATCH attempts = %+v, want two with one key", patches)
	}
	if names, _ := a.queue.names(); len(names) != 0 {
		t.Errorf("queue = %v, want empty", names)
	}
}

func TestPermanentRejectionIsDropped(t *testing.T) {
	h := newHub(t)
	a, _ := testAgent(t, h)
	ctx := context.Background()
	run := &agentRun{Type: TypeBackup, Status: StatusFailed, Error: "boom", CreatedAt: a.now(), PostKey: "k1", PatchKey: "k2"}
	if err := a.queue.add(run, a.now()); err != nil {
		t.Fatal(err)
	}
	if err := a.queue.add(run, a.now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	h.failNext("POST "+m1+"history", 422, 1) // only the first is rejected
	a.replay(ctx)
	if names, _ := a.queue.names(); len(names) != 0 {
		t.Errorf("queue = %v, want the rejected item dropped and the other sent", names)
	}
	if got := h.seen(); len(got) != 2 {
		t.Errorf("requests = %v, want 2 POSTs", routes(got))
	}
}

func TestSettingsFallBackToSavedCopy(t *testing.T) {
	h := newHub(t)
	a, _ := testAgent(t, h)
	ctx := context.Background()

	h.mu.Lock()
	h.settings = nil
	h.mu.Unlock()
	a.refreshSettings(ctx)
	if a.settings != nil {
		t.Fatal("a 404 must leave the agent without settings")
	}

	h.mu.Lock()
	h.settings = &Settings{Destination: Destination{Name: "r2-test"}, Schedule: "0 1 * * *", RetentionDays: 14}
	h.mu.Unlock()
	a.refreshSettings(ctx)
	if a.settings == nil || a.settings.RetentionDays != 14 {
		t.Fatalf("settings = %+v", a.settings)
	}

	// A new agent starts while the hub is down and loads the saved copy.
	h.setDown(true)
	b := NewAgent(h.client(), "m1", a.StateDir, a.Log)
	b.loadSavedSettings()
	b.refreshSettings(ctx)
	if b.settings == nil || b.settings.RetentionDays != 14 || b.settings.Schedule != "0 1 * * *" {
		t.Errorf("settings after a restart with the hub down = %+v", b.settings)
	}

	// An unusable schedule from the hub doesn't replace a good copy.
	h.setDown(false)
	h.mu.Lock()
	h.settings = &Settings{Destination: Destination{Name: "r2-test"}, Schedule: "nonsense", RetentionDays: 1}
	h.mu.Unlock()
	b.refreshSettings(ctx)
	if b.settings.RetentionDays != 14 {
		t.Errorf("bad settings replaced the good ones: %+v", b.settings)
	}
}

func TestFailedBackupIsReportedAndSkipsCleanup(t *testing.T) {
	h := newHub(t)
	a, stub := testAgent(t, h)
	ctx := context.Background()
	a.refreshSettings(ctx)
	h.seen()
	stub.backup = Outcome{Status: StatusFailed, Error: "Fatal: wrong password"}
	a.runDue(ctx)
	reqs := h.seen()
	var patch HistoryPatch
	for _, r := range reqs {
		if r.Method == "PATCH" {
			_ = json.Unmarshal(r.Body, &patch)
		}
	}
	if patch.Status == nil || *patch.Status != StatusFailed || patch.Error == nil || *patch.Error != "Fatal: wrong password" || patch.SnapshotID != nil {
		t.Errorf("patch = %+v", patch)
	}
	if strings.Join(stub.calls, ",") != "backup" {
		t.Errorf("restic calls = %v, want only backup", stub.calls)
	}
}

// stopDuringBackup stops the agent while restic is backing up, like a SIGTERM would.
type stopDuringBackup struct {
	*stubRunner
	stop func()
}

func (s stopDuringBackup) Backup(ctx context.Context, paths, exclude []string) Outcome {
	s.stop()
	return s.stubRunner.Backup(ctx, paths, exclude)
}

func TestStoppedRunIsReportedAndRunsAgainOnNextStart(t *testing.T) {
	h := newHub(t)
	a, stub := testAgent(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	a.refreshSettings(ctx)
	h.seen()
	stub.backup = Outcome{Status: StatusFailed, Error: "Fatal: unable to save snapshot: context canceled"}
	stopping := stopDuringBackup{stubRunner: stub, stop: cancel} // SIGTERM arrives during the run
	a.newRunner = func(Settings) (Runner, error) { return stopping, nil }
	a.runDue(ctx)

	patched := false
	for _, r := range h.seen() {
		patched = patched || r.Method == "PATCH"
	}
	if !patched {
		t.Error("a stopped run must still be reported")
	}
	if st, _ := loadState(a.StateDir + "/agent-state.json"); !st.LastBackup.IsZero() {
		t.Errorf("a stopped run must not count as the last backup: %+v", st)
	}
}
