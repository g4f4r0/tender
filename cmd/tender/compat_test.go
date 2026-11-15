package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// A daemon updated before its hub may send fields the hub doesn't know yet: the routes only a
// daemon calls ignore them, so a rollout can update daemons first. The routes people call still
// refuse an unknown field, so a typo there is an error.
func TestDaemonRoutesTakeUnknownFields(t *testing.T) {
	hub := httptest.NewServer(newHub(schema(t), testKey, nil, quiet))
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
	for _, c := range []struct {
		method, path string
		body         any
		want         int
	}{
		{"POST", "/v1/machines/apps/usage", with(map[string]any{"cpu_load": 1, "memory_used_percent": 1, "disk_used_percent": 1, "uptime_seconds": 1,
			"created_at": "2026-11-14T03:30:00Z"}), 201},
		{"PUT", "/v1/machines/apps/services", with(map[string]any{"docker": "running", "services": []any{}}), 204},
		{"PUT", "/v1/machines/apps/backups/snapshots", []any{}, 200},
		{"POST", "/v1/machines/apps/backups/history", with(map[string]any{"type": "backup", "status": "in_progress", "created_at": "2026-11-14T03:30:00Z"}), 201},
		{"PATCH", "/v1/machines/apps/backups/history/1", with(map[string]any{"status": "completed"}), 200},
		{"PATCH", "/v1/machines/apps/actions/99", with(map[string]any{"status": "in_progress"}), 404},
	} {
		status, body := restCall(t, hub.URL, c.method, c.path, machine.Token, c.body)
		if status != c.want || strings.Contains(body, "unknown field") {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, status, body, c.want)
		}
	}
	if status, body := restCall(t, hub.URL, "POST", "/v1/machines", "", with(map[string]any{"name": "web"})); status != 400 ||
		!strings.Contains(body, "unknown field") {
		t.Errorf("a people route took an unknown field: %d %s", status, body)
	}
}
