package machines_test

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
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/server"
)

const reader = "johan@example.com"

type hub struct {
	t     *testing.T
	db    *db.DB
	store *machines.Store
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
	if err := d.Migrate(context.Background(), machines.Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	store := machines.NewStore(d)
	s := server.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	store.Routes(s)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return &hub{t: t, db: d, store: store, srv: srv}
}

// do sends a request as the reader, or as the agent when token is set, and decodes the response into out.
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
		req.Header.Set("Tailscale-User-Login", reader)
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

func (h *hub) add(name string) string {
	h.t.Helper()
	token, err := h.store.Create(context.Background(), name)
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}

func reading(at time.Time, cpu float64) machines.UsageInput {
	return machines.UsageInput{CPULoad: cpu, MemoryUsedPercent: 40, DiskUsedPercent: 50, UptimeSeconds: 3600, CreatedAt: at}
}

func TestCreateAndRotateToken(t *testing.T) {
	h := newHub(t)
	ctx := context.Background()
	token := h.add("g4f4r0-code")

	// The database keeps only the SHA-256 of the token.
	var hash []byte
	if err := h.db.Read.QueryRow(`SELECT token_hash FROM machines WHERE name = 'g4f4r0-code'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if len(hash) != 32 || bytes.Contains(hash, []byte(token)) {
		t.Errorf("token_hash is %d bytes, want a 32 byte hash", len(hash))
	}

	id, name, err := h.store.Authenticate(ctx, token)
	if err != nil || name != "g4f4r0-code" || id == 0 {
		t.Fatalf("Authenticate = %d %q %v", id, name, err)
	}
	var e *server.Error
	if _, _, err := h.store.Authenticate(ctx, token+"x"); !errors.As(err, &e) || e.Status != 404 {
		t.Errorf("wrong token: %v, want a not found error", err)
	}

	if _, err := h.store.Create(ctx, "g4f4r0-code"); !errors.As(err, &e) || e.Status != 409 {
		t.Errorf("duplicate: %v, want 409", err)
	}
	for _, bad := range []string{"", "Has Caps", "has/slash", "-leading", strings.Repeat("a", 64)} {
		if _, err := h.store.Create(ctx, bad); !errors.As(err, &e) || e.Status != 422 {
			t.Errorf("name %q: %v, want 422", bad, err)
		}
	}

	fresh, err := h.store.RotateToken(ctx, "g4f4r0-code")
	if err != nil || fresh == token {
		t.Fatalf("RotateToken = %q, %v", fresh, err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if res := h.do("POST", "/v1/machines/g4f4r0-code/usage", token, reading(at, 1), nil); res.StatusCode != 401 {
		t.Errorf("old token: %d, want 401", res.StatusCode)
	}
	if res := h.do("POST", "/v1/machines/g4f4r0-code/usage", fresh, reading(at, 1), nil); res.StatusCode != 201 {
		t.Errorf("new token: %d, want 201", res.StatusCode)
	}
	if _, err := h.store.RotateToken(ctx, "nope"); !errors.As(err, &e) || e.Status != 404 {
		t.Errorf("rotate unknown: %v, want 404", err)
	}
}

func TestAddUsageRoute(t *testing.T) {
	h := newHub(t)
	tokenA, _ := h.add("a"), h.add("b")
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

	var u machines.Usage
	res := h.do("POST", "/v1/machines/a/usage", tokenA, reading(at, 0.5), &u)
	if res.StatusCode != 201 || u.ID == 0 || !u.CreatedAt.Equal(at) || u.CPULoad != 0.5 || u.UpdatedAt.Before(time.Now().Add(-time.Minute)) {
		t.Errorf("POST usage: %d %+v; want 201, the agent's created_at and a fresh updated_at", res.StatusCode, u)
	}

	if res := h.do("POST", "/v1/machines/b/usage", tokenA, reading(at, 1), nil); res.StatusCode != 403 {
		t.Errorf("a's token on b: %d, want 403", res.StatusCode)
	}
	if res := h.do("POST", "/v1/machines/a/usage", "", reading(at, 1), nil); res.StatusCode != 401 {
		t.Errorf("reader header on an agent route: %d, want 401", res.StatusCode)
	}
	if res := h.do("POST", "/v1/machines/nope/usage", tokenA, reading(at, 1), nil); res.StatusCode != 404 {
		t.Errorf("unknown machine: %d, want 404", res.StatusCode)
	}

	for name, bad := range map[string]machines.UsageInput{
		"no created_at": reading(time.Time{}, 1),
		"memory > 100":  {MemoryUsedPercent: 101, CreatedAt: at},
		"disk < 0":      {DiskUsedPercent: -1, CreatedAt: at},
		"cpu < 0":       reading(at, -1),
	} {
		if res := h.do("POST", "/v1/machines/a/usage", tokenA, bad, nil); res.StatusCode != 422 {
			t.Errorf("%s: %d, want 422", name, res.StatusCode)
		}
	}
	if res := h.do("POST", "/v1/machines/a/usage", tokenA, map[string]any{"cpu_loud": 1}, nil); res.StatusCode != 400 {
		t.Errorf("unknown field: %d, want 400", res.StatusCode)
	}

	// A retry with the same Idempotency-Key stores one reading.
	for range 2 {
		h.do("POST", "/v1/machines/a/usage", tokenA, reading(at.Add(time.Hour), 2), nil, "Idempotency-Key", "retry-1")
	}
	var list server.List[machines.Usage]
	h.do("GET", "/v1/machines/a/usage", "", nil, &list)
	if len(list.Data) != 2 {
		t.Errorf("%d readings after a retry with one key, want 2", len(list.Data))
	}
}

func TestListUsage(t *testing.T) {
	h := newHub(t)
	token := h.add("a")
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	for i := range 5 {
		h.do("POST", "/v1/machines/a/usage", token, reading(start.Add(time.Duration(i)*time.Minute), float64(i)), nil)
	}

	var all server.List[machines.Usage]
	h.do("GET", "/v1/machines/a/usage", "", nil, &all)
	if len(all.Data) != 5 || all.Data[0].CPULoad != 4 || all.Data[4].CPULoad != 0 || all.NextCursor != "" {
		t.Errorf("all readings = %+v, want 5 newest first and no cursor", all)
	}

	var window server.List[machines.Usage]
	h.do("GET", "/v1/machines/a/usage?since=2026-11-14T10:01:00Z&until=2026-11-14T10:03:00Z", "", nil, &window)
	if len(window.Data) != 3 || window.Data[0].CPULoad != 3 || window.Data[2].CPULoad != 1 {
		t.Errorf("window = %+v, want readings 3, 2, 1", window)
	}

	var seen []float64
	path := "/v1/machines/a/usage?limit=2"
	for range 4 {
		var page server.List[machines.Usage]
		h.do("GET", path, "", nil, &page)
		for _, u := range page.Data {
			seen = append(seen, u.CPULoad)
		}
		if page.NextCursor == "" {
			break
		}
		path = "/v1/machines/a/usage?limit=2&cursor=" + page.NextCursor
	}
	if len(seen) != 5 || seen[0] != 4 || seen[4] != 0 {
		t.Errorf("pages gave %v, want 4 3 2 1 0", seen)
	}

	for _, q := range []string{"since=yesterday", "until=2026-11-14", "limit=0"} {
		if res := h.do("GET", "/v1/machines/a/usage?"+q, "", nil, nil); res.StatusCode != 400 {
			t.Errorf("?%s: %d, want 400", q, res.StatusCode)
		}
	}
	if res := h.do("GET", "/v1/machines/nope/usage", "", nil, nil); res.StatusCode != 404 {
		t.Errorf("unknown machine: %d, want 404", res.StatusCode)
	}
}

func TestOverview(t *testing.T) {
	h := newHub(t)
	tokenA := h.add("a")
	h.add("b")
	newest := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

	var list server.List[machines.Overview]
	if res := h.do("GET", "/v1/machines", "", nil, &list); res.StatusCode != 200 || len(list.Data) != 2 {
		t.Fatalf("GET /v1/machines: %d %+v", res.StatusCode, list)
	}
	if a := list.Data[0]; a.Name != "a" || a.Usage != nil || a.LastSeen != nil {
		t.Errorf("machine without readings = %+v, want null usage and last_seen", a)
	}

	// The reading with the latest created_at counts, whatever order they arrived in.
	h.do("POST", "/v1/machines/a/usage", tokenA, reading(newest, 7), nil)
	h.do("POST", "/v1/machines/a/usage", tokenA, reading(newest.Add(-time.Hour), 3), nil)

	var one machines.Overview
	h.do("GET", "/v1/machines/a", "", nil, &one)
	if one.Usage == nil || one.Usage.CPULoad != 7 || one.LastSeen == nil || !one.LastSeen.Equal(newest) {
		t.Errorf("machine a = %+v, want latest usage cpu 7 and last_seen %v", one, newest)
	}
	var plain machines.Machine
	if res := h.do("GET", "/v1/machines/a", "", nil, &plain); plain.Name != "a" || res.StatusCode != 200 {
		t.Errorf("a Machine doesn't decode from the overview: %+v", plain)
	}

	h.do("GET", "/v1/machines", "", nil, &list)
	if list.Data[0].LastSeen == nil || list.Data[1].LastSeen != nil {
		t.Errorf("list last_seen = %v and %v, want a and nil", list.Data[0].LastSeen, list.Data[1].LastSeen)
	}

	if res := h.do("GET", "/v1/machines/zzz", "", nil, nil); res.StatusCode != 404 {
		t.Errorf("unknown machine: %d, want 404", res.StatusCode)
	}
	req, err := http.NewRequest("GET", h.srv.URL+"/v1/machines", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Errorf("people route without the Tailscale header: %d, want 200", res.StatusCode)
	}
}

func TestCreateMachineRoute(t *testing.T) {
	h := newHub(t)

	var created machines.MachineCreated
	res := h.do("POST", "/v1/machines", "", machines.MachineInput{Name: "g4f4r0-code"}, &created)
	if res.StatusCode != 201 || created.Name != "g4f4r0-code" || created.ID == 0 || created.CreatedAt.IsZero() || len(created.Token) != 43 {
		t.Fatalf("POST /v1/machines: %d %+v, want 201 with the machine and a 43 character token", res.StatusCode, created)
	}
	if loc := res.Header.Get("Location"); loc != "/v1/machines/g4f4r0-code" {
		t.Errorf("Location = %q", loc)
	}
	// The token is the machine's own, and no other response shows it.
	if res := h.do("POST", "/v1/machines/g4f4r0-code/usage", created.Token, reading(time.Now().UTC(), 1), nil); res.StatusCode != 201 {
		t.Errorf("the new token: %d, want 201", res.StatusCode)
	}
	for _, path := range []string{"/v1/machines", "/v1/machines/g4f4r0-code"} {
		var raw json.RawMessage
		h.do("GET", path, "", nil, &raw)
		if bytes.Contains(raw, []byte(created.Token)) || bytes.Contains(raw, []byte("token")) {
			t.Errorf("GET %s shows a token: %s", path, raw)
		}
	}

	var problem struct{ Detail string }
	if res := h.do("POST", "/v1/machines", "", machines.MachineInput{Name: "g4f4r0-code"}, &problem); res.StatusCode != 409 || !strings.Contains(problem.Detail, "rotate") {
		t.Errorf("duplicate: %d %q, want 409 that says what to do", res.StatusCode, problem.Detail)
	}
	for _, bad := range []string{"", "Has Caps", "has/slash"} {
		if res := h.do("POST", "/v1/machines", "", machines.MachineInput{Name: bad}, nil); res.StatusCode != 422 {
			t.Errorf("name %q: %d, want 422", bad, res.StatusCode)
		}
	}
	if res := h.do("POST", "/v1/machines", "", map[string]any{"name": "x", "token": "mine"}, nil); res.StatusCode != 400 {
		t.Errorf("unknown field: %d, want 400", res.StatusCode)
	}
	if res := h.do("POST", "/v1/machines", "", map[string]any{"name": 5}, nil); res.StatusCode != 422 {
		t.Errorf("name of the wrong type: %d, want 422", res.StatusCode)
	}
}

func TestRotateTokenRoute(t *testing.T) {
	h := newHub(t)
	old := h.add("a")
	other := h.add("b")

	var fresh machines.Token
	if res := h.do("POST", "/v1/machines/a/token", "", nil, &fresh); res.StatusCode != 200 || len(fresh.Token) != 43 || fresh.Token == old {
		t.Fatalf("rotate: %d %+v, want 200 and a new token", res.StatusCode, fresh)
	}
	if res := h.do("POST", "/v1/machines/a/usage", old, reading(time.Now().UTC(), 1), nil); res.StatusCode != 401 {
		t.Errorf("old token: %d, want 401", res.StatusCode)
	}
	if res := h.do("POST", "/v1/machines/a/usage", fresh.Token, reading(time.Now().UTC(), 1), nil); res.StatusCode != 201 {
		t.Errorf("new token: %d, want 201", res.StatusCode)
	}
	if res := h.do("POST", "/v1/machines/b/usage", other, reading(time.Now().UTC(), 1), nil); res.StatusCode != 201 {
		t.Errorf("another machine's token changed: %d, want 201", res.StatusCode)
	}
	if res := h.do("POST", "/v1/machines/nope/token", "", nil, nil); res.StatusCode != 404 {
		t.Errorf("unknown machine: %d, want 404", res.StatusCode)
	}
}

func TestDeleteMachineRoute(t *testing.T) {
	h := newHub(t)
	token := h.add("a")
	h.add("b")
	h.do("POST", "/v1/machines/a/usage", token, reading(time.Now().UTC(), 1), nil)

	if res := h.do("DELETE", "/v1/machines/a", "", nil, nil); res.StatusCode != 204 {
		t.Fatalf("delete: %d, want 204", res.StatusCode)
	}
	if res := h.do("GET", "/v1/machines/a", "", nil, nil); res.StatusCode != 404 {
		t.Errorf("deleted machine: %d, want 404", res.StatusCode)
	}
	if res := h.do("POST", "/v1/machines/a/usage", token, reading(time.Now().UTC(), 1), nil); res.StatusCode != 404 && res.StatusCode != 401 {
		t.Errorf("deleted machine's token: %d, want it refused", res.StatusCode)
	}
	var n int
	if err := h.db.Read.QueryRow(`SELECT count(*) FROM machine_usage`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d readings left after the delete, %v; want the cascade to remove them", n, err)
	}
	var list server.List[machines.Overview]
	h.do("GET", "/v1/machines", "", nil, &list)
	if len(list.Data) != 1 || list.Data[0].Name != "b" {
		t.Errorf("machines after the delete = %+v, want only b", list.Data)
	}
	if res := h.do("DELETE", "/v1/machines/a", "", nil, nil); res.StatusCode != 404 {
		t.Errorf("second delete: %d, want 404", res.StatusCode)
	}
}
