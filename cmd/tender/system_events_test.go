package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/notifications"
)

var sysNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// clean is a report that finds nothing wrong.
func clean() *securityFacts {
	return &securityFacts{FirewallChecked: true, FirewallOn: true, ListenersChecked: true, SSHChecked: true,
		UpdatesChecked: true, AutoUpdates: true, RebootChecked: true}
}

func secured(latest, previous *securityFacts) *machineSecurity {
	return &machineSecurity{Enabled: true, AllowedPorts: []string{"41641/udp"}, Latest: latest, Previous: previous}
}

func names(fs []notifications.Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Event+":"+f.Severity+":"+f.Service)
	}
	return out
}

func TestSecurityFindings(t *testing.T) {
	off := func(r *securityFacts) *securityFacts { r.FirewallOn, r.Firewall = false, "ufw is inactive"; return r }
	weak := func(r *securityFacts) *securityFacts { r.SSHPasswords, r.SSH = true, sshWeakText(true, "no"); return r }
	manual := func(r *securityFacts) *securityFacts { r.AutoUpdates = false; return r }
	ports := func(ls ...string) func(r *securityFacts) *securityFacts {
		return func(r *securityFacts) *securityFacts {
			for _, l := range ls {
				r.Listeners = append(r.Listeners, listener{Port: l, Address: "0.0.0.0"})
			}
			return r
		}
	}
	unsupported := &securityFacts{}
	cases := []struct {
		name string
		s    *machineSecurity
		want []string
	}{
		{"clean", secured(clean(), clean()), []string{}},
		{"one report", secured(off(clean()), clean()), []string{}},
		{"firewall off twice", secured(off(clean()), off(clean())), []string{"security.firewall_off:critical:"}},
		{"unsupported", secured(unsupported, unsupported), []string{}},
		{"unchecked firewall", secured(off(&securityFacts{}), off(&securityFacts{})), []string{}},
		{"ssh", secured(weak(clean()), weak(clean())), []string{"security.ssh_weak:warning:"}},
		{"auto updates", secured(manual(clean()), manual(clean())), []string{"security.auto_updates_off:warning:"}},
		{"ports, each its own, the allowed one and a new one left out", secured(ports("8080/tcp", "41641/udp", "5432/tcp")(clean()), ports("8080/tcp", "41641/udp")(clean())),
			[]string{"security.port_exposed:warning:8080/tcp"}},
		{"first report", secured(off(clean()), nil), []string{}},
		{"notifications off", func() *machineSecurity { s := secured(off(clean()), off(clean())); s.Enabled = false; return s }(), []string{}},
		{"no reports", nil, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := names(securityFindings("a", c.s, sysNow)); !slices.Equal(got, c.want) {
				t.Errorf("findings = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSecurityUpdatesAndRebootWait(t *testing.T) {
	pending := func(since, reboot time.Duration) *machineSecurity {
		r := clean()
		r.SecurityUpdates = 4
		r.RebootSince = ptrTime(sysNow.Add(-reboot))
		s := secured(r, clean())
		s.SecurityUpdatesSince = ptrTime(sysNow.Add(-since))
		return s
	}
	if got := securityFindings("a", pending(71*time.Hour, 6*24*time.Hour), sysNow); len(got) != 0 {
		t.Errorf("before 3 and 7 days: %q", names(got))
	}
	got := securityFindings("a", pending(73*time.Hour, 8*24*time.Hour), sysNow)
	if s := names(got); !slices.Equal(s, []string{"security.updates_pending:warning:", "security.reboot_required:warning:"}) {
		t.Fatalf("after 3 and 7 days: %q", s)
	}
	if !strings.Contains(got[0].Message, "tender machines upgrade a") || !strings.Contains(got[0].Message, "4 security updates") ||
		!strings.Contains(got[1].Message, "never reboots") {
		t.Errorf("messages = %q / %q", got[0].Message, got[1].Message)
	}
}

func TestUpdateFindings(t *testing.T) {
	if got := updateFindings("a", nil); got != nil {
		t.Errorf("no failure: %v", got)
	}
	got := updateFindings("a", &updateFailure{Version: "0.3.1", Reason: "It failed its health check."})
	if s := names(got); !slices.Equal(s, []string{"update.failed:warning:"}) || got[0].Message != "Tender could not update a to 0.3.1. It failed its health check." {
		t.Errorf("findings = %+v", got)
	}
}

func TestPortText(t *testing.T) {
	plain := portText("a", listener{Port: "5432/tcp", Address: "0.0.0.0", Process: "postgres"})
	docker := portText("a", listener{Port: "8080/tcp", Address: "0.0.0.0", Docker: true, Service: "web"})
	if !strings.Contains(plain, "(postgres)") || !strings.HasSuffix(plain, "tender machines set a --allow-port 5432/tcp") {
		t.Errorf("plain = %q", plain)
	}
	if !strings.Contains(docker, "the container web") || !strings.Contains(docker, "around ufw") {
		t.Errorf("docker = %q", docker)
	}
	if got := sshWeakText(true, "yes"); !strings.Contains(got, "password login") || !strings.Contains(got, "root") {
		t.Errorf("sshWeakText = %q", got)
	}
}

// Every event the other domains fire is registered with the severities their findings use, so
// the rules take them, and the docs list them (TestDocsListEveryEvent).
func TestDomainEventsRegistered(t *testing.T) {
	want := map[string]string{
		eventLogsShippingFailing: "warning", eventFirewallOff: "critical", eventPortExposed: "warning", eventSSHWeak: "warning",
		eventSecurityUpdatesPending: "warning", eventAutoUpdatesOff: "warning", eventRebootRequired: "warning", eventUpdateFailed: "warning",
	}
	if len(domainEvents) != len(want) {
		t.Errorf("domainEvents has %d events, want %d", len(domainEvents), len(want))
	}
	for name, severity := range want {
		i := slices.IndexFunc(notifications.Events(), func(e notifications.EventType) bool { return e.Name == name })
		if i < 0 || !slices.Equal(notifications.Events()[i].Severities, []string{severity}) {
			t.Errorf("%s is not registered as %s", name, severity)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
