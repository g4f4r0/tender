package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/security"
	"github.com/g4f4r0/tender/internal/server"
)

// A daemon updated before its hub may send fields the hub doesn't know yet: the routes only a
// daemon calls ignore them, so a rollout can update daemons first. The routes people call still
// refuse an unknown field, so a typo there is an error. The daemon routes with a body come from the
// hub's own route list, so a new one can't go untested.
func TestDaemonRoutesTakeUnknownFields(t *testing.T) {
	h := newHub(schema(t), testKey, nil, quiet)
	hub := httptest.NewServer(h)
	defer hub.Close()
	var machine struct{ Token string }
	status, body := restCall(t, hub.URL, "POST", "/v1/machines", "", map[string]any{"name": "apps"})
	if status != 201 || json.Unmarshal([]byte(body), &machine) != nil {
		t.Fatalf("create machine: %d %s", status, body)
	}
	with := func(m map[string]any) map[string]any {
		out := map[string]any{"from_a_newer_daemon": true}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	asMap := func(v any) map[string]any {
		b, err := json.Marshal(v)
		var m map[string]any
		if err != nil || json.Unmarshal(b, &m) != nil {
			t.Fatalf("marshal %T: %v", v, err)
		}
		return m
	}
	tool, deny := "ufw", "deny"
	report := security.Report{
		Firewall:    security.Firewall{Check: "ok", Tool: &tool, Active: true, DefaultIncoming: &deny},
		Listeners:   security.Listeners{Check: "ok", Ports: []security.Listener{}},
		SSH:         security.SSH{Check: "ok"},
		Updates:     security.Updates{Check: "ok", UnattendedUpgrades: true},
		Reboot:      security.Reboot{Check: "ok"},
		CollectedAt: time.Now().UTC().Truncate(time.Second),
	}
	type call struct {
		path string
		body any
		want int
	}
	// Each daemon route that takes a body, by its registered pattern.
	calls := map[string]call{
		"POST /v1/machines/{machine}/usage": {"/v1/machines/apps/usage", with(map[string]any{"cpu_load": 1, "memory_used_percent": 1,
			"disk_used_percent": 1, "uptime_seconds": 1, "created_at": "2026-11-14T03:30:00Z"}), 201},
		"PUT /v1/machines/{machine}/services":          {"/v1/machines/apps/services", with(map[string]any{"docker": "running", "services": []any{}}), 204},
		"PUT /v1/machines/{machine}/backups/snapshots": {"/v1/machines/apps/backups/snapshots", []any{}, 200},
		"POST /v1/machines/{machine}/backups/history": {"/v1/machines/apps/backups/history",
			with(map[string]any{"type": "backup", "status": "in_progress", "created_at": "2026-11-14T03:30:00Z"}), 201},
		"PATCH /v1/machines/{machine}/backups/history/{id}": {"/v1/machines/apps/backups/history/1", with(map[string]any{"status": "completed"}), 200},
		"PATCH /v1/machines/{machine}/actions/{id}":         {"/v1/machines/apps/actions/99", with(map[string]any{"status": "in_progress"}), 404},
		"PUT /v1/machines/{machine}/logs/status":            {"/v1/machines/apps/logs/status", with(map[string]any{"data": []any{}}), 204},
		"PUT /v1/machines/{machine}/security":               {"/v1/machines/apps/security", with(asMap(report)), 204},
		"PUT /v1/machines/{machine}/exposures/status":       {"/v1/machines/apps/exposures/status", with(map[string]any{"tailscale_dns_name": "apps.tail1234.ts.net"}), 204},
	}
	for _, r := range h.Routes() {
		method, _, _ := strings.Cut(r.Pattern, " ")
		if r.Caller != server.CallerDaemon || method == "GET" {
			continue
		}
		c, ok := calls[r.Pattern]
		if !ok {
			t.Errorf("%s is a daemon route with no case here; add one that sends an unknown field", r.Pattern)
			continue
		}
		status, body := restCall(t, hub.URL, method, c.path, machine.Token, c.body)
		if status != c.want || strings.Contains(body, "unknown field") {
			t.Errorf("%s %s: %d %s, want %d", method, c.path, status, body, c.want)
		}
		delete(calls, r.Pattern)
	}
	for p := range calls {
		t.Errorf("%s has a case but the hub has no such daemon route", p)
	}
	if status, body := restCall(t, hub.URL, "POST", "/v1/machines", "", with(map[string]any{"name": "web"})); status != 400 ||
		!strings.Contains(body, "unknown field") {
		t.Errorf("a people route took an unknown field: %d %s", status, body)
	}
}
