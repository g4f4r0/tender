package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/client"
)

// The tests ask no real systemd: without a fake, systemctl is missing, so Docker counts as wanted.
func TestMain(m *testing.M) {
	systemctl = "/nonexistent/systemctl"
	os.Exit(m.Run())
}

// fakeSystemctl makes systemctl a script that runs body, and returns the file that logs its
// arguments.
func fakeSystemctl(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$*\" >>" + args + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	was := systemctl
	systemctl = filepath.Join(dir, "systemctl")
	t.Cleanup(func() { systemctl = was })
	return args
}

// refusingSocket is a socket file that nothing listens on, as Docker leaves behind once stopped.
func refusingSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "docker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	return socket
}

func TestTurnedOff(t *testing.T) {
	for _, c := range []struct {
		name, body string
		off        bool
	}{
		{"both disabled", "printf 'disabled\\ndisabled\\n'; exit 1", true},
		{"masked and unknown", "printf 'masked\\nnot-found\\n'; exit 4", true},
		{"neither known, by an older systemd", "for u in docker.service docker.socket; do " +
			"echo \"Failed to get unit file state for $u: No such file or directory\" >&2; done; exit 1", true},
		{"the socket enabled", "printf 'disabled\\nenabled\\n'", false},
		{"the engine static", "printf 'static\\ndisabled\\n'", false},
		{"indirect", "printf 'indirect\\ndisabled\\n'", false},
		{"generated", "printf 'disabled\\ngenerated\\n'", false},
		{"alias", "printf 'alias\\ndisabled\\n'", false},
		{"enabled for this boot", "printf 'enabled-runtime\\ndisabled\\n'", false},
		{"one answer for two units", "printf 'disabled\\n'; exit 1", false},
		{"no bus", "echo 'Failed to connect to bus: No such file or directory' >&2; exit 1", false},
		{"killed", "kill -9 $$", false},
		{"stuck", "sleep 10", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func(was time.Duration) { systemdTimeout = was }(systemdTimeout)
			systemdTimeout = 200 * time.Millisecond
			args := fakeSystemctl(t, c.body)
			start := time.Now()
			if got := turnedOff(context.Background()); got != c.off {
				t.Errorf("turnedOff = %v, want %v", got, c.off)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("took %s", d)
			}
			if b, _ := os.ReadFile(args); string(b) != "is-enabled docker.service docker.socket\n" {
				t.Errorf("systemctl was run with %q", b)
			}
		})
	}
	systemctl = "/nonexistent/systemctl"
	if turnedOff(context.Background()) {
		t.Error("without systemctl, Docker counts as turned off")
	}
}

// A refusing or missing socket on a machine whose Docker is turned off pings as errOff, and keeps
// what it found; a socket that answers never asks systemd.
func TestPingTurnedOff(t *testing.T) {
	ctx := context.Background()
	args := fakeSystemctl(t, "printf 'disabled\\ndisabled\\n'; exit 1")
	refusing := refusingSocket(t)
	if err := NewDocker(refusing).Ping(ctx); !errors.Is(err, errOff) || !errors.Is(err, errUnreachable) {
		t.Errorf("refused, turned off: %v, want errOff and errUnreachable", err)
	}
	missing := filepath.Join(t.TempDir(), "docker.sock")
	if err := NewDocker(missing).Ping(ctx); !errors.Is(err, errOff) || !errors.Is(err, errNoDocker) {
		t.Errorf("missing, turned off: %v, want errOff and errNoDocker", err)
	}
	_ = os.Remove(args)
	f := newFakeDocker(t)
	if err := NewDocker(f.socket).Ping(ctx); err != nil {
		t.Errorf("Docker answers: %v", err)
	}
	if _, err := os.Stat(args); err == nil {
		t.Error("a Docker that answers asked systemd")
	}

	fakeSystemctl(t, "printf 'enabled\\nenabled\\n'")
	if err := NewDocker(refusing).Ping(ctx); errors.Is(err, errOff) || !errors.Is(err, errUnreachable) {
		t.Errorf("refused, wanted: %v, want errUnreachable only", err)
	}
}

// On a machine whose Docker is turned off, with a stale socket left over, the daemon tells the
// hub Docker is off, though it never saw Docker: the hub forgets the machine's Docker and its
// services. It logs that once, and says so again only after it changed.
func TestDaemonDockerOff(t *testing.T) {
	h := newTestHub(t)
	ctx := context.Background()
	s := NewStore(h.db)
	if err := s.Replace(ctx, 1, []ContainerInput{running("web", "c1")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Unreachable(ctx, 1, ReasonRefused); err != nil {
		t.Fatal(err)
	}
	fakeSystemctl(t, "printf 'disabled\\ndisabled\\n'; exit 1")
	var logs syncBuffer
	d := NewDaemon(client.New(h.URL, "token-a"), "a", refusingSocket(t), slog.New(slog.NewTextHandler(&logs, nil)))
	d.StateDir = t.TempDir()
	d.Every = 50 * time.Millisecond
	rctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	d.Run(rctx)
	ms, err := s.Machines(ctx, 1)
	if err != nil || ms[0].Docker != nil || ms[0].DockerReason != nil || ms[0].ReportedAt() != nil || ms[0].Data[0].Containers[0].State != StateRemoved {
		t.Errorf("after Docker is off: %+v, %v", ms, err)
	}
	if n := strings.Count(logs.String(), `msg="Docker is turned off on this machine (docker.service and docker.socket aren't enabled), so Tender doesn't watch its services."`); n != 1 ||
		!strings.Contains(logs.String(), "level=INFO") || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("logged %d times:\n%s", n, logs.String())
	}
	if !strings.Contains(h.logs.String(), "status=204") {
		t.Errorf("the hub took no report:\n%s", h.logs.String())
	}
}

// A machine without Docker and without its socket sends nothing, also when systemd doesn't know
// Docker either.
func TestDaemonWithoutDockerAndUnits(t *testing.T) {
	h := newTestHub(t)
	fakeSystemctl(t, "printf 'not-found\\nnot-found\\n'; exit 4")
	var logs syncBuffer
	d := NewDaemon(client.New(h.URL, "token-a"), "a", filepath.Join(t.TempDir(), "docker.sock"), slog.New(slog.NewTextHandler(&logs, nil)))
	d.reportNow(context.Background())
	d.reportNow(context.Background())
	if strings.Contains(h.logs.String(), "path=") {
		t.Errorf("a machine without Docker called the hub:\n%s", h.logs.String())
	}
	if n := strings.Count(logs.String(), `msg="this machine has no Docker"`); n != 1 {
		t.Errorf("logged %d times:\n%s", n, logs.String())
	}
}

// A daemon that saw Docker, whose Docker is then turned off and its socket removed, says off, not
// that the socket is gone.
func TestDaemonSawDockerThenOff(t *testing.T) {
	h := newTestHub(t)
	f := newFakeDocker(t, &fakeContainer{ID: "aaa", Name: "web", State: "running"})
	d := NewDaemon(client.New(h.URL, "token-a"), "a", f.socket, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	d.reportNow(ctx)
	if err := os.Remove(f.socket); err != nil {
		t.Fatal(err)
	}
	d.Runtime.(*Docker).http.CloseIdleConnections()
	fakeSystemctl(t, "printf 'masked\\nmasked\\n'; exit 1")
	d.reportNow(ctx)
	ms, err := NewStore(h.db).Machines(ctx, 1)
	if err != nil || ms[0].Docker != nil || ms[0].Data[0].Containers[0].State != StateRemoved {
		t.Errorf("after Docker was turned off: %+v, %v", ms, err)
	}
}
