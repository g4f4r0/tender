package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

func ptr[T any](v T) *T { return &v }

// newDB has machines a (id 1) and b (id 2) and this domain's tables; actions can't import machines.
func newDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = d.Write.Exec(`CREATE TABLE machines (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, token_hash BLOB NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) STRICT;
		INSERT INTO machines VALUES (1, 'a', x'00', 0, 0), (2, 'b', x'00', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background(), Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	return d
}

// fakeServices knows the services web, actions and gone on machine a, and records what finished.
type fakeServices struct {
	mu       sync.Mutex
	finished []string
	fail     error
}

func (f *fakeServices) Check(_ context.Context, machineID int64, service, container string) error {
	switch {
	case machineID != 1 || (service != "web" && service != "actions" && service != "gone"): // gone: the hub knows it, Docker no longer does
		return server.NotFound("Service %q doesn't exist on this machine.", service)
	case container != "" && container != "app":
		return server.NotFound("Container %q isn't in service %s.", container, service)
	}
	return nil
}

func (f *fakeServices) Finished(_ context.Context, _ int64, service, container, typ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	if container != "" {
		service += "/" + container
	}
	f.finished = append(f.finished, typ+" "+service)
	return nil
}

func (f *fakeServices) Deployment(_ context.Context, _ int64, service string) (json.RawMessage, error) {
	if service == "broken" {
		return nil, server.Invalid("Cloudflare refused the tunnel's routes.")
	}
	return json.RawMessage(`{"name":"` + service + `"}`), nil
}

func status(err error) int {
	var e *server.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func TestValidate(t *testing.T) {
	for name, c := range map[string]struct {
		in   Input
		want string
	}{
		"no type":                {Input{}, "type must be"},
		"unknown type":           {Input{Type: "kill"}, "type must be"},
		"start without service":  {Input{Type: TypeStart}, "service is required for start."},
		"stop with lines":        {Input{Type: TypeStop, Service: ptr("web"), Lines: ptr(5)}, "only for a logs action"},
		"restart with since":     {Input{Type: TypeRestart, Service: ptr("web"), Since: ptr("1h")}, "only for a logs action"},
		"service logs with unit": {Input{Type: TypeLogs, Service: ptr("web"), Unit: ptr("nginx")}, "only for the machine's logs"},
		"empty service":          {Input{Type: TypeLogs, Service: ptr("")}, "service can't be empty"},
		"flag as unit":           {Input{Type: TypeLogs, Unit: ptr("--output=cat")}, "unit must be"},
		"glob as unit":           {Input{Type: TypeLogs, Unit: ptr("nginx*")}, "unit must be"},
		"bad priority":           {Input{Type: TypeLogs, Priority: ptr("loud")}, "priority must be one of emerg"},
		"since too long":         {Input{Type: TypeLogs, Since: ptr("8d")}, "since must be"},
		"since not a duration":   {Input{Type: TypeLogs, Since: ptr("yesterday")}, "since must be"},
		"zero lines":             {Input{Type: TypeLogs, Lines: ptr(0)}, "lines must be"},
		"negative lines":         {Input{Type: TypeLogs, Lines: ptr(-5)}, "lines must be"},
		"too many lines":         {Input{Type: TypeLogs, Lines: ptr(2001)}, "lines must be"},
	} {
		err := c.in.validate()
		if status(err) != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want 422 with %q", name, err, c.want)
		}
	}
	for name, in := range map[string]Input{
		"machine logs":      {Type: TypeLogs, Unit: ptr("getty@tty1.service"), Priority: ptr("warning"), Since: ptr("7d"), Lines: ptr(2000)},
		"service logs":      {Type: TypeLogs, Service: ptr("web"), Since: ptr("1h30m")},
		"all machine logs":  {Type: TypeLogs},
		"start a service":   {Type: TypeStart, Service: ptr("web")},
		"escaped unit name": {Type: TypeLogs, Unit: ptr(`dev-disk-by\x2duuid.device`)},
	} {
		if err := in.validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	in := Input{Type: TypeLogs}
	if err := in.validate(); err != nil || *in.Lines != DefaultLines {
		t.Errorf("default lines = %v, %v", in.Lines, err)
	}
}

func TestParseSince(t *testing.T) {
	for in, want := range map[string]time.Duration{"30m": 30 * time.Minute, "1h": time.Hour, "1h30m": 90 * time.Minute, "7d": MaxSince, "168h": MaxSince} {
		if got, err := ParseSince(in); err != nil || got != want {
			t.Errorf("%s = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0s", "-1h", "0d", "8d", "169h", "1w", "d"} {
		if _, err := ParseSince(in); err == nil {
			t.Errorf("%q parsed", in)
		}
	}
}

func TestStore(t *testing.T) {
	d := newDB(t)
	svc := &fakeServices{}
	s := NewStore(d, svc)
	ctx := context.Background()

	if _, err := s.Create(ctx, 1, Input{Type: TypeStart, Service: ptr("nope")}); status(err) != http.StatusNotFound {
		t.Errorf("unknown service: %v", err)
	}
	logs, err := s.Create(ctx, 1, Input{Type: TypeLogs, Service: ptr("web")})
	if err != nil || logs.Status != StatusPending || *logs.Lines != DefaultLines || *logs.Service != "web" || logs.Machine != "a" {
		t.Fatalf("logs action = %+v, %v", logs, err)
	}
	stop, _ := s.Create(ctx, 1, Input{Type: TypeStop, Service: ptr("web")})
	journal, _ := s.Create(ctx, 1, Input{Type: TypeLogs, Unit: ptr("tenderd"), Priority: ptr("warning"), Since: ptr("1h")})
	if journal.Service != nil || *journal.Unit != "tenderd" || *journal.Since != "1h" {
		t.Errorf("journal action = %+v", journal)
	}

	// Another machine's daemon sees none of them; this one's gets them one at a time, oldest first.
	if list, err := s.Take(ctx, 2); err != nil || len(list) != 0 {
		t.Errorf("machine b took %+v, %v", list, err)
	}
	for _, want := range []int64{logs.ID, stop.ID, journal.ID} {
		list, err := s.Take(ctx, 1)
		if err != nil || len(list) != 1 || list[0].ID != want || list[0].Status != StatusPending {
			t.Fatalf("take = %+v, %v; want only action %d, still pending", list, err, want)
		}
		if err := s.Claim(ctx, 1, want); err != nil {
			t.Fatal(err)
		}
	}
	if again, _ := s.Take(ctx, 1); len(again) != 0 {
		t.Errorf("taken twice: %+v", again)
	}

	if err := s.Finish(ctx, 1, logs.ID, Patch{Status: "done"}); status(err) != http.StatusUnprocessableEntity {
		t.Errorf("bad status: %v", err)
	}
	if err := s.Finish(ctx, 1, logs.ID, Patch{Status: StatusCompleted, Output: ptr("hello\n")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, 1, logs.ID, Patch{Status: StatusFailed}); status(err) != http.StatusConflict {
		t.Errorf("finished twice: %v", err)
	}
	if err := s.Finish(ctx, 2, stop.ID, Patch{Status: StatusCompleted}); status(err) != http.StatusNotFound {
		t.Errorf("another machine's action: %v", err)
	}
	// The services domain hears about a completed stop first; when that fails, the stop stays in_progress.
	svc.fail = errors.New("database is locked")
	if err := s.Finish(ctx, 1, stop.ID, Patch{Status: StatusCompleted}); err == nil {
		t.Error("finished though the services domain didn't hear about it")
	}
	svc.fail = nil
	if err := s.Finish(ctx, 1, stop.ID, Patch{Status: StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, 1, journal.ID, Patch{Status: StatusCompleted, Output: ptr("entry\n")}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(svc.finished, []string{"stop web"}) {
		t.Errorf("the services domain heard %q", svc.finished)
	}
	if got, err := s.Get(ctx, 1, logs.ID); err != nil || got.Status != StatusCompleted || *got.Output != "hello\n" {
		t.Errorf("logs = %+v, %v", got, err)
	}

	// The list is newest first, without output.
	list, err := s.List(ctx, 1)
	if err != nil || len(list) != 3 || list[0].ID != journal.ID {
		t.Errorf("list = %+v, %v", list, err)
	}

	// Await gives up after its timeout, with the action as it is.
	start, _ := s.Create(ctx, 1, Input{Type: TypeStart, Service: ptr("web")})
	if got, err := s.Await(ctx, 1, start.ID, 10*time.Millisecond); err != nil || got.Status != StatusPending {
		t.Errorf("await = %+v, %v", got, err)
	}

	// Old actions fail: one never picked up, one never answered.
	if err := s.Claim(ctx, 1, start.ID); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.Create(ctx, 1, Input{Type: TypeRestart, Service: ptr("web")})
	old := time.Now().Add(-16 * time.Minute).Unix()
	if _, err := d.Write.Exec(`UPDATE actions SET created_at = ?, updated_at = ? WHERE id IN (?, ?)`, old, old, pending.ID, start.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, 1, pending.ID); got.Status != StatusFailed || *got.Output != notPickedUp {
		t.Errorf("never picked up = %+v", got)
	}
	if got, _ := s.Get(ctx, 1, start.ID); got.Status != StatusFailed || *got.Output != notAnswered {
		t.Errorf("never answered = %+v", got)
	}

	// The trim never deletes an action that is still waiting; TestTrimKeepsUnfinished trims finished ones.
	for range 110 {
		if _, err := s.Create(ctx, 1, Input{Type: TypeStart, Service: ptr("web")}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := d.Read.QueryRow(`SELECT count(*) FROM actions WHERE machine_id = 1 AND status = 'pending'`).Scan(&n); err != nil || n != 110 {
		t.Errorf("kept %d waiting actions, %v; want all 110", n, err)
	}
}

// A logs action's output, service or machine, is cleared Keep after it finished; the row stays.
func TestOutputCleared(t *testing.T) {
	d := newDB(t)
	s := NewStore(d, &fakeServices{})
	ctx := context.Background()
	var ids []int64
	for _, in := range []Input{{Type: TypeLogs, Service: ptr("web")}, {Type: TypeLogs}, {Type: TypeRestart, Service: ptr("web")}} {
		a, err := s.Create(ctx, 1, in)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Claim(ctx, 1, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, 1, a.ID, Patch{Status: StatusCompleted, Output: ptr("password=hunter2\n")}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	if got, _ := s.Get(ctx, 1, ids[0]); got.Output == nil || got.OutputCleared {
		t.Fatalf("cleared at once: %+v", got)
	}
	old := time.Now().Add(-2 * time.Hour).Unix()
	if _, err := d.Write.Exec(`UPDATE actions SET updated_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[:2] {
		if got, err := s.Get(ctx, 1, id); err != nil || got.Output != nil || !got.OutputCleared || got.Status != StatusCompleted {
			t.Errorf("logs action %d after an hour = %+v, %v", id, got, err)
		}
	}
	if got, _ := s.Get(ctx, 1, ids[2]); got.OutputCleared || got.Output == nil {
		t.Errorf("a restart's output was cleared: %+v", got)
	}
	// A logs action that failed holds why, not logs: that stays, so it still says what happened.
	never, _ := s.Create(ctx, 1, Input{Type: TypeLogs})
	if _, err := d.Write.Exec(`UPDATE actions SET created_at = ?, updated_at = ? WHERE id = ?`, old-3600, old-3600, never.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, 1, never.ID); got.Status != StatusFailed {
		t.Fatalf("a logs action that waited too long = %+v", got)
	}
	if _, err := d.Write.Exec(`UPDATE actions SET updated_at = ? WHERE id = ?`, old, never.ID); err != nil { // an hour after it expired
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, 1, never.ID); got.Status != StatusFailed || deref(got.Output) != notPickedUp || got.OutputCleared {
		t.Errorf("a logs action that expired unread, an hour later = %+v", got)
	}

	// The sweep clears them when nobody reads them, on every machine.
	s.Keep = time.Millisecond
	b, _ := s.Create(ctx, 2, Input{Type: TypeLogs})
	if err := s.Claim(ctx, 2, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, 2, b.ID, Patch{Status: StatusCompleted, Output: ptr("x\n")}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec(`UPDATE actions SET updated_at = ? WHERE id = ?`, old, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.tidy(ctx, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	var cleared bool
	if err := d.Read.QueryRow(`SELECT output_cleared FROM actions WHERE id = ?`, b.ID).Scan(&cleared); err != nil || !cleared {
		t.Errorf("the sweep left machine b's logs: %v, %v", cleared, err)
	}
}

func TestWait(t *testing.T) {
	s := NewStore(newDB(t), &fakeServices{})
	ctx := context.Background()

	// Nothing pending: it waits the whole time, then answers an empty list.
	start := time.Now()
	if list, err := s.Wait(ctx, 1, 700*time.Millisecond, nil); err != nil || len(list) != 0 || time.Since(start) < 700*time.Millisecond {
		t.Errorf("empty wait = %+v, %v after %s", list, err, time.Since(start))
	}

	// An action queued meanwhile, from any process, answers within a poll.
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = NewStore(s.db, &fakeServices{}).Create(ctx, 1, Input{Type: TypeRestart, Service: ptr("web")})
	}()
	start = time.Now()
	list, err := s.Wait(ctx, 1, 10*time.Second, nil)
	if err != nil || len(list) != 1 || time.Since(start) > 2*time.Second {
		t.Errorf("wait = %+v, %v after %s", list, err, time.Since(start))
	}

	// Waiting with nothing to do only reads: it works while something else holds the write lock.
	queued, err := s.Create(ctx, 1, Input{Type: TypeStart, Service: ptr("web")})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE actions SET updated_at = updated_at`); err != nil {
		t.Fatal(err)
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if a, err := s.Await(lctx, 1, queued.ID, 300*time.Millisecond); err != nil || a.Status != StatusPending {
		t.Errorf("await while the write lock is held = %+v, %v", a, err)
	}
	cancel()
	_ = tx.Rollback()
	if _, err := s.Take(ctx, 1); err != nil { // nothing pending for the wait below
		t.Fatal(err)
	}
	tx, err = s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	lctx, cancel = context.WithTimeout(ctx, 3*time.Second)
	start = time.Now()
	if list, err := s.Wait(lctx, 1, 300*time.Millisecond, nil); err != nil || len(list) != 0 || time.Since(start) > 2*time.Second {
		t.Errorf("empty wait while the write lock is held = %+v, %v after %s", list, err, time.Since(start))
	}
	cancel()
	_ = tx.Rollback()

	// The hub shutting down ends the wait at once.
	stop := make(chan struct{})
	close(stop)
	start = time.Now()
	if _, err := s.Wait(ctx, 1, 10*time.Second, stop); err != nil || time.Since(start) > time.Second {
		t.Errorf("wait while stopping: %v after %s", err, time.Since(start))
	}
}

// The keep-100 trim deletes only finished actions: one still waiting for the daemon, or running,
// stays however many finished ones come after it.
func TestTrimKeepsUnfinished(t *testing.T) {
	d := newDB(t)
	s := NewStore(d, &fakeServices{})
	ctx := context.Background()
	waiting, err := s.Create(ctx, 1, Input{Type: TypeRestart, Service: ptr("web")})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for range 120 {
		if _, err := d.Write.Exec(`INSERT INTO actions (machine_id, type, service, status, created_at, updated_at) VALUES (1, 'start', 'web', 'completed', ?, ?)`,
			now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(ctx, 1, Input{Type: TypeStart, Service: ptr("web")}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, 1, waiting.ID); err != nil || got.Status != StatusPending {
		t.Errorf("the waiting action after the trim = %+v, %v", got, err)
	}
	var finished int
	if err := d.Read.QueryRow(`SELECT count(*) FROM actions WHERE machine_id = 1 AND status = 'completed'`).Scan(&finished); err != nil || finished != kept {
		t.Errorf("kept %d finished actions, %v; want %d", finished, err, kept)
	}
}

// The list reads no output: it is the summary of each action, and the output can be 256 KiB each.
func TestListReadsNoOutput(t *testing.T) {
	sel := strings.SplitN(selectSummaries, "FROM", 2)[0]
	for _, col := range strings.Split(sel, ",") {
		if strings.TrimSpace(col) == "a.output" {
			t.Errorf("the list selects a.output: %s", sel)
		}
	}
}

// An action the daemon was handed but didn't claim is handed out again after 30 s; meanwhile the
// next one is. A claim moves it from pending to in_progress once.
func TestClaim(t *testing.T) {
	d := newDB(t)
	s := NewStore(d, &fakeServices{})
	ctx := context.Background()
	a, _ := s.Create(ctx, 1, Input{Type: TypeRestart, Service: ptr("web")})
	b, _ := s.Create(ctx, 1, Input{Type: TypeStop, Service: ptr("web")})
	for _, want := range []int64{a.ID, b.ID} {
		if list, err := s.Take(ctx, 1); err != nil || len(list) != 1 || list[0].ID != want {
			t.Fatalf("take = %+v, %v; want %d", list, err, want)
		}
	}
	if list, _ := s.Take(ctx, 1); len(list) != 0 {
		t.Errorf("handed out again at once: %+v", list)
	}
	if list, err := s.Wait(ctx, 1, 600*time.Millisecond, nil); err != nil || len(list) != 0 {
		t.Errorf("wait handed out again at once: %+v, %v", list, err)
	}
	if _, err := d.Write.Exec(`UPDATE actions SET handed_out_at = handed_out_at - 30 WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := s.Wait(ctx, 1, time.Second, nil); err != nil || len(list) != 1 || list[0].ID != a.ID || list[0].Status != StatusPending {
		t.Fatalf("after 30 s unclaimed = %+v, %v; want %d again", list, err, a.ID)
	}
	if err := s.Claim(ctx, 1, a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, 1, a.ID); got.Status != StatusInProgress {
		t.Errorf("claimed = %+v", got)
	}
	if err := s.Claim(ctx, 1, a.ID); status(err) != http.StatusConflict {
		t.Errorf("claimed twice: %v", err)
	}
	if err := s.Claim(ctx, 2, b.ID); status(err) != http.StatusNotFound {
		t.Errorf("another machine claimed it: %v", err)
	}
	if err := s.Finish(ctx, 1, b.ID, Patch{Status: StatusCompleted}); status(err) != http.StatusConflict {
		t.Errorf("finished without a claim: %v", err)
	}
	if _, err := d.Write.Exec(`UPDATE actions SET handed_out_at = handed_out_at - 30, updated_at = updated_at - 30 WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Take(ctx, 1); len(list) != 0 {
		t.Errorf("a claimed action was handed out again: %+v", list)
	}
}

// A report that comes after the hub gave up on the action replaces "did not say how it ended",
// and a completed stop still marks the service stopped by Tender. Nothing else is replaced.
func TestLateFinish(t *testing.T) {
	d := newDB(t)
	svc := &fakeServices{}
	s := NewStore(d, svc)
	ctx := context.Background()
	stop, _ := s.Create(ctx, 1, Input{Type: TypeStop, Service: ptr("web")})
	never, _ := s.Create(ctx, 1, Input{Type: TypeStart, Service: ptr("web")})
	if err := s.Claim(ctx, 1, stop.ID); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-16 * time.Minute).Unix()
	if _, err := d.Write.Exec(`UPDATE actions SET created_at = ?, updated_at = ?`, old, old); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, 1, stop.ID); got.Status != StatusFailed || deref(got.Output) != notAnswered {
		t.Fatalf("expired = %+v", got)
	}
	if err := s.Finish(ctx, 1, stop.ID, Patch{Status: StatusCompleted}); err != nil {
		t.Fatalf("late report: %v", err)
	}
	if got, _ := s.Get(ctx, 1, stop.ID); got.Status != StatusCompleted || got.Output != nil {
		t.Errorf("after the late report = %+v", got)
	}
	if !slices.Equal(svc.finished, []string{"stop web"}) {
		t.Errorf("the services domain heard %q", svc.finished)
	}
	if err := s.Finish(ctx, 1, stop.ID, Patch{Status: StatusFailed}); status(err) != http.StatusConflict {
		t.Errorf("a finished action was replaced: %v", err)
	}
	if err := s.Finish(ctx, 1, never.ID, Patch{Status: StatusCompleted}); status(err) != http.StatusConflict {
		t.Errorf("an action never picked up took a report: %v", err)
	}
}

// Clearing a logs action's output leaves none of its bytes in the database's files: not in the
// pages it freed, nor in the WAL. Big output spills to overflow pages, so both sizes are tried.
func TestClearedLogsLeaveNoBytes(t *testing.T) {
	d := newDB(t)
	s := NewStore(d, &fakeServices{})
	ctx := context.Background()
	const marker = "MARKER-JOURNAL-LINE"
	for _, out := range []string{marker + "\n", strings.Repeat(marker+" password=hunter2\n", 12000)} {
		a, err := s.Create(ctx, 1, Input{Type: TypeLogs})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Claim(ctx, 1, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, 1, a.ID, Patch{Status: StatusCompleted, Output: ptr(out)}); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(dbPath(t, d)), "hub.db*"))
	if !contains(t, files, marker) {
		t.Fatalf("the marker never reached %q", files)
	}
	old := time.Now().Add(-2 * time.Hour).Unix()
	if _, err := d.Write.Exec(`UPDATE actions SET updated_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	if err := s.tidy(ctx, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	files, _ = filepath.Glob(filepath.Join(filepath.Dir(dbPath(t, d)), "hub.db*"))
	if contains(t, files, marker) {
		t.Errorf("cleared logs are still in %q", files)
	}
}

func dbPath(t *testing.T, d *db.DB) string {
	t.Helper()
	var seq int
	var name, file string
	if err := d.Read.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func contains(t *testing.T, files []string, s string) bool {
	t.Helper()
	found := false
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(s)) {
			t.Logf("%s holds %q", filepath.Base(f), s)
			found = true
		}
	}
	return found
}
