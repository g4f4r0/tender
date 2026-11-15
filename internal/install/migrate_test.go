package install

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// oldInstall turns a current install into the layout of Tender v0.2.1: tender-agent and tender-hub
// units, /etc/tender/agent.toml and the state in /var/lib/tender/agent.
func oldInstall(t *testing.T, e *env) {
	t.Helper()
	for from, to := range map[string]string{
		"/etc/systemd/system/tenderd.service":   "/etc/systemd/system/tender-agent.service",
		"/etc/systemd/system/tenderhub.service": "/etc/systemd/system/tender-hub.service",
		"/etc/tender/daemon.toml":               "/etc/tender/agent.toml",
	} {
		if err := os.Rename(e.path(from), e.path(to)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(e.path("/var/lib/tender/agent/queue"), 0o700); err != nil {
		t.Fatal(err)
	}
	for p, data := range map[string]string{
		"/var/lib/tender/agent/agent-state.json":    `{"last_backup":"2026-11-15T07:57:01Z"}`,
		"/var/lib/tender/agent/settings.json":       `{"schedule":"30 3 * * *"}`,
		"/var/lib/tender/agent/repository-password": "pw",
		"/var/lib/tender/agent/queue/1.json":        `{"type":"backup"}`,
	} {
		if err := os.WriteFile(e.path(p), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(e *env, p string) bool {
	_, err := os.Lstat(e.path(p))
	return err == nil
}

// A refresh by the new binary, which "tender update" runs, moves a v0.2.1 machine to the new
// names: the old units stop and go, and the daemon keeps its config, saved settings, queued
// reports and the time of its last backup.
func TestRefreshMovesOldServicesToTheirNewNames(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	config := e.read(t, "/etc/tender/daemon.toml")
	oldInstall(t, e)
	e.commands()
	e.out.Reset()

	if daemon, hub := e.in.installedServices(); !daemon || !hub {
		t.Fatalf("installed services = %v, %v; want the old units found", daemon, hub)
	}
	if err := e.in.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := e.commands()
	order := []string{
		"systemctl disable --now tender-agent", "systemctl daemon-reload", "systemctl enable --now tenderd",
		"systemctl disable --now tender-hub", "systemctl daemon-reload", "systemctl enable --now tenderhub",
	}
	at := 0
	for _, c := range got {
		if at < len(order) && c == order[at] {
			at++
		}
	}
	if at != len(order) {
		t.Errorf("commands:\n%s\nwant these in order:\n%s", strings.Join(got, "\n"), strings.Join(order, "\n"))
	}
	for _, p := range []string{"/etc/systemd/system/tender-agent.service", "/etc/systemd/system/tender-hub.service",
		"/etc/tender/agent.toml", "/var/lib/tender/agent"} {
		if exists(e, p) {
			t.Errorf("%s is still there", p)
		}
	}
	for _, u := range []string{"tenderd", "tenderhub"} {
		want, _ := units.ReadFile("units/" + u + ".service")
		if e.read(t, "/etc/systemd/system/"+u+".service") != string(want) {
			t.Errorf("%s.service is not the current unit", u)
		}
	}
	if e.read(t, "/etc/tender/daemon.toml") != config {
		t.Error("daemon.toml is not the old agent.toml")
	}
	for p, want := range map[string]string{
		"/var/lib/tender/daemon/state.json":          `{"last_backup":"2026-11-15T07:57:01Z"}`,
		"/var/lib/tender/daemon/settings.json":       `{"schedule":"30 3 * * *"}`,
		"/var/lib/tender/daemon/repository-password": "pw",
		"/var/lib/tender/daemon/queue/1.json":        `{"type":"backup"}`,
	} {
		if !exists(e, p) || e.read(t, p) != want {
			t.Errorf("%s did not keep its content", p)
		}
	}
	out := e.out.String()
	for _, want := range []string{"Renamed the tender-agent service to tenderd", "Renamed the tender-hub service to tenderhub"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Next:") {
		t.Errorf("a move printed first-install steps:\n%s", out)
	}

	// The next refresh has nothing left to move.
	if err := e.in.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := e.commands(); slices.ContainsFunc(c, func(s string) bool { return strings.Contains(s, "disable") }) {
		t.Errorf("a second refresh moved again: %v", c)
	}
}

// The hub check runs before anything changes, with what the install will use, and only when the
// hub address or the token is new.
func TestDaemonChecksTheHub(t *testing.T) {
	ctx := context.Background()
	var calls [][3]string
	check := func(err error) func(context.Context, string, string, string) error {
		return func(_ context.Context, hub, machine, token string) error {
			calls = append(calls, [3]string{hub, machine, token})
			return err
		}
	}

	e := newEnv(t)
	e.in.CheckHub = check(errors.New("cannot reach the hub"))
	before := e.tree(t)
	if err := e.in.Daemon(ctx, daemonOpts); err == nil || !strings.Contains(err.Error(), "cannot reach the hub") {
		t.Fatalf("err = %v, want the check's error", err)
	}
	if !reflect.DeepEqual(before, e.tree(t)) {
		t.Error("a failed check changed files")
	}

	e.in.CheckHub = check(nil)
	if err := e.in.Daemon(ctx, daemonOpts); err != nil {
		t.Fatal(err)
	}
	if err := e.in.Daemon(ctx, DaemonOptions{}); err != nil { // a repair
		t.Fatal(err)
	}
	if err := e.in.Daemon(ctx, DaemonOptions{Hub: testHub, Machine: "code"}); err != nil { // a repair that keeps the config
		t.Fatal(err)
	}
	if err := e.in.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	e.in.Refreshing = false
	if err := e.in.Daemon(ctx, DaemonOptions{Token: "new-token"}); err != nil { // a new token, the config stays
		t.Fatal(err)
	}
	want := [][3]string{
		{testHub, "code", testToken}, {testHub, "code", testToken},
		{testHub, "code", "new-token"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("checks = %v, want %v", calls, want)
	}
}

// A move that fails before the new service runs keeps the stopped old unit, so running the refresh
// again, as the update's error says, finishes the move with the config and state intact.
func TestFailedMoveCanBeRunAgain(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	config := e.read(t, "/etc/tender/daemon.toml")
	oldInstall(t, e)
	if err := os.Remove(e.path("/etc/systemd/system/tender-hub.service")); err != nil { // a daemon-only machine
		t.Fatal(err)
	}
	e.fake.failOnce = "systemctl enable --now tenderd"
	if err := e.in.Refresh(context.Background()); err == nil {
		t.Fatal("the refresh did not fail")
	}
	if !exists(e, "/etc/systemd/system/tender-agent.service") {
		t.Fatal("the old unit is gone before tenderd ran, so nothing finds the install any more")
	}
	if err := e.in.Refresh(context.Background()); err != nil {
		t.Fatalf("running the refresh again: %v", err)
	}
	if exists(e, "/etc/systemd/system/tender-agent.service") || e.read(t, "/etc/tender/daemon.toml") != config ||
		e.read(t, "/var/lib/tender/daemon/state.json") != `{"last_backup":"2026-11-15T07:57:01Z"}` {
		t.Error("the second refresh did not finish the move with the config and state intact")
	}
}

// On a v0.2.1 machine, an install that fails a check leaves tender-agent running and every file
// where it was.
func TestOldDaemonKeepsRunningWhenACheckFails(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		setup func(e *env)
		want  string
	}{
		{"the hub check", func(e *env) {
			e.in.CheckHub = func(context.Context, string, string, string) error { return errors.New("cannot reach the hub") }
		}, "cannot reach the hub"},
		{"a config under both names", func(e *env) {
			if err := os.WriteFile(e.path("/etc/tender/daemon.toml"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "move the other away"},
		{"a state folder under both names", func(e *env) {
			if err := os.MkdirAll(e.path("/var/lib/tender/daemon"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "move the other away"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			installBoth(t, e)
			oldInstall(t, e)
			c.setup(e)
			before := e.tree(t)
			err := e.in.Daemon(ctx, DaemonOptions{Hub: testHub, Machine: "code", Token: "new-token", Force: true})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if got := e.commands(); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "systemctl") }) {
				t.Errorf("a failed check ran systemctl: %v", got)
			}
			if !reflect.DeepEqual(before, e.tree(t)) {
				t.Error("a failed check changed files")
			}
		})
	}
}

// Each old service stops right before its new one starts, so a step that fails earlier leaves
// the old one running.
func TestOldServicesStopRightBeforeTheNewOnesStart(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	oldInstall(t, e)
	e.commands()
	if err := e.in.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Giving the moved state folder to root, as the daemon runs now, happens while neither runs.
	got := slices.DeleteFunc(e.commands(), func(c string) bool { return strings.HasPrefix(c, "chown -R root:root ") })
	for _, c := range []struct{ old, next string }{
		{"systemctl disable --now tender-agent", "systemctl enable --now tenderd"},
		{"systemctl disable --now tender-hub", "systemctl enable --now tenderhub"},
	} {
		i := slices.Index(got, c.old)
		if i < 0 || i+2 >= len(got) || got[i+1] != "systemctl daemon-reload" || got[i+2] != c.next {
			t.Errorf("want %q, daemon-reload, %q in a row, got:\n%s", c.old, c.next, strings.Join(got, "\n"))
		}
	}
}
