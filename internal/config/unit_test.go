package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// FakeSystemd puts the fake systemd-run and systemctl in place for this test, and returns the
// folder where they keep the units' state and their log of calls.
func fakeSystemd(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs("testdata/fakesystemd")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("FAKE_SYSTEMD_DIR", dir)
	t.Setenv(EnvTestSystemdRun, filepath.Join(bin, "systemd-run"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// A transient unit runs without --wait or --pipe, which need D-Bus; its output goes to a file and
// its result comes from systemctl show, and it stays until it is cleared.
func TestTransientUnit(t *testing.T) {
	dir := fakeSystemd(t)
	ctx := context.Background()
	log := filepath.Join(t.TempDir(), "job.log")
	if u, err := ShowUnit(ctx, "tenderjob"); err != nil || u.Exists() {
		t.Fatalf("before: %+v %v", u, err)
	}
	if err := StartUnit(ctx, "tenderjob", log, []string{"GREETING=hello"}, "/bin/sh", "-c", `echo "$GREETING"; sleep 0.3; echo done`); err != nil {
		t.Fatal(err)
	}
	if u, err := ShowUnit(ctx, "tenderjob"); err != nil || !u.Running() {
		t.Errorf("while it runs: %+v %v", u, err)
	}
	if err := StartUnit(ctx, "tenderjob", log, nil, "true"); err == nil {
		t.Error("started a second unit with the same name")
	}
	u, err := WaitUnit(ctx, "tenderjob", 20*time.Millisecond)
	if err != nil || u.Running() || !u.Succeeded() {
		t.Fatalf("after: %+v %v", u, err)
	}
	if b, _ := os.ReadFile(log); string(b) != "hello\ndone\n" {
		t.Errorf("output = %q", b)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "log"))
	if strings.Contains(string(calls), "--wait") || strings.Contains(string(calls), "--pipe") {
		t.Errorf("calls = %s", calls)
	}
	ClearUnit(ctx, "tenderjob")
	if u, _ := ShowUnit(ctx, "tenderjob"); u.Exists() {
		t.Errorf("after clearing: %+v", u)
	}

	if err := StartUnit(ctx, "tenderjob", log, nil, "/bin/sh", "-c", "exit 3"); err != nil {
		t.Fatal(err)
	}
	u, err = WaitUnit(ctx, "tenderjob", 20*time.Millisecond)
	if err != nil || u.Succeeded() || u.Status != 3 || u.Exists() == false {
		t.Errorf("failed unit: %+v %v", u, err)
	}

	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	ClearUnit(ctx, "tenderjob")
	if err := StartUnit(ctx, "tenderjob", log, nil, "sleep", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := WaitUnit(cctx, "tenderjob", 10*time.Millisecond); err == nil {
		t.Error("the wait outlived its context")
	}
	if u, _ := ShowUnit(ctx, "tenderjob"); !u.Running() {
		t.Error("the end of the wait stopped the unit")
	}
}
