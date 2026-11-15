package notifications_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

// identity knows machines a (id 1) and b (id 2); notifications can't import machines.
type identity struct{ d *db.DB }

func (i identity) Authenticate(context.Context, string) (int64, string, error) {
	return 0, "", server.NotFound("no such token")
}

func (i identity) Lookup(ctx context.Context, name string) (int64, error) {
	var id int64
	if err := i.d.Read.QueryRowContext(ctx, `SELECT id FROM machines WHERE name = ?`, name).Scan(&id); err != nil {
		return 0, server.NotFound("no machine %q", name)
	}
	return id, nil
}

// newHub serves the routes over seeded rows: ids 1 to 5, created at times 100 to 500.
//  1. a, backup_failed, critical, sent
//  2. a, disk_full, warning, pending
//  3. b, backup_failed, critical, resolved
//  4. none, hub_restarted, info, sent
//  5. a, backup_failed, critical, resolved
func newHub(t *testing.T) *httptest.Server { return newHubSending(t, nil) }

// newHubSending is newHub with the test route sending email through sender.
func newHubSending(t *testing.T, sender notifications.Sender) *httptest.Server {
	t.Helper()
	srv, _ := newHubDB(t, sender)
	return srv
}

var testKey = []byte("0123456789abcdef0123456789abcdef")

// newHubDB is newHubSending that also returns the database.
func newHubDB(t *testing.T, sender notifications.Sender) (*httptest.Server, *db.DB) {
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
		INSERT INTO machines VALUES (1, 'a', x'00', 0, 0), (2, 'b', x'00', 0, 0), (3, 'c', x'00', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background(), notifications.Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	_, err = d.Write.Exec(`INSERT INTO notifications (id, machine_id, type, severity, status, content, created_at, updated_at) VALUES
		(1, 1, 'backup_failed', 'critical', 'sent', 'first', 100, 110),
		(2, 1, 'disk_full', 'warning', 'pending', 'second', 200, 200),
		(3, 2, 'backup_failed', 'critical', 'resolved', 'third', 300, 310),
		(4, NULL, 'hub_restarted', 'info', 'sent', 'fourth', 400, 400),
		(5, 1, 'backup_failed', 'critical', 'resolved', 'fifth', 500, 510)`)
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), identity{d})
	notifications.NewStore(d, testKey).Routes(s, sender, notifications.Hub{Name: "hub1", URL: "https://hub1.example.com"})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv, d
}

func get(t *testing.T, srv *httptest.Server, path string, login bool, out any) int {
	t.Helper()
	req, err := http.NewRequest("GET", srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if login {
		req.Header.Set("Tailscale-User-Login", "johan@example.com")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if out != nil && res.StatusCode == 200 {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("GET %s: decode: %v", path, err)
		}
	}
	return res.StatusCode
}

func ids(l server.List[notifications.Notification]) []int64 {
	out := []int64{}
	for _, n := range l.Data {
		out = append(out, n.ID)
	}
	return out
}

func same(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestList(t *testing.T) {
	srv := newHub(t)
	for _, c := range []struct {
		name, path string
		want       []int64
	}{
		{"all, newest first", "/v1/notifications", []int64{5, 4, 3, 2, 1}},
		{"status", "/v1/notifications?status=resolved", []int64{5, 3}},
		{"severity", "/v1/notifications?severity=critical", []int64{5, 3, 1}},
		{"status and severity", "/v1/notifications?status=sent&severity=critical", []int64{1}},
		{"no match", "/v1/notifications?status=pending&severity=info", []int64{}},
		{"one machine", "/v1/machines/a/notifications", []int64{5, 2, 1}},
		{"one machine and status", "/v1/machines/a/notifications?status=resolved", []int64{5}},
		{"machine without any", "/v1/machines/c/notifications", []int64{}},
	} {
		var l server.List[notifications.Notification]
		if code := get(t, srv, c.path, true, &l); code != 200 || !same(ids(l), c.want) {
			t.Errorf("%s: %d %v, want 200 %v", c.name, code, ids(l), c.want)
		}
		if l.Data == nil {
			t.Errorf("%s: data is null, want []", c.name)
		}
	}
}

func TestListPages(t *testing.T) {
	srv := newHub(t)
	var got []int64
	path := "/v1/notifications?limit=2"
	for pages := 0; path != ""; pages++ {
		var l server.List[notifications.Notification]
		if code := get(t, srv, path, true, &l); code != 200 {
			t.Fatalf("GET %s: %d", path, code)
		}
		if pages == 0 && len(l.Data) != 2 {
			t.Fatalf("first page has %d rows, want 2", len(l.Data))
		}
		got = append(got, ids(l)...)
		path = ""
		if l.NextCursor != "" {
			path = "/v1/notifications?limit=2&cursor=" + l.NextCursor
		}
		if pages > 3 {
			t.Fatal("too many pages")
		}
	}
	if !same(got, []int64{5, 4, 3, 2, 1}) {
		t.Errorf("pages hold %v, want 5 4 3 2 1 with no repeat or gap", got)
	}
}

func TestListRejectsBadQueries(t *testing.T) {
	srv := newHub(t)
	for path, want := range map[string]int{
		"/v1/notifications?status=open":     422,
		"/v1/notifications?severity=fatal":  422,
		"/v1/notifications?limit=0":         400,
		"/v1/notifications?limit=501":       400,
		"/v1/notifications?cursor=nonsense": 400,
		"/v1/machines/nope/notifications":   404,
	} {
		if code := get(t, srv, path, true, nil); code != want {
			t.Errorf("GET %s: %d, want %d", path, code, want)
		}
	}
	for _, path := range []string{"/v1/notifications", "/v1/machines/a/notifications", "/v1/machines/a/notifications/1"} {
		if code := get(t, srv, path, false, nil); code != 200 {
			t.Errorf("GET %s without the Tailscale header: %d, want 200", path, code)
		}
	}
}

func TestGet(t *testing.T) {
	srv := newHub(t)
	var n notifications.Notification
	if code := get(t, srv, "/v1/machines/a/notifications/1", true, &n); code != 200 {
		t.Fatalf("GET: %d", code)
	}
	if n.ID != 1 || n.Machine == nil || *n.Machine != "a" || n.Type != "backup_failed" || n.Severity != "critical" ||
		n.Status != "sent" || n.Content != "first" || n.CreatedAt.Unix() != 100 || n.UpdatedAt.Unix() != 110 {
		t.Errorf("got %+v", n)
	}
	// Notification 3 belongs to machine b, 4 to no machine.
	for _, path := range []string{"/v1/machines/a/notifications/3", "/v1/machines/a/notifications/4",
		"/v1/machines/a/notifications/99", "/v1/machines/a/notifications/x"} {
		if code := get(t, srv, path, true, nil); code != 404 {
			t.Errorf("GET %s: %d, want 404", path, code)
		}
	}

	// A machine-less notification shows machine null in the list.
	var l server.List[notifications.Notification]
	get(t, srv, "/v1/notifications?severity=info", true, &l)
	if len(l.Data) != 1 || l.Data[0].Machine != nil {
		t.Errorf("machine-less notification: %+v", l.Data)
	}
}

// send makes a request with an optional JSON body and decodes the response into out.
func send(t *testing.T, srv *httptest.Server, method, path, body string, out any) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return res
}

type problem struct{ Detail string }
