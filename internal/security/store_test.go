package security_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/security"
	"github.com/g4f4r0/tender/internal/server"
)

type hub struct {
	store *security.Store
	srv   *httptest.Server
	token string
	id    int64
}

func newHub(t *testing.T) *hub {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	_, err = d.Write.Exec(`CREATE TABLE machines (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, token_hash BLOB NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) STRICT;
		INSERT INTO machines VALUES (1, 'apps', x'00', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background(), security.Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	st := security.NewStore(d)
	st.Allowed = func(context.Context, int64) ([]string, error) { return []string{"41641/udp"}, nil }
	srv := server.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), identity{})
	st.Routes(srv)
	h := &hub{store: st, srv: httptest.NewServer(srv), token: "token-apps", id: 1}
	t.Cleanup(h.srv.Close)
	return h
}

// identity knows the one machine apps, whose token is token-apps.
type identity struct{}

func (identity) Authenticate(_ context.Context, token string) (int64, string, error) {
	if token == "token-apps" {
		return 1, "apps", nil
	}
	return 0, "", server.NotFound("no such token")
}

func (identity) Lookup(_ context.Context, name string) (int64, error) {
	if name == "apps" {
		return 1, nil
	}
	return 0, server.NotFound("Machine %q doesn't exist.", name)
}

func (h *hub) call(t *testing.T, method, path, token string, body any) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if s, ok := body.(string); ok {
			buf.WriteString(s)
		} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func report(at time.Time, pending int, ports ...security.Listener) security.Report {
	tool, deny := "ufw", "deny"
	if ports == nil {
		ports = []security.Listener{}
	}
	return security.Report{
		Firewall:    security.Firewall{Check: "ok", Tool: &tool, Active: true, DefaultIncoming: &deny},
		Listeners:   security.Listeners{Check: "ok", Ports: ports},
		SSH:         security.SSH{Check: "ok"},
		Updates:     security.Updates{Check: "ok", Pending: pending, Security: pending, UnattendedUpgrades: true},
		Reboot:      security.Reboot{Check: "ok"},
		CollectedAt: at,
	}
}

func TestSecurityReportRoundTrip(t *testing.T) {
	h := newHub(t)
	if status, body := h.call(t, "GET", "/v1/machines/apps/security", "", nil); status != 404 || !strings.Contains(body, "hasn't sent a security report yet") {
		t.Fatalf("before a report: %d %s", status, body)
	}
	t0 := time.Now().UTC().Truncate(time.Second)
	tailscale := security.Listener{Protocol: "udp", Address: "0.0.0.0", Port: 41641, Allowed: true}
	web := security.Listener{Protocol: "tcp", Address: "0.0.0.0", Port: 8080, Docker: true}
	if status, body := h.call(t, "PUT", "/v1/machines/apps/security", h.token, report(t0, 2, tailscale, web)); status != 204 {
		t.Fatalf("PUT: %d %s", status, body)
	}
	if status, _ := h.call(t, "PUT", "/v1/machines/apps/security", "", report(t0, 0)); status != 401 {
		t.Errorf("people may not report: %d", status)
	}
	status, body := h.call(t, "GET", "/v1/machines/apps/security", "", nil)
	var st security.State
	if err := json.Unmarshal([]byte(body), &st); status != 200 || err != nil {
		t.Fatalf("GET: %d %s", status, body)
	}
	if st.Machine != "apps" || len(st.Listeners.Ports) != 2 || !st.Listeners.Ports[0].Allowed || st.Listeners.Ports[1].Allowed ||
		st.SecurityUpdatesSince == nil || !st.SecurityUpdatesSince.Equal(t0) || strings.Contains(body, "previous") {
		t.Errorf("state = %s", body)
	}

	// Security updates count from the first report that found them, and stop with one that finds none.
	if status, _ := h.call(t, "PUT", "/v1/machines/apps/security", h.token, report(t0.Add(15*time.Minute), 1)); status != 204 {
		t.Fatal(status)
	}
	all, err := h.store.All(context.Background())
	if err != nil || all[h.id].SecurityUpdatesSince == nil || !all[h.id].SecurityUpdatesSince.Equal(t0) || all[h.id].Previous == nil ||
		len(all[h.id].Previous.Listeners.Ports) != 2 {
		t.Fatalf("all = %+v %v", all[h.id], err)
	}
	if status, _ := h.call(t, "PUT", "/v1/machines/apps/security", h.token, report(t0.Add(30*time.Minute), 0)); status != 204 {
		t.Fatal(status)
	}
	if all, _ := h.store.All(context.Background()); all[h.id].SecurityUpdatesSince != nil {
		t.Errorf("since stays after none were found: %v", all[h.id].SecurityUpdatesSince)
	}

	for name, body := range map[string]any{
		"bad check":  strings.Replace(mustJSON(report(t0, 0)), `"check":"ok"`, `"check":"fine"`, 1),
		"bad port":   report(t0, 0, security.Listener{Protocol: "tcp", Port: 0}),
		"bad proto":  report(t0, 0, security.Listener{Protocol: "sctp", Port: 1}),
		"no time":    report(time.Time{}, 0),
		"bad counts": func() security.Report { r := report(t0, 0); r.Updates.Security = 3; return r }(),
	} {
		if status, resp := h.call(t, "PUT", "/v1/machines/apps/security", h.token, body); status != 422 {
			t.Errorf("%s: %d %s", name, status, resp)
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
