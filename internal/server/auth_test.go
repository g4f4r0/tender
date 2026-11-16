package server

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
)

// fakeMachines knows machines a (id 1, token tok-a) and b (id 2, token tok-b).
type fakeMachines struct{}

var fakeIDs = map[string]int64{"a": 1, "b": 2}

func (fakeMachines) Authenticate(_ context.Context, token string) (int64, string, error) {
	switch token {
	case "tok-a":
		return 1, "a", nil
	case "tok-b":
		return 2, "b", nil
	}
	return 0, "", NotFound("no such token")
}

func (fakeMachines) Lookup(_ context.Context, name string) (int64, error) {
	if id, ok := fakeIDs[name]; ok {
		return id, nil
	}
	return 0, NotFound("no machine %q", name)
}

// routes opens a real database with the server's tables and serves test routes:
//
//	POST  /v1/machines/{machine}/things   daemon, counts calls, answers 201 with Location
//	PATCH /v1/machines/{machine}/things   daemon, counts calls
//	PATCH /v1/machines/{machine}/quiet    daemon, counts calls, answers 204 with no body
//	POST  /v1/machines/{machine}/boom     daemon, fails with a plain error
//	POST  /v1/machines/{machine}/echo     daemon, decodes {"a": int}
//	GET   /v1/machines/{machine}/either   daemon or people, says which
//	GET   /v1/machines/{machine}/read     people
//	GET   /v1/everything                  people, no machine
//	POST  /v1/everything                  people, counts calls
//	GET   /v1/machines/{machine}/numbers  people, 1..5 newest first, paged
func routes(t *testing.T) (*Server, *httptest.Server, *atomic.Int64) {
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
	_, err = d.Write.Exec(`CREATE TABLE machines (id INTEGER PRIMARY KEY, name TEXT NOT NULL, token_hash BLOB NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) STRICT;
		INSERT INTO machines VALUES (1, 'a', x'00', 0, 0), (2, 'b', x'00', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background(), Migrations); err != nil {
		t.Fatal(err)
	}

	s := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), fakeMachines{})
	var calls atomic.Int64
	created := func(w http.ResponseWriter, r *http.Request) error {
		n := calls.Add(1)
		time.Sleep(20 * time.Millisecond) // lets a concurrent twin arrive
		w.Header().Set("Location", r.URL.Path+"/"+strconv.FormatInt(n, 10))
		WriteJSON(w, http.StatusCreated, map[string]any{"n": n, "machine": MachineID(r.Context())})
		return nil
	}
	s.Handle("POST /v1/machines/{machine}/things", s.Daemon(created))
	s.Handle("PATCH /v1/machines/{machine}/things", s.Daemon(created))
	s.Handle("PATCH /v1/machines/{machine}/quiet", s.Daemon(func(w http.ResponseWriter, r *http.Request) error {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
	s.Handle("POST /v1/machines/{machine}/boom", s.Daemon(func(http.ResponseWriter, *http.Request) error {
		calls.Add(1)
		return io.ErrUnexpectedEOF
	}))
	s.Handle("POST /v1/machines/{machine}/echo", s.Daemon(func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			A int `json:"a"`
		}
		if err := Decode(w, r, &in); err != nil {
			return err
		}
		WriteJSON(w, http.StatusOK, in)
		return nil
	}))
	ok := func(w http.ResponseWriter, r *http.Request) error {
		WriteJSON(w, http.StatusOK, map[string]int64{"machine": MachineID(r.Context())})
		return nil
	}
	s.Handle("GET /v1/machines/{machine}/either", s.DaemonOrPeople(func(w http.ResponseWriter, r *http.Request) error {
		WriteJSON(w, http.StatusOK, map[string]any{"machine": MachineID(r.Context()), "daemon": IsDaemon(r.Context())})
		return nil
	}))
	s.Handle("GET /v1/machines/{machine}/read", s.People(ok))
	s.Handle("GET /v1/everything", s.People(ok))
	s.Handle("POST /v1/everything", s.People(func(w http.ResponseWriter, r *http.Request) error {
		WriteJSON(w, http.StatusCreated, map[string]int64{"n": calls.Add(1)})
		return nil
	}))
	s.Handle("GET /v1/machines/{machine}/numbers", s.People(func(w http.ResponseWriter, r *http.Request) error {
		p, err := ParsePage(r)
		if err != nil {
			return err
		}
		var rows []int64
		for n := int64(5); n >= 1 && len(rows) < p.Size(); n-- {
			if n < p.Before.ID {
				rows = append(rows, n)
			}
		}
		WriteJSON(w, http.StatusOK, NewList(rows, p, func(n int64) Cursor { return Cursor{CreatedAt: n * 10, ID: n} }))
		return nil
	}))

	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv, &calls
}

// send makes a request with header pairs and returns the response and its body.
func send(t *testing.T, srv *httptest.Server, method, path, body string, headers ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return res, string(b)
}

func TestAuth(t *testing.T) {
	_, srv, _ := routes(t)
	daemonA := []string{"Authorization", "Bearer tok-a"}
	reader := []string{"Tailscale-User-Login", "johan@example.com"}

	for _, c := range []struct {
		name, method, path string
		headers            []string
		want               int
	}{
		{"daemon without token", "POST", "/v1/machines/a/things", nil, 401},
		{"daemon with unknown token", "POST", "/v1/machines/a/things", []string{"Authorization", "Bearer nope"}, 401},
		{"daemon with a non-bearer scheme", "POST", "/v1/machines/a/things", []string{"Authorization", "Basic tok-a"}, 401},
		{"daemon route with only the Tailscale header", "POST", "/v1/machines/a/things", reader, 401},
		{"daemon with its own token", "POST", "/v1/machines/a/things", daemonA, 201},
		{"daemon with another machine's token", "POST", "/v1/machines/b/things", daemonA, 403},
		{"daemon with a token on an unknown machine", "POST", "/v1/machines/zzz/things", daemonA, 404},
		{"people without a header", "GET", "/v1/machines/a/read", nil, 200},
		{"people with the Tailscale header", "GET", "/v1/machines/a/read", reader, 200},
		{"people on an unknown machine", "GET", "/v1/machines/zzz/read", reader, 404},
		{"people on a route without machine", "GET", "/v1/everything", reader, 200},
		{"people route ignores a token", "GET", "/v1/machines/a/read", daemonA, 200},
		{"either as daemon", "GET", "/v1/machines/a/either", daemonA, 200},
		{"either as people", "GET", "/v1/machines/a/either", reader, 200},
		{"either with nothing", "GET", "/v1/machines/a/either", nil, 200},
		{"either with another machine's token", "GET", "/v1/machines/b/either", daemonA, 403},
		{"either with a bad token and the Tailscale header", "GET", "/v1/machines/a/either", append([]string{"Authorization", "Bearer nope"}, reader...), 401},
	} {
		res, body := send(t, srv, c.method, c.path, "{}", c.headers...)
		if res.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d (%s)", c.name, res.StatusCode, c.want, body)
		}
		if c.want >= 400 && res.Header.Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: content type %q, want problem+json", c.name, res.Header.Get("Content-Type"))
		}
	}

	_, body := send(t, srv, "GET", "/v1/machines/b/read", "", reader...)
	if body != "{\"machine\":2}\n" {
		t.Errorf("handler saw machine %q, want id 2", body)
	}

	// The handler learns whether the daemon or people called.
	_, body = send(t, srv, "GET", "/v1/machines/a/either", "", daemonA...)
	if body != "{\"daemon\":true,\"machine\":1}\n" {
		t.Errorf("daemon call: %q", body)
	}
	_, body = send(t, srv, "GET", "/v1/machines/a/either", "")
	if body != "{\"daemon\":false,\"machine\":1}\n" {
		t.Errorf("people call: %q", body)
	}
}

// fakeTokens knows one API token, api-1.
type fakeTokens struct{}

func (fakeTokens) Authenticate(_ context.Context, token string) (string, error) {
	if token == "api-1" {
		return "laptop", nil
	}
	return "", NotFound("no such token")
}

// With RequireTokens, people routes need an API token, a machine token never passes as one, an
// API token never passes as a daemon, and health stays open.
func TestAuthWithAPITokens(t *testing.T) {
	s, srv, _ := routes(t)
	s.RequireTokens(fakeTokens{})
	daemonA := []string{"Authorization", "Bearer tok-a"}
	person := []string{"Authorization", "Bearer api-1"}
	for _, c := range []struct {
		name, method, path string
		headers            []string
		want               int
	}{
		{"health without a token", "GET", "/v1/health", nil, 200},
		{"people without a token", "GET", "/v1/machines/a/read", nil, 401},
		{"people with only the Tailscale header", "GET", "/v1/machines/a/read", []string{"Tailscale-User-Login", "johan@example.com"}, 401},
		{"people with a wrong token", "GET", "/v1/everything", []string{"Authorization", "Bearer api-2"}, 401},
		{"people with a non-bearer scheme", "GET", "/v1/everything", []string{"Authorization", "Basic api-1"}, 401},
		{"people with a machine token", "GET", "/v1/machines/a/read", daemonA, 401},
		{"people with an API token", "GET", "/v1/machines/a/read", person, 200},
		{"people write with an API token", "POST", "/v1/everything", person, 201},
		{"people without a token on an unknown machine", "GET", "/v1/machines/zzz/read", nil, 401},
		{"people with an API token on an unknown machine", "GET", "/v1/machines/zzz/read", person, 404},
		{"daemon with an API token", "POST", "/v1/machines/a/things", person, 401},
		{"daemon with its own token", "POST", "/v1/machines/a/things", daemonA, 201},
		{"either without a token", "GET", "/v1/machines/a/either", nil, 401},
		{"either with a wrong token", "GET", "/v1/machines/a/either", []string{"Authorization", "Bearer nope"}, 401},
		{"either as daemon", "GET", "/v1/machines/a/either", daemonA, 200},
		{"either with an API token", "GET", "/v1/machines/a/either", person, 200},
		{"either with another machine's token", "GET", "/v1/machines/b/either", daemonA, 403},
	} {
		res, body := send(t, srv, c.method, c.path, "{}", c.headers...)
		if res.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d (%s)", c.name, res.StatusCode, c.want, body)
		}
		// People are told how to get an API token; a daemon route keeps its own sentence.
		if c.want == 401 && (res.Header.Get("Content-Type") != "application/problem+json" ||
			!strings.HasSuffix(c.path, "/things") && !strings.Contains(body, "sudo tender tokens add NAME")) {
			t.Errorf("%s: %q %s, want problem+json saying how to get a token", c.name, res.Header.Get("Content-Type"), body)
		}
	}
	// The handler tells the daemon from people by the token they sent.
	if _, body := send(t, srv, "GET", "/v1/machines/a/either", "", daemonA...); body != "{\"daemon\":true,\"machine\":1}\n" {
		t.Errorf("machine token: %q", body)
	}
	if _, body := send(t, srv, "GET", "/v1/machines/a/either", "", person...); body != "{\"daemon\":false,\"machine\":1}\n" {
		t.Errorf("API token: %q", body)
	}
	// A people 401 says how to make the first token.
	if _, body := send(t, srv, "GET", "/v1/everything", ""); !strings.Contains(body, "sudo tender tokens add NAME") || !strings.Contains(body, "TENDER_TOKEN") {
		t.Errorf("401 = %s", body)
	}
}

func TestPeopleRoutesIgnoreIdempotencyKeys(t *testing.T) {
	_, srv, calls := routes(t)
	for range 2 {
		res, body := send(t, srv, "POST", "/v1/everything", "{}", "Idempotency-Key", "k", "Authorization", "Bearer tok-a")
		if res.StatusCode != 201 {
			t.Fatalf("people POST: %d %s", res.StatusCode, body)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("a people POST with a key ran %d times, want 2: keys are for daemons", calls.Load())
	}
}

func TestIdempotency(t *testing.T) {
	_, srv, calls := routes(t)
	a := func(key string) []string { return []string{"Authorization", "Bearer tok-a", "Idempotency-Key", key} }

	first, firstBody := send(t, srv, "POST", "/v1/machines/a/things", "{}", a("k1")...)
	if first.StatusCode != 201 || first.Header.Get("Location") != "/v1/machines/a/things/1" {
		t.Fatalf("first: %d location %q", first.StatusCode, first.Header.Get("Location"))
	}
	again, againBody := send(t, srv, "POST", "/v1/machines/a/things", "{}", a("k1")...)
	if again.StatusCode != 201 || againBody != firstBody || calls.Load() != 1 {
		t.Errorf("replay: %d %q after %d calls, want 201 %q after 1", again.StatusCode, againBody, calls.Load(), firstBody)
	}
	if again.Header.Get("Location") != "/v1/machines/a/things/1" || again.Header.Get("Content-Type") != "application/json" {
		t.Errorf("replay headers: Location %q, Content-Type %q", again.Header.Get("Location"), again.Header.Get("Content-Type"))
	}

	// An answer with no body is kept too: a retried 204 is a 204, and runs nothing.
	for range 2 {
		if res, body := send(t, srv, "PATCH", "/v1/machines/a/quiet", "{}", a("k0")...); res.StatusCode != 204 || body != "" || calls.Load() != 2 {
			t.Errorf("quiet: %d %q after %d calls, want 204 after 2", res.StatusCode, body, calls.Load())
		}
	}
	calls.Store(1)

	// The same key on another method or path is a mistake, and nothing runs.
	for _, c := range []struct{ method, path string }{{"PATCH", "/v1/machines/a/things"}, {"POST", "/v1/machines/a/echo"}} {
		res, body := send(t, srv, c.method, c.path, "{}", a("k1")...)
		if res.StatusCode != 422 || calls.Load() != 1 {
			t.Errorf("%s %s with a used key: %d %s after %d calls, want 422 after 1", c.method, c.path, res.StatusCode, body, calls.Load())
		}
	}

	// Keys belong to a machine: b may use the same key.
	res, _ := send(t, srv, "POST", "/v1/machines/b/things", "{}", "Authorization", "Bearer tok-b", "Idempotency-Key", "k1")
	if res.StatusCode != 201 || calls.Load() != 2 {
		t.Errorf("other machine, same key: %d after %d calls, want 201 after 2", res.StatusCode, calls.Load())
	}

	// A 5xx is not stored, so a retry runs again. A 4xx is stored.
	before := calls.Load()
	for range 2 {
		if res, _ := send(t, srv, "POST", "/v1/machines/a/boom", "{}", a("k2")...); res.StatusCode != 500 {
			t.Errorf("boom: %d, want 500", res.StatusCode)
		}
	}
	if calls.Load() != before+2 {
		t.Errorf("a 500 was stored: %d runs, want 2", calls.Load()-before)
	}
	for range 2 {
		if res, _ := send(t, srv, "POST", "/v1/machines/a/echo", "{", a("k3")...); res.StatusCode != 400 {
			t.Errorf("malformed echo: %d, want 400", res.StatusCode)
		}
	}
	if res, _ := send(t, srv, "POST", "/v1/machines/a/echo", `{"a":1}`, a("k3")...); res.StatusCode != 400 {
		t.Errorf("replayed 400 changed to %d", res.StatusCode)
	}

	// Without a key every request runs.
	before = calls.Load()
	send(t, srv, "POST", "/v1/machines/a/things", "{}", "Authorization", "Bearer tok-a")
	send(t, srv, "POST", "/v1/machines/a/things", "{}", "Authorization", "Bearer tok-a")
	if calls.Load() != before+2 {
		t.Errorf("keyless requests ran %d times, want 2", calls.Load()-before)
	}
}

func TestIdempotencyConcurrent(t *testing.T) {
	_, srv, calls := routes(t)
	bodies := make([]string, 8)
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, bodies[i] = send(t, srv, "PATCH", "/v1/machines/a/things", "{}", "Authorization", "Bearer tok-a", "Idempotency-Key", "same")
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("handler ran %d times for one key, want 1", calls.Load())
	}
	for _, b := range bodies {
		if b != bodies[0] {
			t.Errorf("bodies differ: %q and %q", b, bodies[0])
		}
	}
}

func TestPruneKeys(t *testing.T) {
	s, srv, _ := routes(t)
	send(t, srv, "POST", "/v1/machines/a/things", "{}", "Authorization", "Bearer tok-a", "Idempotency-Key", "old")
	count := func() (n int) {
		if err := s.db.Read.QueryRow(`SELECT count(*) FROM idempotency_keys`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if err := s.pruneKeys(context.Background(), time.Now().Add(23*time.Hour)); err != nil || count() != 1 {
		t.Fatalf("after 23 hours: %d keys, error %v; want 1", count(), err)
	}
	if err := s.pruneKeys(context.Background(), time.Now().Add(25*time.Hour)); err != nil || count() != 0 {
		t.Fatalf("after 25 hours: %d keys, error %v; want 0", count(), err)
	}
}

func TestDecode(t *testing.T) {
	_, srv, _ := routes(t)
	daemon := []string{"Authorization", "Bearer tok-a"}
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"valid", `{"a":1}`, 200},
		{"malformed", `{"a":`, 400},
		{"empty", ``, 400},
		{"unknown field", `{"b":1}`, 400},
		{"two values", `{"a":1}{"a":2}`, 400},
		{"wrong type", `{"a":"x"}`, 422},
		{"not an object", `[]`, 422},
		{"a number", `7`, 422},
		{"too large", `{"a":1,"pad":"` + strings.Repeat("x", 1<<20) + `"}`, 413},
	} {
		res, body := send(t, srv, "POST", "/v1/machines/a/echo", c.body, daemon...)
		if res.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d (%.80s)", c.name, res.StatusCode, c.want, body)
		}
		if strings.HasPrefix(c.name, "not an object") || c.name == "a number" {
			if !strings.Contains(body, `"detail":"The request body must be a JSON object."`) {
				t.Errorf("%s: %s", c.name, body)
			}
		}
	}
}

func TestPaging(t *testing.T) {
	_, srv, _ := routes(t)
	reader := []string{"Tailscale-User-Login", "johan@example.com"}

	var got []int64
	path := "/v1/machines/a/numbers?limit=2"
	for pages := 0; path != ""; pages++ {
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		_, body := send(t, srv, "GET", path, "", reader...)
		var list List[int64]
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			t.Fatal(err)
		}
		got = append(got, list.Data...)
		path = ""
		if list.NextCursor != "" {
			path = "/v1/machines/a/numbers?limit=2&cursor=" + list.NextCursor
		}
	}
	if want := []int64{5, 4, 3, 2, 1}; !equal(got, want) {
		t.Errorf("pages gave %v, want %v", got, want)
	}

	// Without a limit, all five come back on one page with no cursor.
	_, body := send(t, srv, "GET", "/v1/machines/a/numbers", "", reader...)
	if strings.Contains(body, "next_cursor") {
		t.Errorf("one page still has a cursor: %s", body)
	}

	for _, q := range []string{"limit=0", "limit=501", "limit=x", "cursor=%25%25", "cursor=" + "bm9wZQ"} {
		if res, _ := send(t, srv, "GET", "/v1/machines/a/numbers?"+q, "", reader...); res.StatusCode != 400 {
			t.Errorf("?%s: got %d, want 400", q, res.StatusCode)
		}
	}
}

func equal(a, b []int64) bool {
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
