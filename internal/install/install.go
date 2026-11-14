// Package install sets up tender on a machine: the restic binary, the tender user, the config,
// the credentials and the systemd units. Every path goes through a root prefix and every command
// through a runner, so tests run the whole install in a temporary directory.
package install

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/g4f4r0/tender/internal/config"
)

//go:embed units/*.service
var units embed.FS

const (
	userName      = "tender"
	binaryPath    = "/usr/local/bin/tender"
	etcDir        = "/etc/tender"
	credentialDir = "/etc/tender/credentials"
	unitDir       = "/etc/systemd/system"
	stateParent   = "/var/lib/tender"
)

// Runner runs a command and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Installer carries what an install touches. Make one with New and change the fields in tests.
type Installer struct {
	Root     string    // every path is under this directory, "/" in production
	Run      Runner    // runs useradd, systemctl, findmnt, chown and tailscale
	Out      io.Writer // what the person running the install reads
	Platform string    // GOOS/GOARCH
	Binary   string    // the tender binary to copy to /usr/local/bin/tender

	ResticURL           string
	ResticArchiveSHA256 string // of the downloaded .bz2
	ResticBinarySHA256  string // of the decompressed restic

	changed bool // some file changed during this run
}

// New returns an Installer for this machine.
func New(out io.Writer) (*Installer, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &Installer{
		Root: "/", Run: execRunner, Out: out, Platform: runtime.GOOS + "/" + runtime.GOARCH, Binary: self,
		ResticURL: resticURL, ResticArchiveSHA256: resticArchiveSHA256, ResticBinarySHA256: resticBinarySHA256,
	}, nil
}

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return out, err
}

func (in *Installer) path(p string) string { return filepath.Join(in.Root, p) }

func (in *Installer) say(format string, args ...any) {
	_, _ = fmt.Fprintf(in.Out, format+"\n", args...)
}

func (in *Installer) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := in.Run(ctx, name, args...)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// preflight stops early on a machine that cannot run the units. Being root is checked by the caller.
func (in *Installer) preflight() error {
	if in.Platform != "linux/amd64" {
		return fmt.Errorf("tender installs the pinned restic %s for linux/amd64 only; this machine is %s", resticVersion, in.Platform)
	}
	if fi, err := os.Stat(in.path("/run/systemd/system")); err != nil || !fi.IsDir() {
		return errors.New("systemd is not running on this machine; tender install needs it")
	}
	return nil
}

// write puts data at path atomically (temp file, then rename) and skips the write when the file
// already has this content and mode. A group makes it root:group.
func (in *Installer) write(ctx context.Context, path string, data []byte, mode os.FileMode, group string) (bool, error) {
	full := in.path(path)
	if old, err := os.ReadFile(full); err == nil && bytes.Equal(old, data) {
		if fi, err := os.Stat(full); err == nil && fi.Mode().Perm() == mode {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tender-")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone after a rename, cleans up after a failure
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Chmod(mode), tmp.Close()); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		return false, err
	}
	in.changed = true
	if group != "" {
		if _, err := in.run(ctx, "chown", "root:"+group, full); err != nil {
			return true, err
		}
	}
	in.say("wrote %s", full)
	return true, nil
}

// ensureDir creates path with this mode and the group, unless it is already a directory with that mode.
func (in *Installer) ensureDir(ctx context.Context, path string, mode os.FileMode, group string) error {
	full := in.path(path)
	if fi, err := os.Stat(full); err == nil && fi.IsDir() && fi.Mode().Perm() == mode {
		return nil
	}
	if err := os.MkdirAll(full, mode); err != nil {
		return err
	}
	if err := os.Chmod(full, mode); err != nil {
		return err
	}
	if group != "" {
		_, err := in.run(ctx, "chown", "root:"+group, full)
		return err
	}
	return nil
}

// prepare creates the tender user, installs this binary and makes the config and credential folders.
func (in *Installer) prepare(ctx context.Context) error {
	if _, err := in.Run(ctx, "id", "-u", userName); err != nil {
		if _, err := in.run(ctx, "useradd", "--system", "--user-group", "--no-create-home",
			"--home-dir", stateParent, "--shell", "/usr/sbin/nologin", userName); err != nil {
			return err
		}
		in.say("created user %s", userName)
	}
	self, err := os.ReadFile(in.Binary)
	if err != nil {
		return err
	}
	if _, err := in.write(ctx, binaryPath, self, 0o755, ""); err != nil {
		return err
	}
	if err := in.ensureDir(ctx, etcDir, 0o755, ""); err != nil {
		return err
	}
	return in.ensureDir(ctx, credentialDir, 0o750, userName)
}

// writeConfig writes the rendered config. No data means the file exists and stays.
func (in *Installer) writeConfig(ctx context.Context, path string, data []byte) error {
	if data == nil {
		in.say("kept %s (--force replaces it)", in.path(path))
		return nil
	}
	_, err := in.write(ctx, path, data, 0o644, "")
	return err
}

// checkConfig loads data with load, which reads a file, so a bad value stops the install before it changes anything.
func checkConfig(data []byte, load func(path string) error) error {
	f, err := os.CreateTemp("", "tender-config-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, werr := f.Write(data)
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}
	return load(f.Name())
}

func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	err := toml.NewEncoder(&b).Encode(v)
	return b.Bytes(), err
}

// enable loads the unit and starts it. A running service restarts only when something it uses changed.
func (in *Installer) enable(ctx context.Context, unit string, unitChanged, existed bool) error {
	if unitChanged {
		if _, err := in.run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if _, err := in.run(ctx, "systemctl", "enable", "--now", unit); err != nil {
		return err
	}
	if in.changed && existed {
		_, err := in.run(ctx, "systemctl", "restart", unit)
		return err
	}
	return nil
}

var credentialName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// Credential writes /etc/tender/credentials/name for the agent, which reads it as a file.
func (in *Installer) Credential(ctx context.Context, name, value string) error {
	if !credentialName.MatchString(name) {
		return fmt.Errorf("credential name %q: use lower-case letters, digits, dots and dashes, starting with a letter or digit", name)
	}
	if value == "" {
		return errors.New("the credential value is empty")
	}
	if err := in.ensureDir(ctx, etcDir, 0o755, ""); err != nil {
		return err
	}
	if err := in.ensureDir(ctx, credentialDir, 0o750, userName); err != nil {
		return err
	}
	_, err := in.write(ctx, filepath.Join(credentialDir, name), []byte(value), 0o640, userName)
	return err
}

// AgentOptions are the settings of "tender install agent".
type AgentOptions struct {
	Hub, Machine string
	Token        string // empty keeps the hub-token already installed
	Force        bool   // replace an existing agent.toml
}

// Agent installs and starts the agent. Running it again changes only what differs.
func (in *Installer) Agent(ctx context.Context, o AgentOptions) error {
	if err := in.preflight(); err != nil {
		return err
	}
	tokenPath := filepath.Join(credentialDir, "hub-token")
	if _, err := os.Stat(in.path(tokenPath)); o.Token == "" && err != nil {
		return errors.New("no hub token: pass it on stdin or with --token-file")
	}
	const configPath = etcDir + "/agent.toml"
	var cfg []byte
	if _, err := os.Stat(in.path(configPath)); err != nil || o.Force {
		paths, err := in.mountPaths(ctx)
		if err != nil {
			return err
		}
		a := config.Agent{Hub: o.Hub, Machine: o.Machine, Backups: config.Backups{Paths: paths, Exclude: config.DefaultExclude}}
		if cfg, err = encode(a); err != nil {
			return err
		}
		if err := checkConfig(cfg, func(p string) error { _, err := config.LoadAgent(p); return err }); err != nil {
			return err
		}
	}
	unit, err := units.ReadFile("units/tender-agent.service")
	if err != nil {
		return err
	}
	unitPath := filepath.Join(unitDir, "tender-agent.service")
	_, statErr := os.Stat(in.path(unitPath))
	existed := statErr == nil

	in.changed = false
	if err := in.installRestic(ctx); err != nil {
		return err
	}
	if err := in.prepare(ctx); err != nil {
		return err
	}
	if err := in.writeConfig(ctx, configPath, cfg); err != nil {
		return err
	}
	if o.Token != "" {
		if _, err := in.write(ctx, tokenPath, []byte(o.Token), 0o640, userName); err != nil {
			return err
		}
	}
	unitChanged, err := in.write(ctx, unitPath, unit, 0o644, "")
	if err != nil {
		return err
	}
	if err := in.enable(ctx, "tender-agent", unitChanged, existed); err != nil {
		return err
	}
	in.say("\nNext:\n1. Add each destination's keys: tender install credential NAME < file, for example NAME r2-apps.restic-password.\n2. Check the agent: journalctl -u tender-agent -f")
	return nil
}

// HubOptions are the settings of "tender install hub".
type HubOptions struct {
	Email *config.Email // nil writes no [email] table
	Force bool          // replace an existing hub.toml
}

// Hub installs and starts the hub, then publishes it with Tailscale Serve.
func (in *Installer) Hub(ctx context.Context, o HubOptions) error {
	if err := in.preflight(); err != nil {
		return err
	}
	const configPath = etcDir + "/hub.toml"
	var cfg []byte
	if _, err := os.Stat(in.path(configPath)); err != nil || o.Force {
		var err error
		h := config.Hub{Listen: config.DefaultListen, Database: config.DefaultDatabase, Email: o.Email}
		if cfg, err = encode(h); err != nil {
			return err
		}
		if err := checkConfig(cfg, func(p string) error { _, err := config.LoadHub(p); return err }); err != nil {
			return err
		}
	}
	unit, err := units.ReadFile("units/tender-hub.service")
	if err != nil {
		return err
	}
	// systemd fails the unit when a LoadCredential path is missing, and the hub reads the file
	// directly without the line, so the line goes in only when the file exists.
	if _, err := os.Stat(in.path(filepath.Join(credentialDir, "smtp-password"))); err != nil {
		unit = dropLine(unit, "LoadCredential=smtp-password:")
	}
	unitPath := filepath.Join(unitDir, "tender-hub.service")
	_, statErr := os.Stat(in.path(unitPath))
	existed := statErr == nil

	in.changed = false
	if err := in.prepare(ctx); err != nil {
		return err
	}
	if err := in.writeConfig(ctx, configPath, cfg); err != nil {
		return err
	}
	unitChanged, err := in.write(ctx, unitPath, unit, 0o644, "")
	if err != nil {
		return err
	}
	if err := in.enable(ctx, "tender-hub", unitChanged, existed); err != nil {
		return err
	}
	listen := config.DefaultListen
	if h, err := config.LoadHub(in.path(configPath)); err == nil {
		listen = h.Listen
	}
	in.serve(ctx, "http://"+listen)
	return nil
}

// dropLine removes the lines that start with prefix.
func dropLine(text []byte, prefix string) []byte {
	var out [][]byte
	for _, l := range bytes.Split(text, []byte("\n")) {
		if !bytes.HasPrefix(l, []byte(prefix)) {
			out = append(out, l)
		}
	}
	return bytes.Join(out, []byte("\n"))
}
