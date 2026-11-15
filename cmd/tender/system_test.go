package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/security"
)

// SSH is weak only when someone can log in with a password: root may log in with a key.
func TestSecurityReportSSH(t *testing.T) {
	for _, c := range []struct {
		passwords bool
		root      string
		weak      bool
		text      string
	}{
		{false, "prohibit-password", false, ""},
		{false, "yes", false, ""},
		{true, "prohibit-password", true, "allows password login (PasswordAuthentication yes)"},
		{true, "yes", true, "allows password login (PasswordAuthentication yes) and lets root log in with a password (PermitRootLogin yes)"},
	} {
		r := security.Report{SSH: security.SSH{Check: security.CheckOK, Installed: true, PasswordAuthentication: &c.passwords, PermitRootLogin: &c.root}}
		got := securityReport(r)
		if got.SSHPasswords != c.weak || got.SSH != c.text {
			t.Errorf("passwords %v, root %s: weak %v %q, want %v %q", c.passwords, c.root, got.SSHPasswords, got.SSH, c.weak, c.text)
		}
	}
}

// tender daemon --check reads what the daemon needs and exits, for the update's health check.
func TestDaemonCheck(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(cfg, []byte("hub = \"http://127.0.0.1:1\"\nmachine = \"apps\"\n\n[backups]\npaths = [\"/srv\"]\nexclude = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(dir, "credentials")
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	args := []string{"--check", "--config", cfg, "--state", dir}
	if err := runDaemon(args, log); err == nil || !strings.Contains(err.Error(), "cannot read the hub token") {
		t.Errorf("no token: %v", err)
	}
	if err := os.MkdirAll(creds, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(creds, "hub-token"), []byte("token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDaemon(args, log); err != nil {
		t.Errorf("check: %v", err)
	}
	if err := runDaemon([]string{"--check", "--config", cfg, "--state", filepath.Join(dir, "missing")}, log); err == nil || !strings.Contains(err.Error(), "state folder") {
		t.Errorf("no state folder: %v", err)
	}
}
