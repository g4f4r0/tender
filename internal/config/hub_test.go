package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadHub(t *testing.T) {
	p := write(t, "hub.toml", `
listen = "127.0.0.1:9000"
database = "/tmp/hub.db"
[email]
host = "smtp.example.net"
port = 2465
username = "api_token"
from = "tender <noreply@example.net>"
to = ["a@example.com", "b@example.com"]
`)
	h, err := LoadHub(p)
	if err != nil {
		t.Fatal(err)
	}
	if h.Listen != "127.0.0.1:9000" || h.Database != "/tmp/hub.db" {
		t.Errorf("got %+v", h)
	}
	e := h.Email
	if e == nil || e.Host != "smtp.example.net" || e.Port != 2465 || e.Username != "api_token" ||
		e.From != "tender <noreply@example.net>" || !slices.Equal(e.To, []string{"a@example.com", "b@example.com"}) {
		t.Errorf("email = %+v", e)
	}
}

func TestLoadHubDefaults(t *testing.T) {
	// No [email] table: email is off. A missing port means 465.
	h, err := LoadHub(write(t, "hub.toml", "listen = \"0.0.0.0:1\"\n"))
	if err != nil || h.Listen != "0.0.0.0:1" || h.Database != DefaultDatabase || h.Email != nil {
		t.Errorf("got %+v, %v", h, err)
	}
	h, err = LoadHub(write(t, "hub.toml", "[email]\nhost = \"h\"\nusername = \"u\"\nfrom = \"a@b.co\"\nto = [\"c@d.co\"]\n"))
	if err != nil || h.Email == nil || h.Email.Port != 465 || h.Listen != DefaultListen {
		t.Errorf("got %+v, %v", h, err)
	}
}

func TestLoadHubMissingFile(t *testing.T) {
	// The default path may be missing (the tests run where /etc/tender/hub.toml doesn't exist);
	// a path the user gives may not.
	if _, err := LoadHub(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Error("missing explicit file: want an error")
	}
	if _, err := LoadHub(""); err != nil && !strings.Contains(err.Error(), DefaultHubPath) {
		t.Errorf("default path: %v", err)
	}
}

func TestLoadHubRejects(t *testing.T) {
	const email = "[email]\nhost = \"h\"\nusername = \"u\"\nfrom = \"a@b.co\"\nto = [\"c@d.co\"]\n"
	cases := map[string]struct{ content, want string }{
		"unknown top key":   {"listn = \"x\"\n", "unknown keys: listn"},
		"unknown email key": {email + "password = \"x\"\n", "email.password"},
		"no host":           {strings.Replace(email, "host = \"h\"\n", "", 1), "host is required"},
		"no username":       {strings.Replace(email, "username = \"u\"\n", "", 1), "username is required"},
		"no recipients":     {strings.Replace(email, "to = [\"c@d.co\"]\n", "to = []\n", 1), "to needs"},
		"bad from":          {strings.Replace(email, "a@b.co", "nobody", 1), "from:"},
		"bad to":            {strings.Replace(email, "c@d.co", "nobody", 1), "to:"},
		"bad port":          {email + "port = 70000\n", "port must be"},
		"bad toml":          {"listen = \n", "read config"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadHub(write(t, "hub.toml", c.content))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}
