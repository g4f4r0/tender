package updates

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/config"
)

// The job runs tender update --apply in the transient unit tenderupdate, and its result is read back by version.
func TestJob(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	stand := filepath.Join(dir, "systemd-run")
	script := "#!/bin/sh\necho \"$@\" > " + args + "\ncase \"$*\" in *0.3.9*) echo 'Tender 0.3.9 failed its health check' >&2; exit 1;; esac\n"
	if err := os.WriteFile(stand, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvTestSystemdRun, stand)
	j := Job{Binary: "/usr/local/bin/tender", StateDir: filepath.Join(dir, "state")}
	if err := config.WriteUpdateResult(j.StateDir, config.UpdateResult{Version: "0.3.0", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Update(context.Background(), "0.3.1"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(args)
	if want := "--wait --collect --quiet --unit tenderupdate /usr/local/bin/tender update --apply 0.3.1\n"; string(got) != want {
		t.Errorf("systemd-run %s, want %s", got, want)
	}
	if _, _, ok := j.UpdateResult("0.3.0"); ok {
		t.Error("a stale result stayed")
	}
	if err := j.Update(context.Background(), "0.3.9"); err == nil || !strings.Contains(err.Error(), "failed its health check") {
		t.Errorf("failed job: %v", err)
	}

	if err := config.WriteUpdateResult(j.StateDir, config.UpdateResult{Version: "v0.3.1", Status: "failed", Output: "rolled back"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := j.UpdateResult("0.3.2"); ok {
		t.Error("the result of another version counted")
	}
	if status, output, ok := j.UpdateResult("0.3.1"); !ok || status != "failed" || output != "rolled back" {
		t.Errorf("result = %s %s %v", status, output, ok)
	}
	j.ClearUpdateResult()
	if _, _, ok := j.UpdateResult("0.3.1"); ok {
		t.Error("the result stayed after ClearUpdateResult")
	}
}
