package logs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/logs/sinks"
)

// fakeHub serves a machine's sinks and keeps its status reports.
type fakeHub struct {
	mu       sync.Mutex
	sinks    []DaemonSink
	down     bool
	notFound bool // answers the sinks route with 404, as a hub before log sinks
	reports  []StatusReport
}

func (h *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if h.notFound {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/machines/apps/logs/sinks":
		_ = json.NewEncoder(w).Encode(map[string]any{"data": h.sinks})
	case r.Method == "PUT" && r.URL.Path == "/v1/machines/apps/logs/status":
		var rep StatusReport
		_ = json.NewDecoder(r.Body).Decode(&rep)
		h.reports = append(h.reports, rep)
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

func (h *fakeHub) set(f func(h *fakeHub)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(h)
}

func (h *fakeHub) lastReport() (StatusReport, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.reports) == 0 {
		return StatusReport{}, false
	}
	return h.reports[len(h.reports)-1], true
}

// webhookReceiver keeps the records of each signed batch it gets.
type webhookReceiver struct {
	mu      sync.Mutex
	records []sinks.Record
}

func (wr *webhookReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var batch sinks.Batch
	_ = json.Unmarshal(b, &batch)
	if !strings.HasPrefix(r.Header.Get("Tender-Signature"), "t=") || batch.Machine != "apps" {
		w.WriteHeader(400)
		return
	}
	wr.mu.Lock()
	wr.records = append(wr.records, batch.Records...)
	wr.mu.Unlock()
	w.WriteHeader(204)
}

func (wr *webhookReceiver) count() int {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	return len(wr.records)
}

func fastDaemon(t *testing.T) {
	fast(t)
	a, b, c := sinksEvery, reportEvery, reportSoon
	sinksEvery, reportEvery, reportSoon = 50*time.Millisecond, 100*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { sinksEvery, reportEvery, reportSoon = a, b, c })
}

// The daemon sends to the hub's sinks and reports on them, keeps the sinks for when the hub is
// down or answers 404, keeps a missing sink's state for a week so it resumes when it is back,
// and deletes it after that.
func TestDaemon(t *testing.T) {
	fastDaemon(t)
	bin, _ := fakeJournalctl(t, false)
	recv := &webhookReceiver{}
	sinkSrv := httptest.NewServer(recv)
	defer sinkSrv.Close()
	hub := &fakeHub{sinks: []DaemonSink{{ID: 7, Name: "hook", Type: "webhook",
		Config:  map[string]any{"url": sinkSrv.URL, "secret": "0123456789abcdef"},
		Sources: Sources{Journal: &JournalSource{Units: []string{}, MinPriority: "debug"}}}}}
	hubSrv := httptest.NewServer(hub)
	defer hubSrv.Close()

	state := t.TempDir()
	var clock sync.Mutex
	later := time.Duration(0) // how far the daemon's clock is ahead
	run := func() (context.CancelFunc, chan struct{}) {
		d := NewDaemon(client.New(hubSrv.URL, "tok"), "apps", state, "/nonexistent.sock", quiet)
		d.Journalctl = bin
		d.now = func() time.Time { clock.Lock(); defer clock.Unlock(); return time.Now().Add(later) }
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { d.Run(ctx); close(done) }()
		return cancel, done
	}
	cancel, done := run()
	eventually(t, "the journal at the webhook", func() bool { return recv.count() == 6 })
	eventually(t, "a report", func() bool {
		r, ok := hub.lastReport()
		return ok && len(r.Data) == 1 && r.Data[0].SinkID == 7 && r.Data[0].RecordsSent == 6
	})
	info, err := os.Stat(filepath.Join(state, "logs", "sinks.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved sinks: %v %v", info, err)
	}
	cancel()
	<-done

	// the hub is down: the daemon goes on from the saved sinks and cursors, sending nothing twice.
	hub.set(func(h *fakeHub) { h.down = true })
	cancel, done = run()
	time.Sleep(300 * time.Millisecond)
	if n := recv.count(); n != 6 {
		t.Errorf("%d records after a restart, want the same 6", n)
	}
	cur, _ := os.ReadFile(filepath.Join(state, "logs", "7", "cursors.json"))
	if !strings.Contains(string(cur), `"journal":"6"`) {
		t.Errorf("cursors %s", cur)
	}

	// a hub that answers 404 for the route is no reason to stop.
	hub.set(func(h *fakeHub) { h.down, h.notFound = false, true })
	time.Sleep(200 * time.Millisecond)
	if b, _ := os.ReadFile(filepath.Join(state, "logs", "sinks.json")); !strings.Contains(string(b), `"hook"`) {
		t.Errorf("after a 404 the saved sinks are %s", b)
	}

	// the hub no longer lists the sink, as when it is disabled: the daemon stops, keeps its state,
	// and resumes from it when the sink is back.
	var sink []DaemonSink
	hub.set(func(h *fakeHub) { sink = h.sinks })
	hub.set(func(h *fakeHub) { h.notFound, h.sinks = false, []DaemonSink{} })
	dir := filepath.Join(state, "logs", strconv.Itoa(7))
	eventually(t, "the sink noted as missing", func() bool {
		_, err := os.Stat(filepath.Join(dir, missingFile))
		return err == nil
	})
	hub.set(func(h *fakeHub) { h.sinks = sink })
	eventually(t, "the sink back", func() bool {
		_, err := os.Stat(filepath.Join(dir, missingFile))
		return os.IsNotExist(err)
	})
	time.Sleep(200 * time.Millisecond)
	if n := recv.count(); n != 6 {
		t.Errorf("%d records after the sink came back, want the same 6: it resumes from its cursor", n)
	}

	// missing for more than a week, its state goes.
	hub.set(func(h *fakeHub) { h.sinks = []DaemonSink{} })
	eventually(t, "the sink noted as missing again", func() bool {
		_, err := os.Stat(filepath.Join(dir, missingFile))
		return err == nil
	})
	if _, err := os.Stat(filepath.Join(dir, "cursors.json")); err != nil {
		t.Errorf("the state of a sink missing for a moment is gone: %v", err)
	}
	clock.Lock()
	later = stateKept + time.Hour
	clock.Unlock()
	eventually(t, "the sink's state gone", func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	})
	cancel()
	<-done
	b, _ := os.ReadFile(filepath.Join(state, "logs", "sinks.json"))
	if strings.TrimSpace(string(b)) != "[]" {
		t.Errorf("saved sinks %s", b)
	}
}

// A sink whose pipeline can't start is reported as failing, with why, so the hub alerts on it.
func TestDaemonReportsAStartFailure(t *testing.T) {
	fastDaemon(t)
	hub := &fakeHub{sinks: []DaemonSink{{ID: 9, Name: "future", Type: "kafka", Config: map[string]any{},
		Sources: Sources{Journal: &JournalSource{Units: []string{}, MinPriority: "info"}}}}}
	hubSrv := httptest.NewServer(hub)
	defer hubSrv.Close()
	d := NewDaemon(client.New(hubSrv.URL, "tok"), "apps", t.TempDir(), "/nonexistent.sock", quiet)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	eventually(t, "a report of the failure", func() bool {
		r, ok := hub.lastReport()
		return ok && len(r.Data) == 1 && r.Data[0].SinkID == 9 && r.Data[0].LastError != nil &&
			strings.Contains(*r.Data[0].LastError, "doesn't know the sink type") && r.Data[0].FailingSince != nil
	})
}
