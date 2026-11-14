package notifications_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
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

// newHubSending is newHub with the test email route sending through sender.
func newHubSending(t *testing.T, sender notifications.Sender) *httptest.Server {
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
	notifications.NewStore(d).Routes(s, sender, "hub1")
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv
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

func TestRecipientRoutes(t *testing.T) {
	srv := newHub(t)

	var list server.List[notifications.Recipient]
	if res := send(t, srv, "GET", "/v1/notifications/recipients", "", &list); res.StatusCode != 200 || list.Data == nil || len(list.Data) != 0 {
		t.Fatalf("empty list: %d %+v, want 200 and []", res.StatusCode, list)
	}

	var added notifications.Recipient
	res := send(t, srv, "POST", "/v1/notifications/recipients", `{"email":"johan@example.com"}`, &added)
	if res.StatusCode != 201 || added.ID == 0 || added.Email != "johan@example.com" || added.CreatedAt.IsZero() ||
		res.Header.Get("Location") != "/v1/notifications/recipients/"+strconv.FormatInt(added.ID, 10) {
		t.Fatalf("add: %d %+v location %q", res.StatusCode, added, res.Header.Get("Location"))
	}
	send(t, srv, "POST", "/v1/notifications/recipients", `{"email":"ops@example.com"}`, nil)
	send(t, srv, "GET", "/v1/notifications/recipients", "", &list)
	if len(list.Data) != 2 || list.Data[0].Email != "johan@example.com" || list.Data[1].Email != "ops@example.com" {
		t.Errorf("list = %+v, want both in the order added", list.Data)
	}

	var p problem
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"duplicate", `{"email":"johan@example.com"}`, 409},
		{"duplicate in another case", `{"email":"Johan@Example.com"}`, 409},
		{"not an address", `{"email":"johan"}`, 422},
		{"empty", `{"email":""}`, 422},
		{"with a display name", `{"email":"Johan <johan@example.com>"}`, 422},
		{"with a second address", `{"email":"a@example.com, b@example.com"}`, 422},
		{"with a header injection", `{"email":"a@example.com\r\nBcc: x@example.com"}`, 422},
		{"unknown field", `{"email":"x@example.com","name":"X"}`, 400},
		{"not JSON", `{`, 400},
		{"wrong type", `{"email":5}`, 422},
	} {
		if res := send(t, srv, "POST", "/v1/notifications/recipients", c.body, &p); res.StatusCode != c.want || p.Detail == "" {
			t.Errorf("%s: %d %q, want %d with a sentence", c.name, res.StatusCode, p.Detail, c.want)
		}
	}
	send(t, srv, "GET", "/v1/notifications/recipients", "", &list)
	if len(list.Data) != 2 {
		t.Errorf("%d recipients after the refused adds, want 2", len(list.Data))
	}

	path := "/v1/notifications/recipients/" + strconv.FormatInt(added.ID, 10)
	if res := send(t, srv, "DELETE", path, "", nil); res.StatusCode != 204 {
		t.Errorf("delete: %d, want 204", res.StatusCode)
	}
	for _, path := range []string{path, "/v1/notifications/recipients/999", "/v1/notifications/recipients/x"} {
		if res := send(t, srv, "DELETE", path, "", &p); res.StatusCode != 404 || p.Detail == "" {
			t.Errorf("DELETE %s: %d %q, want 404 with a sentence", path, res.StatusCode, p.Detail)
		}
	}
	send(t, srv, "GET", "/v1/notifications/recipients", "", &list)
	if len(list.Data) != 1 || list.Data[0].Email != "ops@example.com" {
		t.Errorf("recipients after the delete = %+v, want only ops", list.Data)
	}
}

// mailbox records what it is asked to send, and fails when fail is set.
type mailbox struct {
	to   [][]string
	fail bool
}

func (m *mailbox) Send(_ context.Context, to []string, _ notifications.Message) error {
	if m.fail {
		return errors.New("smtp down")
	}
	m.to = append(m.to, to)
	return nil
}

func TestTestEmailRoute(t *testing.T) {
	// Email off.
	off := newHub(t)
	send(t, off, "POST", "/v1/notifications/recipients", `{"email":"a@example.com"}`, nil)
	var p problem
	if res := send(t, off, "POST", "/v1/notifications/test", "", &p); res.StatusCode != 422 || !strings.Contains(p.Detail, "TENDER_SMTP_HOST") {
		t.Errorf("email off: %d %q, want 422 that says what to set", res.StatusCode, p.Detail)
	}

	box := &mailbox{}
	srv := newHubSending(t, box)
	if res := send(t, srv, "POST", "/v1/notifications/test", "", &p); res.StatusCode != 422 || !strings.Contains(p.Detail, "Add one") || len(box.to) != 0 {
		t.Errorf("no recipients: %d %q, %d sent, want 422 that says to add one and nothing sent", res.StatusCode, p.Detail, len(box.to))
	}

	send(t, srv, "POST", "/v1/notifications/recipients", `{"email":"a@example.com"}`, nil)
	send(t, srv, "POST", "/v1/notifications/recipients", `{"email":"b@example.com"}`, nil)
	var sent notifications.TestResult
	if res := send(t, srv, "POST", "/v1/notifications/test", "", &sent); res.StatusCode != 202 || !slices.Equal(sent.SentTo, []string{"a@example.com", "b@example.com"}) {
		t.Errorf("test: %d %+v, want 202 to both", res.StatusCode, sent)
	}
	if len(box.to) != 1 || !slices.Equal(box.to[0], []string{"a@example.com", "b@example.com"}) {
		t.Errorf("one email to both expected, got %v", box.to)
	}

	box.fail = true
	if res := send(t, srv, "POST", "/v1/notifications/test", "", &p); res.StatusCode != 502 || !strings.Contains(p.Detail, "smtp down") || !strings.Contains(p.Detail, "TENDER_SMTP_") {
		t.Errorf("send fails: %d %q, want 502 with the reason and what to check", res.StatusCode, p.Detail)
	}
}
