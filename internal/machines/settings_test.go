package machines_test

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/machines"
)

func TestPatchMachineSettings(t *testing.T) {
	h := newHub(t)
	h.add("apps")
	var m machines.Overview
	if res := h.do(http.MethodGet, "/v1/machines/apps", "", nil, &m); res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	if m.Updates != "inherit" || !m.SecurityAlerts || m.SecurityAllowedPorts == nil || len(m.SecurityAllowedPorts) != 0 || m.Version != nil {
		t.Fatalf("defaults = %+v", m.Machine)
	}
	res := h.do(http.MethodPatch, "/v1/machines/apps", "", map[string]any{"updates": "manual", "security_allowed_ports": []string{"443/tcp", "41641/udp", "443/tcp"}}, &m)
	if res.StatusCode != 200 || m.Updates != "manual" || !reflect.DeepEqual(m.SecurityAllowedPorts, []string{"41641/udp", "443/tcp"}) || !m.SecurityAlerts {
		t.Fatalf("%s %+v", res.Status, m.Machine)
	}
	// A merge patch keeps what it leaves out.
	if res := h.do(http.MethodPatch, "/v1/machines/apps", "", map[string]any{"security_alerts": false}, &m); res.StatusCode != 200 ||
		m.Updates != "manual" || len(m.SecurityAllowedPorts) != 2 || m.SecurityAlerts {
		t.Fatalf("%s %+v", res.Status, m.Machine)
	}
	for name, body := range map[string]any{
		"bad mode":      map[string]any{"updates": "sometimes"},
		"bad port":      map[string]any{"security_allowed_ports": []string{"41641"}},
		"port too high": map[string]any{"security_allowed_ports": []string{"70000/tcp"}},
		"port zero":     map[string]any{"security_allowed_ports": []string{"0/udp"}},
	} {
		if res := h.do(http.MethodPatch, "/v1/machines/apps", "", body, nil); res.StatusCode != 422 {
			t.Errorf("%s: %s, want 422", name, res.Status)
		}
	}
	if res := h.do(http.MethodPatch, "/v1/machines/apps", "", map[string]any{"name": "x"}, nil); res.StatusCode != 400 {
		t.Errorf("unknown field: %s, want 400", res.Status)
	}
	if res := h.do(http.MethodPatch, "/v1/machines/nope", "", map[string]any{"updates": "auto"}, nil); res.StatusCode != 404 {
		t.Errorf("unknown machine: %s", res.Status)
	}
}

func TestUsageRecordsTheDaemonsVersion(t *testing.T) {
	h := newHub(t)
	token := h.add("apps")
	in := reading(time.Now().UTC().Truncate(time.Second), 0.5)
	in.Version = "0.3.1"
	if res := h.do(http.MethodPost, "/v1/machines/apps/usage", token, in, nil); res.StatusCode != 201 {
		t.Fatal(res.Status)
	}
	var m machines.Overview
	h.do(http.MethodGet, "/v1/machines/apps", "", nil, &m)
	if m.Version == nil || *m.Version != "0.3.1" {
		t.Fatalf("version = %v", m.Version)
	}
	// An older daemon sends no version, and the last one stays.
	if res := h.do(http.MethodPost, "/v1/machines/apps/usage", token, reading(time.Now().UTC(), 0.5), nil); res.StatusCode != 201 {
		t.Fatal(res.Status)
	}
	h.do(http.MethodGet, "/v1/machines/apps", "", nil, &m)
	if m.Version == nil || *m.Version != "0.3.1" {
		t.Fatalf("version = %v", m.Version)
	}
	in.Version = "../../x"
	if res := h.do(http.MethodPost, "/v1/machines/apps/usage", token, in, nil); res.StatusCode != 422 {
		t.Errorf("bad version: %s", res.Status)
	}
}
