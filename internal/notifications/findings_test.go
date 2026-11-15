package notifications

import (
	"testing"
	"time"
)

// Another domain's findings that differ by service, such as one per exposed port, are problems of
// their own, each resolving alone, and none of them starts, resolves or stays because of the
// machine's Docker services. While the machine is silent they stay as they are.
func TestDomainFindingsByServiceAndSilence(t *testing.T) {
	Register(EventType{Name: "example.port_open", Severities: []string{SeverityWarning}, Title: "open", Tool: "list_machines", Fires: "x", Resolves: "y"})
	r := newRig(t)
	ports := func(after time.Duration, ps ...string) Machine {
		m := quiet(after)
		for _, p := range ps {
			m.Findings = append(m.Findings, Finding{Event: "example.port_open", Severity: SeverityWarning, Message: p + " is open.", Service: p})
		}
		return m
	}
	r.tick(0, ports(0, "8080/tcp", "5432/tcp"))
	r.want([]string{"example.port_open warning sent", "example.port_open warning sent"}, "a: 8080/tcp open", "a: 5432/tcp open")

	r.tick(time.Minute, ports(time.Minute, "8080/tcp"))
	r.want([]string{"example.port_open warning sent", "example.port_open warning sent", "example.port_open.resolved warning sent"},
		"a: 8080/tcp open", "a: 5432/tcp open", "a: 5432/tcp open, resolved")

	// Silent: the finding is gone from what the wiring sends, but the problem stays.
	r.tick(20*time.Minute, quiet(time.Minute))
	if rows := r.rows(); len(rows) != 4 || rows[3] != "machine.silent critical sent" {
		t.Errorf("a silent machine changed its port problem: %q", rows)
	}
	// Back, and the port is closed: it resolves.
	r.tick(21*time.Minute, ports(21*time.Minute))
	if rows := r.rows(); len(rows) != 6 || rows[4] != "example.port_open.resolved warning sent" || rows[5] != "machine.silent.resolved critical sent" {
		t.Errorf("rows = %q", rows)
	}
}
