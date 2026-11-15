package backups_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

var base = time.Date(2026, 11, 14, 3, 30, 0, 0, time.UTC)

// testKey is the hub's encryption key in these tests: 32 bytes, 0 to 31.
var testKey = func() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}()

// identity knows machines a (id 1, token tok-a) and b (id 2, token tok-b); backups can't import machines.
type identity struct{ d *db.DB }

func (i identity) Authenticate(_ context.Context, token string) (int64, string, error) {
	switch token {
	case "tok-a":
		return 1, "a", nil
	case "tok-b":
		return 2, "b", nil
	}
	return 0, "", server.NotFound("no such token")
}

func (i identity) Lookup(ctx context.Context, name string) (int64, error) {
	var id int64
	if err := i.d.Read.QueryRowContext(ctx, `SELECT id FROM machines WHERE name = ?`, name).Scan(&id); err != nil {
		return 0, server.NotFound("no machine %q", name)
	}
	return id, nil
}

type hub struct {
	t     *testing.T
	db    *db.DB
	store *backups.Store
	srv   *httptest.Server
}

func newHub(t *testing.T) *hub {
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
	// The machines domain's table, as far as backups needs it. c has no backup data at all.
	_, err = d.Write.Exec(`CREATE TABLE machines (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, token_hash BLOB NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) STRICT;
		INSERT INTO machines VALUES (1, 'a', x'00', 0, 0), (2, 'b', x'00', 0, 0), (3, 'c', x'00', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background(), backups.Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	store := backups.NewStore(d, testKey)
	s := server.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), identity{d})
	store.Routes(s)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return &hub{t: t, db: d, store: store, srv: srv}
}

// do sends a request as the reader, or as the daemon when token is set, and decodes the response into out.
func (h *hub) do(method, path, token string, body any, out any, headers ...string) *http.Response {
	h.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			h.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, h.srv.URL+path, &buf)
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Set("Tailscale-User-Login", "johan@example.com")
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			h.t.Error(err)
		}
	}()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			h.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return res
}

func (h *hub) status(want int, method, path, token string, body any) {
	h.t.Helper()
	if res := h.do(method, path, token, body, nil); res.StatusCode != want {
		h.t.Errorf("%s %s: %d, want %d", method, path, res.StatusCode, want)
	}
}

// setup adds destination r2-code and gives machine a (id 1) settings.
func (h *hub) setup() {
	h.t.Helper()
	ctx := context.Background()
	_, err := h.store.AddDestination(ctx, input("r2-code", "https://x.r2.cloudflarestorage.com", "code", ""))
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.store.SetSettings(ctx, 1, "r2-code", "30 3 * * *", 30); err != nil {
		h.t.Fatal(err)
	}
}

// input is a destination with keys and a password.
func input(name, endpoint, bucket, path string) backups.DestinationInput {
	return backups.DestinationInput{Name: name, Type: "s3", Password: "repo-password", Config: map[string]any{
		"endpoint": endpoint, "bucket": bucket, "path": path, "access_key_id": "key-id", "secret_access_key": "key-secret"}}
}

func snap(id string, minutes int, size int64) backups.SnapshotInput {
	return backups.SnapshotInput{ResticID: id, SizeBytes: &size, Paths: []string{"/", "/boot"}, CreatedAt: base.Add(time.Duration(minutes) * time.Minute)}
}

func (h *hub) put(token, machine string, in []backups.SnapshotInput) server.List[backups.Snapshot] {
	h.t.Helper()
	var out server.List[backups.Snapshot]
	if res := h.do("PUT", "/v1/machines/"+machine+"/backups/snapshots", token, in, &out); res.StatusCode != 200 {
		h.t.Fatalf("PUT snapshots: %d", res.StatusCode)
	}
	return out
}

func (h *hub) run(token, machine string, in backups.HistoryInput) backups.History {
	h.t.Helper()
	var out backups.History
	if res := h.do("POST", "/v1/machines/"+machine+"/backups/history", token, in, &out); res.StatusCode != 201 {
		h.t.Fatalf("POST history %+v: %d", in, res.StatusCode)
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func TestDestinations(t *testing.T) {
	h := newHub(t)
	ctx := context.Background()
	var e *server.Error

	created, err := h.store.AddDestination(ctx, input("r2-code", "https://acct.r2.cloudflarestorage.com", "code", "/hosts/"))
	d := created.Destination
	if err != nil || d.Config["path"] != "hosts" || d.Type != "s3" || d.Location != "s3:https://acct.r2.cloudflarestorage.com/code/hosts" {
		t.Fatalf("AddDestination = %+v, %v", created, err)
	}
	if created.Password != "" {
		t.Errorf("a password the owner gave came back: %q", created.Password)
	}
	for name, bad := range map[string]backups.DestinationInput{
		"same name":     input("r2-code", "https://other.example.com", "bkt", ""),
		"same location": input("other", "https://acct.r2.cloudflarestorage.com", "code", "hosts"),
	} {
		if _, err := h.store.AddDestination(ctx, bad); !errors.As(err, &e) || e.Status != 409 {
			t.Errorf("%s: %v, want 409", name, err)
		}
	}
	noKeyID, noSecret, badType := input("x", "https://x.example.com", "bkt", ""), input("x", "https://x.example.com", "bkt", ""), input("x", "https://x.example.com", "bkt", "")
	noKeyID.Config["access_key_id"], noSecret.Config["secret_access_key"], badType.Type = "", "", "gcs"
	for name, bad := range map[string]backups.DestinationInput{
		"bad name":      input("R2", "https://x.example.com", "bkt", ""),
		"bad type":      badType,
		"bad endpoint":  input("x", "x.example.com", "bkt", ""),
		"no bucket":     input("x", "https://x.example.com", "", ""),
		"no key id":     noKeyID,
		"no secret key": noSecret,
	} {
		if _, err := h.store.AddDestination(ctx, bad); !errors.As(err, &e) || e.Status != 422 {
			t.Errorf("%s: %v, want 422", name, err)
		}
	}
	if _, err := h.store.AddDestination(ctx, input("b-two", "https://x.example.com", "bkt", "")); err != nil {
		t.Fatal(err)
	}

	var list server.List[backups.Destination]
	h.do("GET", "/v1/backups/destinations", "", nil, &list)
	if len(list.Data) != 2 || list.Data[0].Name != "b-two" || list.Data[1].Name != "r2-code" {
		t.Errorf("destinations = %+v, want b-two, r2-code", list.Data)
	}
	var got backups.Destination
	h.do("GET", "/v1/backups/destinations/r2-code", "", nil, &got)
	if got.ID != d.ID || got.Config["bucket"] != "code" || got.Config["access_key_id"] != "***" || got.Config["secret_access_key_set"] != true {
		t.Errorf("destination by name = %+v", got)
	}
	h.status(404, "GET", "/v1/backups/destinations/nope", "", nil)
	// People routes need no login, and a token on them is not an identity.
	h.status(200, "GET", "/v1/backups/destinations", "tok-a", nil)
}

func TestSettings(t *testing.T) {
	h := newHub(t)
	ctx := context.Background()
	var e *server.Error

	// Nothing set yet: 404 for the daemon and for a reader.
	h.status(404, "GET", "/v1/machines/a/backups/settings", "tok-a", nil)
	h.status(404, "GET", "/v1/machines/a/backups/settings", "", nil)
	h.setup()

	var st backups.Settings
	if res := h.do("GET", "/v1/machines/a/backups/settings", "tok-a", nil, &st); res.StatusCode != 200 ||
		st.Destination.Name != "r2-code" || st.Schedule != "30 3 * * *" || st.RetentionDays != 30 {
		t.Errorf("daemon settings: %d %+v", res.StatusCode, st)
	}
	h.do("GET", "/v1/machines/a/backups/settings", "", nil, &st)
	if st.RetentionDays != 30 {
		t.Errorf("reader settings = %+v", st)
	}
	h.status(403, "GET", "/v1/machines/a/backups/settings", "tok-b", nil)
	h.status(404, "GET", "/v1/machines/zzz/backups/settings", "", nil)
	h.status(404, "GET", "/v1/machines/b/backups/settings", "tok-b", nil)

	// Setting again replaces the settings.
	if _, err := h.store.SetSettings(ctx, 1, "r2-code", "0 4 * * *", 14); err != nil {
		t.Fatal(err)
	}
	h.do("GET", "/v1/machines/a/backups/settings", "tok-a", nil, &st)
	if st.Schedule != "0 4 * * *" || st.RetentionDays != 14 {
		t.Errorf("replaced settings = %+v", st)
	}
	for name, c := range map[string]struct {
		destination, schedule string
		days                  int
	}{
		"bad cron":            {"r2-code", "every day", 30},
		"six fields":          {"r2-code", "0 30 3 * * *", 30},
		"no retention":        {"r2-code", "30 3 * * *", 0},
		"unknown destination": {"nope", "30 3 * * *", 30},
	} {
		if _, err := h.store.SetSettings(ctx, 1, c.destination, c.schedule, c.days); !errors.As(err, &e) || e.Status != 422 {
			t.Errorf("%s: %v, want 422", name, err)
		}
	}
}

func TestReplaceSnapshots(t *testing.T) {
	h := newHub(t)
	h.setup()

	first := h.put("tok-a", "a", []backups.SnapshotInput{snap("s1", 0, 100), snap("s2", 10, 200), snap("s3", 20, 300)})
	if len(first.Data) != 3 || first.NextCursor != "" || first.Data[0].ResticID != "s3" || first.Data[2].ResticID != "s1" {
		t.Fatalf("first sync = %+v, want s3, s2, s1", first)
	}
	if first.Data[0].SizeBytes == nil || *first.Data[0].SizeBytes != 300 || len(first.Data[0].Paths) != 2 {
		t.Errorf("snapshot content = %+v", first.Data[0])
	}
	ids := map[string]int64{}
	for _, s := range first.Data {
		ids[s.ResticID] = s.ID
	}

	// Runs that point at s1 and s2, and a snapshot on machine b that has the same restic id.
	r1 := h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "completed", SnapshotID: ptr(ids["s1"]), CreatedAt: base})
	r2 := h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "completed", SnapshotID: ptr(ids["s2"]), CreatedAt: base.Add(10 * time.Minute)})
	h.put("tok-b", "b", []backups.SnapshotInput{snap("s1", 0, 1)})

	// Second sync: s1 and s3 are gone, s2 changed, s4 is new, s1 on b stays.
	second := h.put("tok-a", "a", []backups.SnapshotInput{snap("s2", 10, 250), snap("s4", 30, 400)})
	if len(second.Data) != 2 || second.Data[0].ResticID != "s4" || second.Data[1].ResticID != "s2" {
		t.Fatalf("second sync = %+v, want s4, s2", second)
	}
	if second.Data[1].ID != ids["s2"] || *second.Data[1].SizeBytes != 250 {
		t.Errorf("s2 after update = %+v, want the same id %d and size 250", second.Data[1], ids["s2"])
	}
	var run backups.History
	h.do("GET", "/v1/machines/a/backups/history/"+strconv.FormatInt(r1.ID, 10), "", nil, &run)
	if run.SnapshotID != nil {
		t.Errorf("run of a deleted snapshot keeps snapshot_id %d, want null", *run.SnapshotID)
	}
	h.do("GET", "/v1/machines/a/backups/history/"+strconv.FormatInt(r2.ID, 10), "", nil, &run)
	if run.SnapshotID == nil || *run.SnapshotID != ids["s2"] {
		t.Errorf("run of a kept snapshot lost its link: %+v", run.SnapshotID)
	}
	var onB server.List[backups.Snapshot]
	h.do("GET", "/v1/machines/b/backups/snapshots", "", nil, &onB)
	if len(onB.Data) != 1 || onB.Data[0].ResticID != "s1" {
		t.Errorf("machine b's snapshots = %+v, want its own s1", onB.Data)
	}

	// An unchanged snapshot keeps its updated_at; an empty list clears the machine.
	third := h.put("tok-a", "a", []backups.SnapshotInput{snap("s2", 10, 250), snap("s4", 30, 400)})
	if !third.Data[1].UpdatedAt.Equal(second.Data[1].UpdatedAt) {
		t.Errorf("unchanged snapshot got a new updated_at")
	}
	if empty := h.put("tok-a", "a", []backups.SnapshotInput{}); empty.Data == nil || len(empty.Data) != 0 {
		t.Errorf("empty sync = %+v, want []", empty)
	}

	// Rejected bodies.
	dup := []backups.SnapshotInput{snap("x", 0, 1), snap("x", 1, 1)}
	noTime := []backups.SnapshotInput{{ResticID: "x", Paths: []string{"/"}}}
	noID := []backups.SnapshotInput{{CreatedAt: base}}
	for _, body := range []any{dup, noTime, noID, json.RawMessage("null"), map[string]any{}} {
		h.status(422, "PUT", "/v1/machines/a/backups/snapshots", "tok-a", body)
	}
	h.status(401, "PUT", "/v1/machines/a/backups/snapshots", "", []backups.SnapshotInput{})
	h.status(403, "PUT", "/v1/machines/a/backups/snapshots", "tok-b", []backups.SnapshotInput{})
	h.status(404, "PUT", "/v1/machines/zzz/backups/snapshots", "tok-a", []backups.SnapshotInput{})
}

func TestSnapshotReads(t *testing.T) {
	h := newHub(t)
	var in []backups.SnapshotInput
	for i := range 5 {
		in = append(in, snap("s"+strconv.Itoa(i), i, int64(i)))
	}
	h.put("tok-a", "a", in)
	one := h.put("tok-b", "b", []backups.SnapshotInput{snap("other", 0, 1)}).Data[0]

	var seen []string
	path := "/v1/machines/a/backups/snapshots?limit=2"
	for range 5 {
		var page server.List[backups.Snapshot]
		h.do("GET", path, "", nil, &page)
		for _, s := range page.Data {
			seen = append(seen, s.ResticID)
		}
		if page.NextCursor == "" {
			break
		}
		path = "/v1/machines/a/backups/snapshots?limit=2&cursor=" + page.NextCursor
	}
	if want := []string{"s4", "s3", "s2", "s1", "s0"}; len(seen) != 5 || seen[0] != want[0] || seen[4] != want[4] {
		t.Errorf("pages gave %v, want %v", seen, want)
	}

	var got backups.Snapshot
	h.do("GET", "/v1/machines/b/backups/snapshots/"+strconv.FormatInt(one.ID, 10), "", nil, &got)
	if got.ResticID != "other" {
		t.Errorf("snapshot by id = %+v", got)
	}
	h.status(404, "GET", "/v1/machines/a/backups/snapshots/"+strconv.FormatInt(one.ID, 10), "", nil) // b's id on a
	h.status(404, "GET", "/v1/machines/a/backups/snapshots/abc", "", nil)
	h.status(200, "GET", "/v1/machines/a/backups/snapshots", "tok-a", nil)
}

func TestHistoryCreate(t *testing.T) {
	h := newHub(t)
	snapA := h.put("tok-a", "a", []backups.SnapshotInput{snap("s1", 0, 1)}).Data[0]
	snapB := h.put("tok-b", "b", []backups.SnapshotInput{snap("s1", 0, 1)}).Data[0]

	// Online start.
	var started backups.History
	res := h.do("POST", "/v1/machines/a/backups/history", "tok-a", backups.HistoryInput{Type: "backup", Status: "in_progress", CreatedAt: base}, &started)
	if res.StatusCode != 201 || res.Header.Get("Location") != "/v1/machines/a/backups/history/"+strconv.FormatInt(started.ID, 10) ||
		started.Status != "in_progress" || !started.CreatedAt.Equal(base) || started.SnapshotID != nil || started.Error != nil {
		t.Errorf("start: %d %q %+v", res.StatusCode, res.Header.Get("Location"), started)
	}
	// Offline: a finished run reported after the fact, failed with an error and completed with a snapshot.
	failed := h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "failed", Error: ptr("repository locked"), CreatedAt: base.Add(time.Hour)})
	done := h.run("tok-a", "a", backups.HistoryInput{Type: "check", Status: "completed", SnapshotID: &snapA.ID, CreatedAt: base.Add(2 * time.Hour)})
	h.run("tok-a", "a", backups.HistoryInput{Type: "cleanup", Status: "pending", CreatedAt: base.Add(3 * time.Hour)})
	if failed.Status != "failed" || *failed.Error != "repository locked" || *done.SnapshotID != snapA.ID {
		t.Errorf("final runs = %+v %+v", failed, done)
	}

	for _, in := range []backups.HistoryInput{
		{Type: "restore", Status: "pending", CreatedAt: base},                                // bad type
		{Type: "backup", Status: "done", CreatedAt: base},                                    // bad status
		{Type: "backup", Status: "pending"},                                                  // no created_at
		{Type: "backup", Status: "in_progress", Error: ptr("x"), CreatedAt: base},            // error before the end
		{Type: "backup", Status: "pending", SnapshotID: &snapA.ID, CreatedAt: base},          // snapshot before the end
		{Type: "backup", Status: "completed", SnapshotID: &snapB.ID, CreatedAt: base},        // another machine's snapshot
		{Type: "backup", Status: "completed", SnapshotID: ptr(int64(9999)), CreatedAt: base}, // no such snapshot
	} {
		h.status(422, "POST", "/v1/machines/a/backups/history", "tok-a", in)
	}
	// A field this hub doesn't know, from a newer daemon, is ignored.
	h.status(201, "POST", "/v1/machines/a/backups/history", "tok-a", map[string]any{"type": "backup", "status": "pending", "created_at": base, "extra": 1})
	h.status(401, "POST", "/v1/machines/a/backups/history", "", backups.HistoryInput{Type: "backup", Status: "pending", CreatedAt: base})
	h.status(403, "POST", "/v1/machines/a/backups/history", "tok-b", backups.HistoryInput{Type: "backup", Status: "pending", CreatedAt: base})

	// An idempotent retry creates one run.
	in := backups.HistoryInput{Type: "backup", Status: "in_progress", CreatedAt: base.Add(4 * time.Hour)}
	var first, second backups.History
	h.do("POST", "/v1/machines/a/backups/history", "tok-a", in, &first, "Idempotency-Key", "run-1")
	h.do("POST", "/v1/machines/a/backups/history", "tok-a", in, &second, "Idempotency-Key", "run-1")
	var all server.List[backups.History]
	h.do("GET", "/v1/machines/a/backups/history?limit=100", "", nil, &all)
	if first.ID != second.ID || len(all.Data) != 6 {
		t.Errorf("retry made ids %d and %d and %d runs, want one id and 6 runs", first.ID, second.ID, len(all.Data))
	}
	var one backups.History
	h.do("GET", "/v1/machines/a/backups/history/"+strconv.FormatInt(first.ID, 10), "", nil, &one)
	if one.ID != first.ID {
		t.Errorf("run by id = %+v", one)
	}
}

// A run the daemon never finished is closed as failed when a later run of the same type starts.
func TestHistoryAbandoned(t *testing.T) {
	h := newHub(t)
	lost := h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "in_progress", CreatedAt: base})
	check := h.run("tok-a", "a", backups.HistoryInput{Type: "check", Status: "in_progress", CreatedAt: base})
	other := h.run("tok-b", "b", backups.HistoryInput{Type: "backup", Status: "in_progress", CreatedAt: base})
	h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "in_progress", CreatedAt: base.Add(24 * time.Hour)})

	get := func(machine string, id int64) backups.History {
		var got backups.History
		h.do("GET", "/v1/machines/"+machine+"/backups/history/"+strconv.FormatInt(id, 10), "", nil, &got)
		return got
	}
	if got := get("a", lost.ID); got.Status != "failed" || got.Error == nil {
		t.Errorf("lost run = %+v, want failed with an error", got)
	}
	if got := get("a", check.ID); got.Status != "in_progress" {
		t.Errorf("check run = %+v, want untouched", got)
	}
	if got := get("b", other.ID); got.Status != "in_progress" {
		t.Errorf("other machine's run = %+v, want untouched", got)
	}
	// The late report for the closed run is a conflict, which the daemon drops.
	h.status(409, "PATCH", "/v1/machines/a/backups/history/"+strconv.FormatInt(lost.ID, 10), "tok-a", backups.HistoryPatch{Status: ptr("completed")})
}

func TestHistoryPatch(t *testing.T) {
	h := newHub(t)
	snapA := h.put("tok-a", "a", []backups.SnapshotInput{snap("s1", 0, 1)}).Data[0]
	snapB := h.put("tok-b", "b", []backups.SnapshotInput{snap("s1", 0, 1)}).Data[0]
	path := func(machine string, id int64) string {
		return "/v1/machines/" + machine + "/backups/history/" + strconv.FormatInt(id, 10)
	}
	start := func() int64 {
		return h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "in_progress", CreatedAt: base}).ID
	}

	id := start()
	var out backups.History
	res := h.do("PATCH", path("a", id), "tok-a", backups.HistoryPatch{Status: ptr("completed"), SnapshotID: &snapA.ID}, &out)
	if res.StatusCode != 200 || out.Status != "completed" || *out.SnapshotID != snapA.ID || !out.CreatedAt.Equal(base) {
		t.Errorf("finish: %d %+v", res.StatusCode, out)
	}
	// A finished run can't change again, whatever the patch says.
	h.status(409, "PATCH", path("a", id), "tok-a", backups.HistoryPatch{Status: ptr("failed")})
	h.status(409, "PATCH", path("a", id), "tok-a", backups.HistoryPatch{Error: ptr("late")})

	// pending -> in_progress -> failed with an error.
	pending := h.run("tok-a", "a", backups.HistoryInput{Type: "check", Status: "pending", CreatedAt: base}).ID
	h.do("PATCH", path("a", pending), "tok-a", backups.HistoryPatch{Status: ptr("in_progress")}, &out)
	h.do("PATCH", path("a", pending), "tok-a", backups.HistoryPatch{Status: ptr("failed"), Error: ptr("exit 11: repository is already locked")}, &out)
	if out.Status != "failed" || out.Error == nil || *out.Error != "exit 11: repository is already locked" {
		t.Errorf("failed run = %+v", out)
	}

	// Rejected patches leave the run alone.
	id = start()
	h.status(422, "PATCH", path("a", id), "tok-a", backups.HistoryPatch{Status: ptr("pending")})
	h.status(422, "PATCH", path("a", id), "tok-a", backups.HistoryPatch{Status: ptr("done")})
	h.status(422, "PATCH", path("a", id), "tok-a", backups.HistoryPatch{Error: ptr("x")})                                 // still in_progress
	h.status(422, "PATCH", path("a", id), "tok-a", backups.HistoryPatch{Status: ptr("completed"), SnapshotID: &snapB.ID}) // b's snapshot
	h.status(422, "PATCH", path("a", id), "tok-a", backups.HistoryPatch{Status: ptr("completed"), SnapshotID: ptr(int64(9999))})
	h.status(200, "PATCH", path("a", id), "tok-a", map[string]any{"state": "completed"}) // a newer daemon's field, ignored: no change
	h.do("GET", path("a", id), "", nil, &out)
	if out.Status != "in_progress" || out.SnapshotID != nil {
		t.Errorf("run after rejected patches = %+v", out)
	}

	// Another machine's run is invisible: 404 for its own daemon, 403 for a's token on b's path.
	h.status(404, "PATCH", path("b", id), "tok-b", backups.HistoryPatch{Status: ptr("completed")})
	h.status(403, "PATCH", path("b", id), "tok-a", backups.HistoryPatch{Status: ptr("completed")})
	h.status(404, "GET", path("b", id), "", nil)
	h.status(404, "PATCH", path("a", 9999), "tok-a", backups.HistoryPatch{Status: ptr("completed")})
	h.status(401, "PATCH", path("a", id), "", backups.HistoryPatch{Status: ptr("completed")})

	// With an Idempotency-Key a retried finish replays its 200; a new request is a 409.
	patch := backups.HistoryPatch{Status: ptr("completed")}
	for range 2 {
		if res := h.do("PATCH", path("a", id), "tok-a", patch, nil, "Idempotency-Key", "finish-1"); res.StatusCode != 200 {
			t.Errorf("keyed finish: %d, want 200 both times", res.StatusCode)
		}
	}
	h.status(409, "PATCH", path("a", id), "tok-a", patch)
	if res := h.do("PATCH", path("a", id), "tok-a", patch, nil, "Idempotency-Key", "finish-2"); res.StatusCode != 409 {
		t.Errorf("finish with a new key: %d, want 409", res.StatusCode)
	}
}

func TestHistoryList(t *testing.T) {
	h := newHub(t)
	for i, c := range []struct{ typ, status string }{
		{"backup", "completed"}, {"backup", "failed"}, {"check", "completed"}, {"backup", "completed"}, {"cleanup", "completed"},
	} {
		h.run("tok-a", "a", backups.HistoryInput{Type: c.typ, Status: c.status, CreatedAt: base.Add(time.Duration(i) * time.Hour)})
	}
	h.run("tok-b", "b", backups.HistoryInput{Type: "backup", Status: "failed", CreatedAt: base})

	count := func(query string) (int, backups.History) {
		var l server.List[backups.History]
		if res := h.do("GET", "/v1/machines/a/backups/history"+query, "", nil, &l); res.StatusCode != 200 {
			t.Fatalf("GET history%s: %d", query, res.StatusCode)
		}
		if len(l.Data) == 0 {
			return 0, backups.History{}
		}
		return len(l.Data), l.Data[0]
	}
	if n, newest := count(""); n != 5 || newest.Type != "cleanup" {
		t.Errorf("all = %d runs, newest %s; want 5, cleanup", n, newest.Type)
	}
	if n, _ := count("?type=backup"); n != 3 {
		t.Errorf("type=backup: %d, want 3", n)
	}
	if n, _ := count("?status=failed"); n != 1 {
		t.Errorf("status=failed: %d, want 1", n)
	}
	if n, newest := count("?type=backup&status=completed"); n != 2 || !newest.CreatedAt.Equal(base.Add(3*time.Hour)) {
		t.Errorf("backup completed: %d runs, newest %v; want 2, base+3h", n, newest.CreatedAt)
	}

	var seen []int64
	path := "/v1/machines/a/backups/history?limit=2"
	for range 5 {
		var page server.List[backups.History]
		h.do("GET", path, "", nil, &page)
		for _, r := range page.Data {
			seen = append(seen, r.ID)
		}
		if page.NextCursor == "" {
			break
		}
		path = "/v1/machines/a/backups/history?limit=2&cursor=" + page.NextCursor
	}
	if len(seen) != 5 || seen[0] != 5 || seen[4] != 1 {
		t.Errorf("pages gave ids %v, want 5 4 3 2 1", seen)
	}

	h.status(422, "GET", "/v1/machines/a/backups/history?type=restore", "", nil)
	h.status(422, "GET", "/v1/machines/a/backups/history?status=done", "", nil)
	h.status(400, "GET", "/v1/machines/a/backups/history?cursor=%25", "", nil)
	h.status(404, "GET", "/v1/machines/zzz/backups/history", "", nil)
}

func TestSummary(t *testing.T) {
	h := newHub(t)
	h.setup()
	newest := h.put("tok-a", "a", []backups.SnapshotInput{snap("s1", 0, 100), snap("s2", 60, 200)}).Data[0]
	h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "completed", SnapshotID: &newest.ID, CreatedAt: base})
	h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "failed", Error: ptr("boom"), CreatedAt: base.Add(time.Hour)})
	h.run("tok-a", "a", backups.HistoryInput{Type: "check", Status: "completed", CreatedAt: base.Add(30 * time.Minute)})
	h.run("tok-a", "a", backups.HistoryInput{Type: "cleanup", Status: "completed", CreatedAt: base.Add(2 * time.Hour)})

	var sum backups.Summary
	if res := h.do("GET", "/v1/machines/a/backups", "", nil, &sum); res.StatusCode != 200 {
		t.Fatalf("summary: %d", res.StatusCode)
	}
	if sum.Machine != "a" || sum.Settings == nil || sum.Settings.Destination.Name != "r2-code" || sum.SnapshotCount != 2 {
		t.Errorf("summary = %+v", sum)
	}
	if sum.LatestSnapshot == nil || sum.LatestSnapshot.ResticID != "s2" {
		t.Errorf("latest snapshot = %+v, want s2", sum.LatestSnapshot)
	}
	if sum.LastBackup == nil || sum.LastBackup.Status != "failed" || sum.LastCheck == nil || !sum.LastCheck.CreatedAt.Equal(base.Add(30*time.Minute)) {
		t.Errorf("last runs = %+v and %+v, want the failed backup and the check", sum.LastBackup, sum.LastCheck)
	}

	// A machine with nothing has null parts and a zero count.
	var bare backups.Summary
	h.do("GET", "/v1/machines/c/backups", "", nil, &bare)
	if bare.Machine != "c" || bare.Settings != nil || bare.LatestSnapshot != nil || bare.LastBackup != nil || bare.LastCheck != nil || bare.SnapshotCount != 0 {
		t.Errorf("empty summary = %+v", bare)
	}

	var all server.List[backups.Summary]
	h.do("GET", "/v1/backups", "", nil, &all)
	if len(all.Data) != 3 || all.Data[0].Machine != "a" || all.Data[0].SnapshotCount != 2 || all.Data[2].Machine != "c" {
		t.Errorf("all summaries = %+v", all.Data)
	}
	h.status(404, "GET", "/v1/machines/zzz/backups", "", nil)
	h.status(200, "GET", "/v1/backups", "tok-a", nil)
}

func TestLastCompleted(t *testing.T) {
	h := newHub(t)
	h.setup()
	h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "completed", CreatedAt: base})
	h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "completed", CreatedAt: base.Add(time.Hour)})
	h.run("tok-a", "a", backups.HistoryInput{Type: "backup", Status: "failed", Error: ptr("boom"), CreatedAt: base.Add(2 * time.Hour)})
	h.run("tok-a", "a", backups.HistoryInput{Type: "check", Status: "completed", CreatedAt: base.Add(3 * time.Hour)})
	h.run("tok-b", "b", backups.HistoryInput{Type: "backup", Status: "failed", Error: ptr("boom"), CreatedAt: base})

	got, err := h.store.LastCompleted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got["a"].Equal(base.Add(time.Hour)) {
		t.Errorf("got %v, want only a, at the newest completed backup", got)
	}
}
