package notifications

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// clean is a report that finds nothing wrong.
func clean() *SecurityReport {
	return &SecurityReport{FirewallChecked: true, FirewallOn: true, ListenersChecked: true, SSHChecked: true,
		UpdatesChecked: true, AutoUpdates: true, RebootChecked: true}
}

func secured(after time.Duration, latest, previous *SecurityReport) Machine {
	m := quiet(after)
	m.Security = &Security{AlertsEnabled: true, AllowedPorts: []string{"41641/udp"}, Latest: latest, Previous: previous}
	return m
}

func TestSystemRules(t *testing.T) {
	off := func(r *SecurityReport) *SecurityReport { r.FirewallOn, r.Firewall = false, "ufw is inactive"; return r }
	weak := func(r *SecurityReport) *SecurityReport { r.SSHPasswords, r.SSH = true, SSHText(true, "no"); return r }
	manual := func(r *SecurityReport) *SecurityReport { r.AutoUpdates = false; return r }
	ports := func(ls ...string) func(r *SecurityReport) *SecurityReport {
		return func(r *SecurityReport) *SecurityReport {
			for _, l := range ls {
				r.Listeners = append(r.Listeners, Listener{Port: l, Address: "0.0.0.0"})
			}
			return r
		}
	}
	unsupported := &SecurityReport{}
	cases := []struct {
		name string
		m    Machine
		want []string
	}{
		{"clean", secured(0, clean(), clean()), []string{}},
		{"one report", secured(0, off(clean()), clean()), []string{}},
		{"firewall off twice", secured(0, off(clean()), off(clean())), []string{"firewall_off:critical"}},
		{"unsupported", secured(0, unsupported, unsupported), []string{}},
		{"unchecked firewall", secured(0, off(&SecurityReport{}), off(&SecurityReport{})), []string{}},
		{"ssh", secured(0, weak(clean()), weak(clean())), []string{"ssh_weak:warning"}},
		{"auto updates", secured(0, manual(clean()), manual(clean())), []string{"auto_updates_off:warning"}},
		{"ports", secured(0, ports("8080/tcp", "41641/udp", "5432/tcp")(clean()), ports("8080/tcp", "41641/udp")(clean())),
			[]string{"port_exposed:warning"}},
		{"first report", func() Machine { m := secured(0, off(clean()), nil); return m }(), []string{}},
		{"alerts off", func() Machine { m := secured(0, off(clean()), off(clean())); m.Security.AlertsEnabled = false; return m }(), []string{}},
		{"silent", secured(-time.Hour, off(clean()), off(clean())), []string{"machine_silent:critical"}},
		{"update failed", func() Machine {
			m := quiet(0)
			m.UpdateFailed = &UpdateFailure{Version: "0.3.1", Reason: "It failed its health check."}
			return m
		}(), []string{"update_failed:warning"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summary(evaluate(c.m, t0)); !slices.Equal(got, c.want) {
				t.Errorf("findings = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSecurityUpdatesAndRebootWait(t *testing.T) {
	pending := func(since, reboot time.Duration) Machine {
		r := clean()
		r.SecurityUpdates = 4
		r.RebootSince = ptr(t0.Add(-reboot))
		m := secured(0, r, clean())
		m.Security.SecurityUpdatesSince = ptr(t0.Add(-since))
		return m
	}
	if got := summary(evaluate(pending(71*time.Hour, 6*24*time.Hour), t0)); len(got) != 0 {
		t.Errorf("before 3 and 7 days: %q", got)
	}
	got := evaluate(pending(73*time.Hour, 8*24*time.Hour), t0)
	if s := summary(got); !slices.Equal(s, []string{"security_updates_pending:warning", "reboot_required:warning"}) {
		t.Fatalf("after 3 and 7 days: %q", s)
	}
	if !strings.Contains(got[0].content, "tender machines upgrade a") || !strings.Contains(got[0].content, "4 security updates") ||
		!strings.Contains(got[1].content, "never reboots") {
		t.Errorf("contents = %q / %q", got[0].content, got[1].content)
	}
}

func TestPortText(t *testing.T) {
	plain := portText("a", Listener{Port: "5432/tcp", Address: "0.0.0.0", Process: "postgres"})
	docker := portText("a", Listener{Port: "8080/tcp", Address: "0.0.0.0", Docker: true, Service: "web"})
	if !strings.Contains(plain, "(postgres)") || !strings.HasSuffix(plain, "tender machines set a --allow-port 5432/tcp") {
		t.Errorf("plain = %q", plain)
	}
	if !strings.Contains(docker, "the container web") || !strings.Contains(docker, "around ufw") {
		t.Errorf("docker = %q", docker)
	}
	if got := SSHText(true, "yes"); !strings.Contains(got, "password login") || !strings.Contains(got, "root") {
		t.Errorf("SSHText = %q", got)
	}
}

// One alert per exposed port, each resolving on its own; security alerts stay while the machine is silent.
func TestSystemAlertsOpenAndResolve(t *testing.T) {
	r := newRig(t)
	two := func(ls ...string) *SecurityReport {
		rep := clean()
		for _, l := range ls {
			rep.Listeners = append(rep.Listeners, Listener{Port: l, Address: "0.0.0.0"})
		}
		return rep
	}
	r.tick(0, secured(0, two("8080/tcp", "5432/tcp"), two("8080/tcp", "5432/tcp")))
	r.want([]string{"port_exposed warning sent", "port_exposed warning sent"}, "a: 8080/tcp exposed", "a: 5432/tcp exposed")

	r.tick(time.Minute, secured(time.Minute, two("8080/tcp"), two("8080/tcp")))
	r.want([]string{"port_exposed warning sent", "port_exposed warning resolved"}, "a: 8080/tcp exposed", "a: 5432/tcp exposed", "a: 5432/tcp exposed, resolved")

	m := secured(time.Minute, clean(), clean())
	r.tick(20*time.Minute, m) // silent: the open alert stays
	if rows := r.rows(); rows[0] != "port_exposed warning sent" {
		t.Errorf("silent machine resolved its port alert: %q", rows)
	}
	r.tick(21*time.Minute, secured(21*time.Minute, clean(), clean()))
	if rows := r.rows(); rows[0] != "port_exposed warning resolved" {
		t.Errorf("a clean report kept the alert: %q", rows)
	}
}
