// Package install sets up Tender on a machine: the restic binary, the tender user, the config,
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

	Version     string // the running tender version, "dev" for a local build
	LatestURL   string // answers with the newest release as JSON, including its tag_name
	DownloadURL string // holds the releases: <DownloadURL>/<tag>/<file>

	Refreshing bool // refresh the installed services: restart them whatever changed, skip the next steps

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
		Version: "dev", LatestURL: latestURL, DownloadURL: downloadURL,
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
		return fmt.Errorf("tender installs on linux/amd64 only, and this machine is %s. Run the install on a 64-bit x86 Linux server", in.Platform)
	}
	if fi, err := os.Stat(in.path("/run/systemd/system")); err != nil || !fi.IsDir() {
		return errors.New("systemd is not running on this machine, and Tender runs its services with it. Use a Linux server that boots with systemd")
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
	return true, nil
}

// writeSaying is write, and says what happened when it changed the file.
func (in *Installer) writeSaying(ctx context.Context, path string, data []byte, mode os.FileMode, group, what string) (bool, error) {
	changed, err := in.write(ctx, path, data, mode, group)
	if err == nil && changed {
		in.say("%s", what)
	}
	return changed, err
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
		in.say("Created the %s user", userName)
	}
	self, err := os.ReadFile(in.Binary)
	if err != nil {
		return err
	}
	if _, err := in.writeSaying(ctx, binaryPath, self, 0o755, "", "Installed Tender at "+in.path(binaryPath)); err != nil {
		return err
	}
	if err := in.ensureDir(ctx, etcDir, 0o755, ""); err != nil {
		return err
	}
	return in.ensureDir(ctx, credentialDir, 0o750, userName)
}

// writeConfig writes the rendered config. No data means the file exists and stays; that is worth
// a line only when the person passed settings that the kept file now ignores.
func (in *Installer) writeConfig(ctx context.Context, path string, data []byte, passedSettings bool) error {
	if data == nil {
		if passedSettings {
			in.say("Kept the existing %s. Add --force to replace it.", in.path(path))
		}
		return nil
	}
	_, err := in.writeSaying(ctx, path, data, 0o644, "", "Wrote "+in.path(path))
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
	if !existed {
		in.say("Started %s", unit)
	}
	if (in.changed || in.Refreshing) && existed {
		if _, err := in.run(ctx, "systemctl", "restart", unit); err != nil {
			return err
		}
		in.say("Restarted %s", unit)
	}
	return nil
}

var credentialName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// Credential writes /etc/tender/credentials/name. The agent reads these files directly. The hub
// can't (its unit hides the folder) and gets only smtp-password through LoadCredential, so adding
// smtp-password to an installed hub rewrites its unit and restarts it.
func (in *Installer) Credential(ctx context.Context, name, value string) error {
	if !credentialName.MatchString(name) {
		return fmt.Errorf("credential name %q is not valid. Use lower-case letters, digits, dots and dashes, starting with a letter or digit", name)
	}
	if value == "" {
		return errors.New("the credential value is empty. Pipe it in, for example: sudo tender install credential NAME < FILE")
	}
	if err := in.ensureDir(ctx, etcDir, 0o755, ""); err != nil {
		return err
	}
	if err := in.ensureDir(ctx, credentialDir, 0o750, userName); err != nil {
		return err
	}
	in.changed = false
	if changed, err := in.write(ctx, filepath.Join(credentialDir, name), []byte(value), 0o640, userName); err != nil {
		return err
	} else if changed {
		in.say("Saved credential %s", name)
	} else {
		in.say("Credential %s already has this value. Nothing to change.", name)
	}
	if name != "smtp-password" {
		return nil
	}
	if _, err := os.Stat(in.path(hubUnitPath)); err != nil {
		return nil // no hub here; "tender install hub" adds the line when it runs
	}
	unit, err := in.hubUnit()
	if err != nil {
		return err
	}
	unitChanged, err := in.write(ctx, hubUnitPath, unit, 0o644, "")
	if err != nil {
		return err
	}
	return in.enable(ctx, "tender-hub", unitChanged, true)
}

const (
	agentUnitPath = unitDir + "/tender-agent.service"
	hubUnitPath   = unitDir + "/tender-hub.service"
)

// hubUnit is the hub's unit file. systemd fails a unit whose LoadCredential path is missing, so
// the smtp-password line goes in only when the file exists.
func (in *Installer) hubUnit() ([]byte, error) {
	unit, err := units.ReadFile("units/tender-hub.service")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(in.path(filepath.Join(credentialDir, "smtp-password"))); err != nil {
		unit = dropLine(unit, "LoadCredential=smtp-password:")
	}
	return unit, nil
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
		return errors.New("this machine has no hub token yet. Create one with \"tender hub add-machine NAME\" on the hub, then pass it with --token-file PATH or pipe it in on stdin")
	}
	const configPath = etcDir + "/agent.toml"
	var cfg []byte
	if _, err := os.Stat(in.path(configPath)); err != nil || o.Force {
		if o.Hub == "" || o.Machine == "" {
			return errors.New("this machine has no agent config yet. Pass --hub URL and --machine NAME")
		}
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
	unitPath := agentUnitPath
	_, statErr := os.Stat(in.path(unitPath))
	existed := statErr == nil

	in.changed = false
	if err := in.installRestic(ctx); err != nil {
		return err
	}
	if err := in.prepare(ctx); err != nil {
		return err
	}
	if err := in.writeConfig(ctx, configPath, cfg, o.Hub != ""); err != nil {
		return err
	}
	if o.Token != "" {
		if _, err := in.writeSaying(ctx, tokenPath, []byte(o.Token), 0o640, userName, "Saved the hub token"); err != nil {
			return err
		}
	}
	unitChanged, err := in.writeSaying(ctx, unitPath, unit, 0o644, "", "Wrote "+in.path(unitPath))
	if err != nil {
		return err
	}
	if err := in.enable(ctx, "tender-agent", unitChanged, existed); err != nil {
		return err
	}
	switch {
	case !existed:
		in.say(agentNext)
	case !in.changed && !in.Refreshing:
		in.say("tender-agent is already installed and up to date. Nothing to change.")
	}
	return nil
}

const agentNext = `
The agent is running. Next:
  1. Save the repository password and the S3 keys of the destination this machine backs up to,
     named after it (here r2):
       sudo tender install credential r2.password < password.txt
       sudo tender install credential r2.access-key-id < access-key-id.txt
       sudo tender install credential r2.secret-access-key < secret-access-key.txt
  2. Watch the agent: journalctl -u tender-agent -f`

// hubNext says what to do after a first hub install. url is empty when the hub is not published.
func hubNext(url string) string {
	if url == "" {
		url = "HUB_URL"
	}
	return `
The hub is running. Next:
  1. Add a machine and keep the token it prints once:
       sudo tender hub add-machine NAME
  2. Add a destination and set when that machine backs up:
       sudo tender hub add-destination r2 --type s3 --endpoint URL --bucket BUCKET
       sudo tender hub set-backup NAME --destination r2 --schedule "30 3 * * *" --retention-days 30
  3. Install the agent on that machine, with the token when it asks:
       curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh \
         | sudo sh -s -- agent --hub ` + url + ` --machine NAME`
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
	unit, err := in.hubUnit()
	if err != nil {
		return err
	}
	unitPath := hubUnitPath
	_, statErr := os.Stat(in.path(unitPath))
	existed := statErr == nil

	in.changed = false
	if err := in.prepare(ctx); err != nil {
		return err
	}
	if err := in.writeConfig(ctx, configPath, cfg, o.Email != nil); err != nil {
		return err
	}
	unitChanged, err := in.writeSaying(ctx, unitPath, unit, 0o644, "", "Wrote "+in.path(unitPath))
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
	url, published := in.serve(ctx, "http://"+listen)
	switch {
	case !existed:
		in.say(hubNext(url))
	case !in.changed && !published && !in.Refreshing:
		in.say("tender-hub is already installed and up to date. Nothing to change.")
	}
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
