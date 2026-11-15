package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/config"
)

var testKeyBase64 = base64.StdEncoding.EncodeToString(testKey)

func TestHubSettingsFlagsOverrideEnvironment(t *testing.T) {
	env := func(name string) string {
		return map[string]string{
			config.EnvEncryptionKey: testKeyBase64, config.EnvListen: "127.0.0.1:9", config.EnvDatabase: "/tmp/from-env.db",
		}[name]
	}
	for _, c := range []struct{ database, listen, wantDatabase, wantListen string }{
		{"", "", "/tmp/from-env.db", "127.0.0.1:9"},
		{"/tmp/flag.db", "", "/tmp/flag.db", "127.0.0.1:9"},
		{"", "0.0.0.0:1", "/tmp/from-env.db", "0.0.0.0:1"},
	} {
		cfg, err := hubSettings(env, c.database, c.listen)
		if err != nil || cfg.Database != c.wantDatabase || cfg.Listen != c.wantListen {
			t.Errorf("flags %q %q: %+v, %v", c.database, c.listen, cfg, err)
		}
	}
}

func TestHubHelpShowsOnlyHowToRunIt(t *testing.T) {
	for _, want := range []string{"tender hub [--database PATH] [--listen ADDR]", "openssl rand -base64 32",
		"TENDER_ENCRYPTION_KEY", "TENDER_LISTEN", "TENDER_DATABASE", "TENDER_SMTP_HOST", "TENDER_SMTP_PORT",
		"TENDER_SMTP_USERNAME", "TENDER_SMTP_PASSWORD", "TENDER_SMTP_FROM", "/etc/tender/hub.env"} {
		if !strings.Contains(hubUsage, want) {
			t.Errorf("tender hub help lacks %q", want)
		}
	}
	for _, gone := range []string{"add-machine", "rotate-token", "add-destination", "set-backup", "test-email", "--config", "hub.toml"} {
		if strings.Contains(hubUsage, gone) {
			t.Errorf("tender hub help still mentions %q", gone)
		}
	}
	// The usage fits the width every other usage text keeps.
	for _, line := range strings.Split(hubUsage, "\n") {
		if len(line) > lineWidth+20 {
			t.Errorf("usage line is %d characters: %q", len(line), line)
		}
	}
}

// An old admin subcommand is no longer one: it is an argument the hub doesn't take.
func TestHubHasNoAdminSubcommands(t *testing.T) {
	for _, name := range []string{"add-machine", "rotate-token", "add-destination", "set-backup", "test-email"} {
		err := serveHub([]string{name}, quiet)
		if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
			t.Errorf("tender hub %s: %v, want it refused as an argument", name, err)
		}
	}
}

func TestHubNeedsAKey(t *testing.T) {
	t.Setenv(config.EnvEncryptionKey, "")
	err := serveHub([]string{"--database", filepath.Join(t.TempDir(), "hub.db")}, quiet)
	if err == nil || !strings.Contains(err.Error(), "openssl rand -base64 32") || !strings.Contains(err.Error(), config.EnvEncryptionKey) {
		t.Errorf("no key: %v, want an error that names the variable and how to make a key", err)
	}
	t.Setenv(config.EnvEncryptionKey, "short")
	if err := serveHub(nil, quiet); err == nil || !strings.Contains(err.Error(), "openssl rand -base64 32") {
		t.Errorf("bad key: %v", err)
	}
}

// A hub started with another key than the one that sealed its secrets stops at once, with a
// message that says why, and shows none of them.
func TestHubRefusesTheWrongKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	ctx := context.Background()
	d, err := openHub(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = backups.NewStore(d, testKey).AddDestination(ctx, backups.DestinationInput{Name: "r2", Config: map[string]any{"endpoint": "https://x.example.com", "bucket": "b", "access_key_id": "marker-key-id", "secret_access_key": "marker-secret"}, Password: "marker-password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	other := bytes.Clone(testKey)
	other[0] ^= 0xff
	t.Setenv(config.EnvEncryptionKey, base64.StdEncoding.EncodeToString(other))
	t.Setenv(config.EnvListen, "127.0.0.1:0")
	err = serveHub([]string{"--database", path}, quiet)
	if err == nil || !strings.Contains(err.Error(), config.EnvEncryptionKey) || !strings.Contains(err.Error(), `"r2"`) || strings.Contains(err.Error(), "marker") {
		t.Errorf("wrong key: %v, want a message about the key and destination r2, with no secret", err)
	}
}

// A database the hub can't write, such as one outside its state folder under systemd's
// ProtectSystem, stops the hub at once with a message that says how to fix it.
func TestHubRefusesADatabaseItCantWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.db")
	d, err := openHub(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err = openHub(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "can't write its database "+path) || !strings.Contains(err.Error(), "ReadWritePaths=") {
		t.Errorf("read-only folder: %v", err)
	}
}
