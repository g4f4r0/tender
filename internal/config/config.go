// Package config loads the daemon's TOML file and reads its credential files.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultDaemonPath is where the daemon reads its config when --config isn't given.
const DefaultDaemonPath = "/etc/tender/daemon.toml"

// Daemon is /etc/tender/daemon.toml. The destination, schedule and retention come from the hub.
type Daemon struct {
	Hub     string  `toml:"hub"`
	Machine string  `toml:"machine"`
	Backups Backups `toml:"backups"`
}

// Backups says what to back up.
type Backups struct {
	Paths   []string `toml:"paths"`
	Exclude []string `toml:"exclude"`
}

// DefaultExclude is used when the config has no exclude key.
var DefaultExclude = []string{
	"/var/lib/docker", "/var/lib/containerd", "/var/cache", "/tmp", "/var/tmp",
	"/home/*/.cache", "/home/*/.local/share/docker", "**/node_modules",
}

// LoadDaemon reads and validates the daemon config. Unknown keys are errors, so a typo
// can't silently turn a setting off.
func LoadDaemon(path string) (Daemon, error) {
	var a Daemon
	md, err := toml.DecodeFile(path, &a)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Daemon{}, fmt.Errorf("there is no daemon config at %s. Run \"sudo tender install daemon --hub URL --machine NAME\" to create it, or pass --config PATH: %w", path, err)
		}
		return Daemon{}, fmt.Errorf("cannot read config %s. Fix its TOML syntax and check that you can read it: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}
		return Daemon{}, fmt.Errorf("config %s: unknown keys: %s. Fix the spelling or remove them", path, strings.Join(names, ", "))
	}
	if a.Machine == "" {
		return Daemon{}, fmt.Errorf("config %s: machine is required. Set machine = \"NAME\", the name the hub knows", path)
	}
	u, err := url.Parse(a.Hub)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Daemon{}, fmt.Errorf("config %s: hub must be an http or https URL. Set hub = \"https://apps.example.ts.net\", for example", path)
	}
	if len(a.Backups.Paths) == 0 {
		a.Backups.Paths = []string{"/"}
	}
	if !md.IsDefined("backups", "exclude") {
		a.Backups.Exclude = DefaultExclude
	}
	return a, nil
}

// CredentialsDir is $CREDENTIALS_DIRECTORY under systemd, else /etc/tender/credentials.
func CredentialsDir() string {
	if d := os.Getenv("CREDENTIALS_DIRECTORY"); d != "" {
		return d
	}
	return filepath.Join(Root(), "/etc/tender/credentials")
}

// ReadCredential returns the content of dir/name without its trailing newline.
// Errors name the file and say how to save it again, never show its content.
func ReadCredential(dir, name string) (string, error) {
	if name == "" || name != filepath.Base(name) {
		return "", fmt.Errorf("credential name %q must be a plain file name. Use letters, digits, dots and dashes", name)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("credential %s is not saved on this machine. Install the daemon again with \"sudo tender install daemon\" to save it: %w", name, err)
	}
	if err != nil {
		return "", fmt.Errorf("cannot read credential %s. Check that the tender user can read it: %w", name, err)
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("credential %s is empty. Install the daemon again with \"sudo tender install daemon\" to save it", name)
	}
	return v, nil
}
