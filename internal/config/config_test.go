package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAgent(t *testing.T) {
	p := write(t, "agent.toml", `
hub = "https://hub.example.net"
machine = "g4f4r0-code"
[backups]
paths = ["/", "/boot"]
exclude = ["/tmp"]
`)
	a, err := LoadAgent(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hub != "https://hub.example.net" || a.Machine != "g4f4r0-code" {
		t.Errorf("got %+v", a)
	}
	if !slices.Equal(a.Backups.Paths, []string{"/", "/boot"}) || !slices.Equal(a.Backups.Exclude, []string{"/tmp"}) {
		t.Errorf("backups = %+v", a.Backups)
	}
}

func TestLoadAgentDefaults(t *testing.T) {
	p := write(t, "agent.toml", "hub = \"http://127.0.0.1:8080\"\nmachine = \"m\"\n")
	a, err := LoadAgent(p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.Backups.Paths, []string{"/"}) {
		t.Errorf("paths = %v, want [/]", a.Backups.Paths)
	}
	if !slices.Equal(a.Backups.Exclude, DefaultExclude) {
		t.Errorf("exclude = %v, want the default list", a.Backups.Exclude)
	}
}

func TestLoadAgentEmptyExcludeStaysEmpty(t *testing.T) {
	p := write(t, "agent.toml", "hub = \"http://h\"\nmachine = \"m\"\n[backups]\nexclude = []\n")
	a, err := LoadAgent(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Backups.Exclude) != 0 {
		t.Errorf("exclude = %v, want empty", a.Backups.Exclude)
	}
}

func TestLoadAgentRejects(t *testing.T) {
	cases := map[string]struct{ content, want string }{
		"unknown top key":    {"hub = \"http://h\"\nmachine = \"m\"\nhubb = 1\n", "unknown keys: hubb"},
		"unknown backup key": {"hub = \"http://h\"\nmachine = \"m\"\n[backups]\npath = [\"/\"]\n", "backups.path"},
		"no machine":         {"hub = \"http://h\"\n", "machine is required"},
		"bad hub":            {"hub = \"hub.example.net\"\nmachine = \"m\"\n", "hub must be"},
		"bad toml":           {"hub = \n", "read config"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadAgent(write(t, "agent.toml", c.content))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
	if _, err := LoadAgent(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Error("missing file: want an error")
	}
}

func TestReadCredential(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hub-token"), []byte("s3cret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCredential(dir, "hub-token")
	if err != nil || got != "s3cret-value" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestReadCredentialErrorsNameTheFileNotTheContent(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadCredential(dir, "r2.restic-password")
	if err == nil || !strings.Contains(err.Error(), "r2.restic-password") {
		t.Errorf("missing file error = %v, want it to name the file", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty"), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCredential(dir, "empty"); err == nil {
		t.Error("empty credential: want an error")
	}
	if _, err := ReadCredential(dir, "../etc/passwd"); err == nil {
		t.Error("path in name: want an error")
	}
}

func TestCredentialsDir(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "/run/credentials/tender.service")
	if got := CredentialsDir(); got != "/run/credentials/tender.service" {
		t.Errorf("got %q", got)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if got := CredentialsDir(); got != "/etc/tender/credentials" {
		t.Errorf("got %q", got)
	}
}
