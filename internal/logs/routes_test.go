package logs_test

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
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/logs"
	"github.com/g4f4r0/tender/internal/server"
)

// testKey is the hub's encryption key in these tests: 32 bytes, 0 to 31.
var testKey = func() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}()

// identity knows machines a (id 1, token tok-a) and b (id 2, token tok-b); logs can't import machines.
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
	path  string
	store *logs.Store
	srv   *httptest.Server
}

func newHub(t *testing.T) *hub {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	_, err = d.Write.Exec(`CREATE TABLE machines (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, token_hash BLOB NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) STRICT;
		INSERT INTO machines VALUES (1, 'a', x'00', 0, 0), (2, 'b', x'00', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background(), logs.Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	st := logs.NewStore(d, testKey)
	srv := server.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), identity{d})
	st.Routes(srv)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &hub{t: t, db: d, path: path, store: st, srv: ts}
}

// do sends a request and returns the status and the body. token, when set, is a daemon's.
func (h *hub) do(method, path, token string, body any) (int, string) {
	h.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (h *hub) must(want int, method, path, token string, body any) string {
	h.t.Helper()
	code, b := h.do(method, path, token, body)
	if code != want {
		h.t.Fatalf("%s %s = %d %s, want %d", method, path, code, b, want)
	}
	return b
}

const header = "Bearer otlp-header-secret"

func otlpSink() map[string]any {
	return map[string]any{"name": "otel", "type": "otlp",
		"config": map[string]any{"endpoint": "https://otel.example.com/v1/logs", "headers": map[string]any{"Authorization": header}}}
}

// A sink round-trips through REST with its secret sealed: never in a response, never in the
// database in plain text, and only the machine's daemon gets it back.
func TestSinkSecrets(t *testing.T) {
	h := newHub(t)
	created := h.must(201, "POST", "/v1/logs/sinks", "", otlpSink())
	if strings.Contains(created, header) || !strings.Contains(created, `"headers":"***","headers_set":true`) {
		t.Errorf("create shows %s", created)
	}
	if strings.Contains(created, `"secret"`) {
		t.Error("an otlp sink has no made secret")
	}
	for _, p := range []string{"/v1/logs/sinks", "/v1/logs/sinks/otel"} {
		if b := h.must(200, "GET", p, "", nil); strings.Contains(b, header) || !strings.Contains(b, `"headers":"***"`) {
			t.Errorf("GET %s shows %s", p, b)
		}
	}
	for _, f := range []string{h.path, h.path + "-wal"} {
		if b, _ := os.ReadFile(f); bytes.Contains(b, []byte(header)) {
			t.Errorf("%s holds the header in plain text", f)
		}
	}
	// people can't read the daemon route, and the daemon gets the secret.
	h.must(401, "GET", "/v1/machines/a/logs/sinks", "", nil)
	h.must(403, "GET", "/v1/machines/a/logs/sinks", "tok-b", nil)
	b := h.must(200, "GET", "/v1/machines/a/logs/sinks", "tok-a", nil)
	if !strings.Contains(b, `"Authorization":"`+header+`"`) {
		t.Errorf("daemon got %s", b)
	}
	if err := logs.NewStore(h.db, make([]byte, 32)).CheckKey(context.Background()); err == nil {
		t.Error("a wrong key opened the secrets")
	}
	if err := h.store.CheckKey(context.Background()); err != nil {
		t.Error(err)
	}
}

// A webhook sink without a secret gets one the hub made, shown once.
func TestWebhookSecretMadeOnce(t *testing.T) {
	h := newHub(t)
	b := h.must(201, "POST", "/v1/logs/sinks", "", map[string]any{"name": "hook", "type": "webhook", "config": map[string]any{"url": "https://logs.example.com/in"}})
	var created struct {
		Secret string         `json:"secret"`
		Config map[string]any `json:"config"`
	}
	_ = json.Unmarshal([]byte(b), &created)
	if len(created.Secret) != 64 || created.Config["secret"] != "***" {
		t.Fatalf("created %s", b)
	}
	if g := h.must(200, "GET", "/v1/logs/sinks/hook", "", nil); strings.Contains(g, created.Secret) {
		t.Error("the made secret shows again")
	}
	if d := h.must(200, "GET", "/v1/machines/a/logs/sinks", "tok-a", nil); !strings.Contains(d, created.Secret) {
		t.Error("the daemon doesn't get the made secret")
	}
	given := h.must(201, "POST", "/v1/logs/sinks", "", map[string]any{"name": "hook2", "type": "webhook",
		"config": map[string]any{"url": "https://logs.example.com/in", "secret": "my-own-secret-1234"}})
	if strings.Contains(given, "my-own-secret-1234") || strings.Contains(given, `"secret":"`) && !strings.Contains(given, `"secret":"***"`) {
		t.Errorf("a given secret is shown: %s", given)
	}
}

func TestSinkDefaultsAndValidation(t *testing.T) {
	h := newHub(t)
	b := h.must(201, "POST", "/v1/logs/sinks", "", otlpSink())
	if !strings.Contains(b, `"enabled":true,"sources":{"journal":{"units":[],"min_priority":"info"},"services":{"names":[]}},"machines":[],"status":[]`) {
		t.Errorf("defaults: %s", b)
	}
	for _, c := range []struct {
		name string
		body map[string]any
		code int
		says string
	}{
		{"bad name", map[string]any{"name": "Otel!", "type": "otlp", "config": map[string]any{"endpoint": "https://x/v1/logs"}}, 422, "Log sink names"},
		{"no type", map[string]any{"name": "x", "config": map[string]any{}}, 422, "type is required"},
		{"bad type", map[string]any{"name": "x", "type": "loki", "config": map[string]any{}}, 422, "type must be one of otlp, webhook"},
		{"no config", map[string]any{"name": "x", "type": "otlp"}, 422, "config is required"},
		{"bad endpoint", map[string]any{"name": "x", "type": "otlp", "config": map[string]any{"endpoint": "nope"}}, 422, "config.endpoint must be"},
		{"hidden secret", map[string]any{"name": "x", "type": "otlp", "config": map[string]any{"endpoint": "https://x/v1/logs", "headers": "***"}}, 422, "how the hub hides a secret"},
		{"no source", map[string]any{"name": "x", "type": "otlp", "config": map[string]any{"endpoint": "https://x/v1/logs"}, "sources": map[string]any{}}, 422, "sources must name journal"},
		{"bad priority", map[string]any{"name": "x", "type": "otlp", "config": map[string]any{"endpoint": "https://x/v1/logs"},
			"sources": map[string]any{"journal": map[string]any{"min_priority": "loud"}}}, 422, "min_priority must be one of"},
		{"bad unit", map[string]any{"name": "x", "type": "otlp", "config": map[string]any{"endpoint": "https://x/v1/logs"},
			"sources": map[string]any{"journal": map[string]any{"units": []string{"--output=x"}}}}, 422, "not a systemd unit name"},
		{"bad container", map[string]any{"name": "x", "type": "otlp", "config": map[string]any{"endpoint": "https://x/v1/logs"},
			"sources": map[string]any{"services": map[string]any{"names": []string{"-x"}}}}, 422, "not a container name"},
		{"unknown machine", map[string]any{"name": "x", "type": "otlp", "config": map[string]any{"endpoint": "https://x/v1/logs"}, "machines": []string{"zzz"}}, 422, `zzz\", which is not a machine`},
		{"unknown field", map[string]any{"name": "x", "type": "otlp", "config": map[string]any{"endpoint": "https://x/v1/logs"}, "nope": 1}, 400, "nope"},
		{"same name", otlpSink(), 409, "already exists"},
	} {
		code, b := h.do("POST", "/v1/logs/sinks", "", c.body)
		if code != c.code || !strings.Contains(b, c.says) {
			t.Errorf("%s: %d %s, want %d with %q", c.name, code, b, c.code, c.says)
		}
	}
	h.must(404, "GET", "/v1/logs/sinks/zzz", "", nil)
	h.must(404, "DELETE", "/v1/logs/sinks/zzz", "", nil)
}

// PATCH merges: a field left out stays, *** keeps a secret, null removes an optional field, and
// the name and type can't change.
func TestPatchSink(t *testing.T) {
	h := newHub(t)
	h.must(201, "POST", "/v1/logs/sinks", "", otlpSink())
	b := h.must(200, "PATCH", "/v1/logs/sinks/otel", "", map[string]any{"config": map[string]any{"endpoint": "https://other.example.com/v1/logs", "headers": "***"},
		"machines": []string{"b"}, "sources": map[string]any{"journal": map[string]any{"units": []string{"nginx.service"}}, "services": nil}})
	if !strings.Contains(b, `"endpoint":"https://other.example.com/v1/logs"`) || !strings.Contains(b, `"headers_set":true`) ||
		!strings.Contains(b, `"sources":{"journal":{"units":["nginx.service"],"min_priority":"info"},"services":null},"machines":["b"]`) {
		t.Errorf("patched: %s", b)
	}
	if d := h.must(200, "GET", "/v1/machines/b/logs/sinks", "tok-b", nil); !strings.Contains(d, header) {
		t.Errorf("the kept header is gone: %s", d)
	}
	if d := h.must(200, "GET", "/v1/machines/a/logs/sinks", "tok-a", nil); d != `{"data":[]}`+"\n" {
		t.Errorf("a gets a sink only for b: %s", d)
	}
	b = h.must(200, "PATCH", "/v1/logs/sinks/otel", "", map[string]any{"config": map[string]any{"headers": nil}, "enabled": false})
	if !strings.Contains(b, `"headers_set":false`) || !strings.Contains(b, `"enabled":false`) {
		t.Errorf("removed headers: %s", b)
	}
	if d := h.must(200, "GET", "/v1/machines/b/logs/sinks", "tok-b", nil); d != `{"data":[]}`+"\n" {
		t.Errorf("a disabled sink goes to no daemon: %s", d)
	}
	for _, c := range []struct {
		body map[string]any
		code int
		says string
	}{
		{map[string]any{"name": "x"}, 422, "The name can't change"},
		{map[string]any{"type": "webhook"}, 422, "The type can't change"},
		{map[string]any{"nope": 1}, 400, "It can change config, enabled, sources, machines"},
		{map[string]any{"config": map[string]any{"endpoint": nil}}, 422, "config.endpoint is required"},
		{map[string]any{"enabled": "yes"}, 422, "enabled must be true or false"},
		{map[string]any{"machines": []string{"zzz"}}, 422, "not a machine"},
	} {
		code, b := h.do("PATCH", "/v1/logs/sinks/otel", "", c.body)
		if code != c.code || !strings.Contains(b, c.says) {
			t.Errorf("%v: %d %s, want %d with %q", c.body, code, b, c.code, c.says)
		}
	}
	h.must(404, "PATCH", "/v1/logs/sinks/zzz", "", map[string]any{"enabled": true})
}

// A daemon's report shows per machine on the sink, and goes with the sink when it is removed.
func TestStatus(t *testing.T) {
	h := newHub(t)
	b := h.must(201, "POST", "/v1/logs/sinks", "", otlpSink())
	var s logs.Sink
	_ = json.Unmarshal([]byte(b), &s)
	sent := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	failing := sent.Add(time.Minute)
	msg := "the sink answered 503 Service Unavailable"
	report := logs.StatusReport{Data: []logs.StatusInput{{SinkID: s.ID, RecordsSent: 1500, RecordsDropped: 7, LastSentAt: &sent, LastError: &msg, FailingSince: &failing},
		{SinkID: 999, RecordsSent: 1}}}
	h.must(401, "PUT", "/v1/machines/a/logs/status", "", report)
	h.must(204, "PUT", "/v1/machines/a/logs/status", "tok-a", report)
	h.must(204, "PUT", "/v1/machines/a/logs/status", "tok-a", report) // again, as an update
	g := h.must(200, "GET", "/v1/logs/sinks/otel", "", nil)
	if !strings.Contains(g, `"status":[{"machine":"a","records_sent":1500,"records_dropped":7,"last_sent_at":"2026-11-15T12:00:00Z","last_error":"the sink answered 503 Service Unavailable","failing_since":"2026-11-15T12:01:00Z","last_dropped_at":null`) {
		t.Errorf("status: %s", g)
	}
	health, err := h.store.Health(context.Background())
	if err != nil || len(health) != 1 || health[0].Machine != "a" || health[0].Sink != "otel" || health[0].FailingSince == nil {
		t.Errorf("health %+v %v", health, err)
	}
	h.must(200, "PATCH", "/v1/logs/sinks/otel", "", map[string]any{"machines": []string{"b"}})
	if health, _ := h.store.Health(context.Background()); len(health) != 0 {
		t.Errorf("a sink no longer for a has health on a: %+v", health)
	}
	h.must(204, "DELETE", "/v1/logs/sinks/otel", "", nil)
	var n int
	_ = h.db.Read.QueryRow(`SELECT count(*) FROM log_sink_status`).Scan(&n)
	if n != 0 {
		t.Errorf("%d status rows left after the sink went", n)
	}
	code, body := h.do("PUT", "/v1/machines/a/logs/status", "tok-a", map[string]any{"data": []map[string]any{{"sink_id": 1, "records_sent": -1}}})
	if code != 422 {
		t.Errorf("negative count: %d %s", code, body)
	}
}

// A URL's query string can carry a key, so it is kept like a secret: sealed in the database,
// shown as ?***, kept when a read is sent back, and given in full only to the daemon.
func TestURLQueryIsSecret(t *testing.T) {
	h := newHub(t)
	const key = "urlkey-in-the-query"
	created := h.must(201, "POST", "/v1/logs/sinks", "", map[string]any{"name": "hook", "type": "webhook",
		"config": map[string]any{"url": "https://logs.example.com/in?key=" + key, "secret": "0123456789abcdef"}})
	if strings.Contains(created, key) || !strings.Contains(created, `"url":"https://logs.example.com/in?***"`) {
		t.Errorf("create shows %s", created)
	}
	for _, p := range []string{"/v1/logs/sinks", "/v1/logs/sinks/hook"} {
		if b := h.must(200, "GET", p, "", nil); strings.Contains(b, key) || strings.Contains(b, "url_query") {
			t.Errorf("GET %s shows %s", p, b)
		}
	}
	for _, f := range []string{h.path, h.path + "-wal"} {
		if b, _ := os.ReadFile(f); bytes.Contains(b, []byte(key)) {
			t.Errorf("%s holds the query in plain text", f)
		}
	}
	// Sending back the URL a read showed keeps the query; a ?*** URL of another place is refused.
	h.must(200, "PATCH", "/v1/logs/sinks/hook", "", map[string]any{"config": map[string]any{"url": "https://logs.example.com/in?***"}})
	if b := h.must(200, "GET", "/v1/machines/a/logs/sinks", "tok-a", nil); !strings.Contains(b, `"url":"https://logs.example.com/in?key=`+key+`"`) {
		t.Errorf("daemon got %s", b)
	}
	if code, b := h.do("PATCH", "/v1/logs/sinks/hook", "", map[string]any{"config": map[string]any{"url": "https://other.example.com/in?***"}}); code != 422 || !strings.Contains(b, "?***") {
		t.Errorf("PATCH another masked URL = %d %s, want 422", code, b)
	}
	if code, _ := h.do("POST", "/v1/logs/sinks", "", map[string]any{"name": "x", "type": "otlp",
		"config": map[string]any{"endpoint": "https://otel.example.com/v1/logs?***"}}); code != 422 {
		t.Errorf("POST a masked endpoint = %d, want 422", code)
	}
	// A new query replaces the old; no query removes it.
	h.must(200, "PATCH", "/v1/logs/sinks/hook", "", map[string]any{"config": map[string]any{"url": "https://logs.example.com/in"}})
	if b := h.must(200, "GET", "/v1/machines/a/logs/sinks", "tok-a", nil); strings.Contains(b, key) || !strings.Contains(b, `"url":"https://logs.example.com/in"`) {
		t.Errorf("daemon got %s after the query was removed", b)
	}
}
