package backups

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRestic writes a script that stands in for restic. It exits with FAKE_CODE after printing
// FAKE_STDOUT and FAKE_STDERR; with FAKE_NEEDS_INIT=1 it answers 10 until "init" has run.
func fakeRestic(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = init ]; then touch "$TMPDIR/inited"; exit 0; fi
if [ "$FAKE_NEEDS_INIT" = 1 ] && [ ! -e "$TMPDIR/inited" ]; then
  printf '%s\n' '{"message_type":"exit_error","code":10,"message":"Fatal: repository does not exist"}' >&2
  exit 10
fi
printf '%s\n' "$FAKE_STDERR" >&2
printf '%s\n' "$FAKE_STDOUT"
exit "$FAKE_CODE"
`
	p := filepath.Join(t.TempDir(), "restic")
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func fakeRunner(t *testing.T, env ...string) *Restic {
	state := t.TempDir()
	return &Restic{
		Bin: fakeRestic(t), Repository: "/nowhere", PasswordFile: "/nowhere/pw", Host: "m1",
		Env: env, CacheDir: filepath.Join(state, "cache"), StateDir: state,
	}
}

func TestExitCodeMapping(t *testing.T) {
	const summary = `{"message_type":"summary","snapshot_id":"abc123","total_bytes_processed":5}`
	exitErr := func(code, msg string) string {
		return `{"message_type":"exit_error","code":` + code + `,"message":"` + msg + `"}`
	}
	cases := []struct {
		name       string
		code       string
		stderr     string
		status     string
		errContain string // "" means Error must be empty
		wantID     string
	}{
		{"ok", "0", "", StatusCompleted, "", "abc123"},
		{"partial", "3",
			`{"message_type":"error","error":{"message":"x"},"during":"scan","item":"/etc/a"}` + "\n" +
				`{"message_type":"error","error":{"message":"x"},"during":"archival","item":"/etc/a"}` + "\n" +
				`{"message_type":"error","error":{"message":"x"},"during":"archival","item":"/etc/b"}` + "\n" +
				exitErr("3", "Warning: at least one source file could not be read"),
			StatusCompleted, "2 files could not be read: /etc/a, /etc/b", "abc123"},
		{"partial without items", "3", exitErr("3", "Warning: some"), StatusCompleted, "Warning: some", "abc123"},
		{"locked", "11", exitErr("11", "Fatal: unable to create lock: repository is already locked by PID 7"), StatusFailed, "already locked by PID 7", ""},
		{"wrong password", "12", exitErr("12", "Fatal: wrong password or no key found"), StatusFailed, "wrong password", ""},
		{"interrupted", "130", exitErr("130", "Fatal: unable to save snapshot: context canceled"), StatusFailed, "context canceled", ""},
		{"generic", "1", exitErr("1", "Fatal: all source directories/files do not exist"), StatusFailed, "do not exist", ""},
		{"panic without exit_error", "2", "panic: runtime error\ngoroutine 1 [running]:", StatusFailed, "panic: runtime error", ""},
		{"unknown code", "77", "", StatusFailed, "exit", ""},
		{"stack trace is cut", "1", exitErr("1", `unable to open cache: mkdir /x: permission denied\ngithub.com/restic/restic/internal/backend/cache.New`), StatusFailed, "permission denied", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := fakeRunner(t, "FAKE_CODE="+c.code, "FAKE_STDOUT="+summary, "FAKE_STDERR="+c.stderr)
			o := r.Backup(context.Background(), []string{"/src"}, nil)
			if o.Status != c.status {
				t.Errorf("status = %q, want %q (error %q)", o.Status, c.status, o.Error)
			}
			if c.errContain == "" && o.Error != "" || !strings.Contains(o.Error, c.errContain) {
				t.Errorf("error = %q, want it to contain %q", o.Error, c.errContain)
			}
			if strings.Contains(o.Error, "github.com/") {
				t.Errorf("error keeps a stack trace: %q", o.Error)
			}
			if o.ResticID != c.wantID && c.status == StatusCompleted || o.ResticID != "" && c.status == StatusFailed {
				t.Errorf("restic id = %q, want %q", o.ResticID, c.wantID)
			}
		})
	}
}

func TestMissingRepositoryIsInitializedOnce(t *testing.T) {
	r := fakeRunner(t, "FAKE_NEEDS_INIT=1", "FAKE_CODE=0", `FAKE_STDOUT={"message_type":"summary","snapshot_id":"s1"}`)
	o := r.Backup(context.Background(), []string{"/src"}, nil)
	if o.Status != StatusCompleted || o.ResticID != "s1" {
		t.Errorf("outcome = %+v, want completed with s1 after init", o)
	}
	// If init doesn't help, the run fails instead of looping.
	r = fakeRunner(t, "FAKE_NEEDS_INIT=1", "FAKE_CODE=0")
	r.Bin = fakeNeverInits(t)
	if o := r.Backup(context.Background(), []string{"/src"}, nil); o.Status != StatusFailed {
		t.Errorf("outcome = %+v, want failed", o)
	}
}

func fakeNeverInits(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "restic")
	script := "#!/bin/sh\nif [ \"$1\" = init ]; then exit 0; fi\nexit 10\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestErrorTextHidesSecretsAndIsCapped(t *testing.T) {
	secret := "AKIAsupersecret1234"
	msg := "Fatal: bad key " + secret + " " + strings.Repeat("x", 3000)
	r := fakeRunner(t, "AWS_ACCESS_KEY_ID="+secret, "FAKE_CODE=1",
		`FAKE_STDERR={"message_type":"exit_error","code":1,"message":"`+msg+`"}`)
	o := r.Backup(context.Background(), []string{"/src"}, nil)
	if strings.Contains(o.Error, secret) {
		t.Errorf("error leaks the secret: %q", o.Error[:60])
	}
	if len(o.Error) > maxErrorBytes+3 {
		t.Errorf("error is %d bytes, want at most %d", len(o.Error), maxErrorBytes+3)
	}

	// A secret that the size limit cuts in half is still hidden.
	msg = strings.Repeat("x", maxErrorBytes-10) + secret + " tail"
	r = fakeRunner(t, "AWS_ACCESS_KEY_ID="+secret, "FAKE_CODE=1",
		`FAKE_STDERR={"message_type":"exit_error","code":1,"message":"`+msg+`"}`)
	if o := r.Backup(context.Background(), []string{"/src"}, nil); strings.Contains(o.Error, secret[:10]) {
		t.Errorf("error keeps the start of a secret cut by the limit: %q", o.Error[len(o.Error)-20:])
	}
}

func TestCleanupRejectsBadRetention(t *testing.T) {
	r := fakeRunner(t, "FAKE_CODE=0")
	if o := r.Cleanup(context.Background(), 0); o.Status != StatusFailed {
		t.Errorf("outcome = %+v, want failed", o)
	}
}

func TestMissingBinaryFails(t *testing.T) {
	r := fakeRunner(t)
	r.Bin = filepath.Join(t.TempDir(), "no-such-restic")
	if o := r.Check(context.Background()); o.Status != StatusFailed || o.Error == "" {
		t.Errorf("outcome = %+v, want failed with an error", o)
	}
}

// TestResticIntegration runs the real restic against a local repository.
func TestResticIntegration(t *testing.T) {
	bin := lookRestic(t)
	ctx := context.Background()

	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "a.txt"), strings.Repeat("alpha\n", 1000))
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "bravo\n")
	writeFile(t, filepath.Join(src, "skip.log"), "excluded\n")
	state := t.TempDir()
	password := filepath.Join(state, "pw")
	writeFile(t, password, "test-only-password\n")

	r := &Restic{
		Bin: bin, Repository: filepath.Join(t.TempDir(), "repo"), PasswordFile: password, Host: "tender-test",
		CacheDir: filepath.Join(state, "cache"), StateDir: state,
	}

	o := r.Backup(ctx, []string{src}, []string{"*.log"}) // the repository doesn't exist yet: init runs
	if o.Status != StatusCompleted || o.Error != "" || len(o.ResticID) != 64 {
		t.Fatalf("backup = %+v", o)
	}
	writeFile(t, filepath.Join(src, "sub", "c.txt"), "charlie\n")
	o2 := r.Backup(ctx, []string{src}, []string{"*.log"})
	if o2.Status != StatusCompleted || o2.ResticID == o.ResticID {
		t.Fatalf("second backup = %+v", o2)
	}

	snaps, err := r.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snaps))
	}
	s := snaps[0]
	if s.ResticID != o.ResticID {
		t.Errorf("restic_id = %s, want %s", s.ResticID, o.ResticID)
	}
	const wantBytes = 6000 + 6 // a.txt and b.txt; the .log is excluded
	if s.SizeBytes == nil || *s.SizeBytes != wantBytes {
		t.Errorf("size_bytes = %v, want %d", s.SizeBytes, wantBytes)
	}
	if len(s.Paths) != 1 || s.Paths[0] != src {
		t.Errorf("paths = %v, want [%s]", s.Paths, src)
	}
	if s.CreatedAt.IsZero() || s.CreatedAt.Location().String() != "UTC" {
		t.Errorf("created_at = %v", s.CreatedAt)
	}
	for _, s := range snaps {
		t.Logf("snapshot restic_id=%s size_bytes=%d paths=%v created_at=%s", s.ResticID, *s.SizeBytes, s.Paths, s.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	}

	if c := r.Cleanup(ctx, 30); c.Status != StatusCompleted {
		t.Errorf("cleanup = %+v", c)
	}
	if c := r.Check(ctx); c.Status != StatusCompleted {
		t.Errorf("check = %+v", c)
	}
	if after, _ := r.Snapshots(ctx); len(after) != 2 {
		t.Errorf("%d snapshots after cleanup, want 2 (keep-within counts from the newest)", len(after))
	}

	// A wrong password is exit 12: failed, with restic's message.
	writeFile(t, password, "another-password\n")
	if w := r.Check(ctx); w.Status != StatusFailed || !strings.Contains(w.Error, "wrong password") {
		t.Errorf("wrong password check = %+v", w)
	}
	writeFile(t, password, "test-only-password\n")

	// An unreadable file is exit 3: completed, with the file named in the error.
	if os.Geteuid() != 0 {
		writeFile(t, filepath.Join(src, "noread.txt"), "secret\n")
		if err := os.Chmod(filepath.Join(src, "noread.txt"), 0); err != nil {
			t.Fatal(err)
		}
		p := r.Backup(ctx, []string{src}, nil)
		if p.Status != StatusCompleted || !strings.Contains(p.Error, "noread.txt") || len(p.ResticID) != 64 {
			t.Errorf("partial backup = %+v", p)
		}
		t.Logf("partial backup: status=%s error=%q", p.Status, p.Error)
	}

	// No pending lock is left behind.
	if _, err := os.Stat(filepath.Join(r.Repository, "locks")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(r.Repository, "locks"))
		if len(entries) != 0 {
			t.Errorf("%d locks left in the repository", len(entries))
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// lookRestic finds the real restic, on PATH or in ~/.local/bin, or skips the test.
func lookRestic(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("restic")
	if err != nil {
		if home, herr := os.UserHomeDir(); herr == nil {
			bin = filepath.Join(home, ".local", "bin", "restic")
			_, err = os.Stat(bin)
		}
	}
	if err != nil {
		t.Skip("restic is not installed")
	}
	return bin
}
