package updates

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/config"
)

// fakeSystemd puts the fake systemd-run and systemctl in place, and returns their folder.
func fakeSystemd(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs("../config/testdata/fakesystemd")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("FAKE_SYSTEMD_DIR", dir)
	t.Setenv(config.EnvTestSystemdRun, filepath.Join(bin, "systemd-run"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// job writes a stand-in for tender that, as update --apply VERSION, runs script.
func job(t *testing.T, script string) Job {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tender")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nstate="+filepath.Join(dir, "state")+"\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Job{Binary: bin, StateDir: filepath.Join(dir, "state")}
}

// The job runs tender update --apply in the transient unit tenderupdate, without --wait, and the
// wait for it ends when it leaves its result, or ends without one.
func TestJob(t *testing.T) {
	sd := fakeSystemd(t)
	jobEvery = 20 * time.Millisecond
	ctx := context.Background()
	j := job(t, `echo "Updating to $3"; sleep 0.2; printf '{"version":"%s","status":"completed","output":"Updated."}' "$3" >$state/update-result.json`)
	if err := config.WriteUpdateResult(j.StateDir, config.UpdateResult{Version: "0.3.0", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Update(ctx, "0.3.1"); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(sd, "log"))
	if want := "systemd-run --unit tenderupdate  " + j.Binary + " update --apply 0.3.1\n"; !strings.Contains(string(calls), want) || strings.Contains(string(calls), "--wait") {
		t.Errorf("calls:\n%s\nwant %s", calls, want)
	}
	if status, output, ok := j.UpdateResult("0.3.1"); !ok || status != "completed" || output != "Updated." {
		t.Errorf("result = %s %s %v", status, output, ok)
	}
	if b, _ := os.ReadFile(filepath.Join(j.StateDir, UpdateLog)); !strings.Contains(string(b), "Updating to 0.3.1") {
		t.Errorf("log = %q", b)
	}
	if _, _, ok := j.UpdateResult("0.3.0"); ok {
		t.Error("a stale result stayed")
	}
	if _, _, ok := j.UpdateResult("0.3.2"); ok {
		t.Error("the result of another version counted")
	}
	for range 50 {
		if !j.Running(ctx) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	j.ClearUpdateResult()
	if _, _, ok := j.UpdateResult("0.3.1"); ok {
		t.Error("the result stayed after ClearUpdateResult")
	}
	if u, _ := config.ShowUnit(ctx, UpdateUnit); u.Exists() {
		t.Errorf("the unit stayed: %+v", u)
	}

	// A job that ends without a result fails with its last line.
	j = job(t, `echo "tender: cannot download Tender v0.3.9"; exit 1`)
	if err := j.Update(ctx, "0.3.9"); err == nil || !strings.Contains(err.Error(), "cannot download Tender v0.3.9") {
		t.Errorf("failed job: %v", err)
	}
	j.ClearUpdateResult()

	// While the job runs, Running says so; the wait ends with its context and the job runs on.
	j = job(t, `sleep 1`)
	cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := j.Update(cctx, "0.3.1"); err == nil {
		t.Error("the wait outlived its context")
	}
	if !j.Running(ctx) {
		t.Error("the job stopped with the wait")
	}
}
