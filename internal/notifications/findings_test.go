package notifications

import (
	"sync"
	"testing"
	"time"
)

// registerDomainEvents registers, once per test binary, the events cmd/tender registers for the
// logs, security and updates domains, with the same names and severities.
var registerDomainEvents = sync.OnceFunc(func() {
	for name, severity := range domainEvents {
		Register(EventType{Name: name, Severities: []string{severity}, Title: name, Tool: "list_machines", Fires: "x", Resolves: "y"})
	}
})

var domainEvents = map[string]string{
	"security.firewall_off":     SeverityCritical,
	"security.port_exposed":     SeverityWarning,
	"security.ssh_weak":         SeverityWarning,
	"security.updates_pending":  SeverityWarning,
	"security.auto_updates_off": SeverityWarning,
	"security.reboot_required":  SeverityWarning,
	"update.failed":             SeverityWarning,
	"logs.shipping_failing":     SeverityWarning,
}

// A silent machine fires none of the findings other domains send for it: they come from its last
// reports, as old as the silence, and machine.silent already says so.
func TestSilentMachineFiresNoFindings(t *testing.T) {
	registerDomainEvents()
	r := newRig(t)
	m := silent()
	for name, severity := range domainEvents {
		f := Finding{Event: name, Severity: severity, Message: name + " on a."}
		if name == "security.port_exposed" {
			f.Service = "8080/tcp"
		}
		m.Findings = append(m.Findings, f)
	}
	r.tick(0, m)
	r.want([]string{"machine.silent critical sent"}, "a: not reporting")

	// Back and reporting with the same findings, they fire.
	back := quiet(time.Minute)
	back.Findings = m.Findings
	r.tick(time.Minute, back)
	if rows := r.rows(); len(rows) != 2+len(domainEvents) {
		t.Errorf("after the machine is back, rows = %q, want machine.silent, its resolve and the %d findings", rows, len(domainEvents))
	}
}

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
