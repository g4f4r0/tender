// Package install sets up Tender on a machine: the restic binary, the tender user, the config,
// the hub token, the hub's settings and the systemd units. Every path goes through a root prefix and every command
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
	credentialDir = "/etc/tender/credentials" // holds the daemon's hub-token only
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

	// CheckHub asks the hub whether it takes token for machine. "install daemon" runs it before it
	// changes anything, whenever the hub address or the token is new. Nil skips the check.
	CheckHub func(ctx context.Context, hub, machine, token string) error

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

const (
	daemonUnitPath   = unitDir + "/tenderd.service"
	hubUnitPath      = unitDir + "/tenderhub.service"
	daemonConfigPath = etcDir + "/daemon.toml"
)

// DaemonOptions are the settings of "tender install daemon".
type DaemonOptions struct {
	Hub, Machine string
	Token        string // empty keeps the hub-token already installed
	Force        bool   // replace an existing daemon.toml
}

// Daemon installs and starts the daemon. Running it again changes only what differs.
func (in *Installer) Daemon(ctx context.Context, o DaemonOptions) error {
	if err := in.preflight(); err != nil {
		return err
	}
	// A tender-agent unit is an install from v0.2.1 or earlier. It is checked here and moved below,
	// once every check has passed, right before tenderd takes over.
	old := in.installed(oldUnitPath(oldDaemonUnit))
	if old {
		if err := in.checkOldDaemon(); err != nil {
			return err
		}
	}
	tokenPath := filepath.Join(credentialDir, "hub-token")
	if _, err := os.Stat(in.path(tokenPath)); o.Token == "" && err != nil {
		return errors.New("this machine has no hub token yet. Create one with \"tender machines add NAME\", then pass it with --token-file PATH or pipe it in on stdin")
	}
	configPath, installedConfig := daemonConfigPath, daemonConfigPath
	if old && !in.installed(daemonConfigPath) {
		installedConfig = oldConfigPath
	}
	var cfg []byte
	if _, err := os.Stat(in.path(installedConfig)); err != nil || o.Force {
		if o.Hub == "" || o.Machine == "" {
			return errors.New("this machine has no daemon config yet. Pass --hub URL and --machine NAME")
		}
		paths, err := in.mountPaths(ctx)
		if err != nil {
			return err
		}
		a := config.Daemon{Hub: o.Hub, Machine: o.Machine, Backups: config.Backups{Paths: paths, Exclude: config.DefaultExclude}}
		if cfg, err = encode(a); err != nil {
			return err
		}
		if err := checkConfig(cfg, func(p string) error { _, err := config.LoadDaemon(p); return err }); err != nil {
			return err
		}
	}
	if err := in.checkHub(ctx, o, cfg != nil, installedConfig); err != nil {
		return err
	}
	unit, err := in.daemonUnit()
	if err != nil {
		return err
	}
	unitPath := daemonUnitPath
	_, statErr := os.Stat(in.path(unitPath))
	existed := statErr == nil || old

	in.changed = false
	if err := in.installRestic(ctx); err != nil {
		return err
	}
	if err := in.prepare(ctx); err != nil {
		return err
	}
	if old {
		if err := in.moveOldDaemon(ctx); err != nil {
			return err
		}
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
	if err := in.enable(ctx, "tenderd", unitChanged, existed); err != nil {
		return err
	}
	if old {
		if err := in.removeOldUnit(ctx, oldDaemonUnit, "tenderd"); err != nil {
			return err
		}
	}
	switch {
	case !existed:
		in.say(daemonNext(o.Machine))
	case !in.changed && !in.Refreshing:
		in.say("tenderd is already installed and up to date. Nothing to change.")
	}
	return nil
}

// daemonUnit is tenderd.service. The daemon joins the docker group, when there is one, to reach
// Docker's socket, and the systemd-journal group, when there is one, to read the whole journal
// with journalctl. A group that doesn't exist is left out, since systemd won't start a unit whose
// group doesn't exist. Installing Docker later needs "tender install refresh".
func (in *Installer) daemonUnit() ([]byte, error) {
	unit, err := units.ReadFile("units/tenderd.service")
	if err != nil {
		return nil, err
	}
	var lines, groups []string
	if in.hasGroup("docker") {
		lines = append(lines, "# Docker's socket. The docker group is root-equivalent: whoever is in it controls the machine.")
		groups = append(groups, "docker")
	}
	if in.hasGroup("systemd-journal") {
		lines = append(lines, "# The whole journal, for machine logs. Logs can hold secrets; the hub clears them an hour after they are read.")
		groups = append(groups, "systemd-journal")
	}
	if len(groups) == 0 {
		return unit, nil
	}
	add := strings.Join(lines, "\n") + "\nSupplementaryGroups=" + strings.Join(groups, " ") + "\n"
	return bytes.Replace(unit, []byte("Group=tender\n"), []byte("Group=tender\n"+add), 1), nil
}

// hasGroup reports whether /etc/group has the group called name.
func (in *Installer) hasGroup(name string) bool {
	b, err := os.ReadFile(in.path("/etc/group"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, name+":") {
			return true
		}
	}
	return false
}

// checkHub runs CheckHub when the config or the token is new: newConfig means o's hub and machine
// go into a new config; otherwise they come from the installed config at installedConfig.
func (in *Installer) checkHub(ctx context.Context, o DaemonOptions, newConfig bool, installedConfig string) error {
	if in.CheckHub == nil || in.Refreshing || (!newConfig && o.Token == "") {
		return nil
	}
	hub, machine, token := o.Hub, o.Machine, o.Token
	if !newConfig {
		c, err := config.LoadDaemon(in.path(installedConfig))
		if err != nil {
			return err
		}
		hub, machine = c.Hub, c.Machine
	}
	if token == "" {
		b, err := os.ReadFile(in.path(filepath.Join(credentialDir, "hub-token")))
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	}
	return in.CheckHub(ctx, hub, machine, token)
}

// daemonNext says what to do after a first daemon install.
func daemonNext(machine string) string {
	if machine == "" {
		machine = "MACHINE"
	}
	return `
The daemon is running. It gets its destination, schedule and keys from the hub. Next:
  1. Set when this machine backs up, from any machine that can reach the hub:
       tender backups set ` + machine + ` --destination DESTINATION --schedule "30 3 * * *" --retention-days 30
  2. Watch the daemon: journalctl -u tenderd -f`
}

// hubNext says what to do after a first hub install. url is empty when the hub is not published,
// listen is where the hub listens, and email says whether alert emails are set up.
func hubNext(url, listen string, email bool) string {
	hub := "http://" + listen
	where := "The tender command on this machine finds it by itself."
	if url != "" {
		hub = url
		where += "\nAnywhere else, set TENDER_HUB=" + url + " or pass --hub " + url + "."
	}
	if url == "" {
		url = "HUB_URL"
	}
	next := `
The hub is running at ` + hub + `.
` + where + `

Next:
  1. Add a destination for the backups; it asks for the S3 keys and makes the repository password:
       tender destinations add NAME --endpoint URL --bucket BUCKET
  2. Add a machine and keep the token it prints once:
       tender machines add NAME
  3. Set when that machine backs up:
       tender backups set NAME --destination NAME --schedule "30 3 * * *" --retention-days 30
  4. Choose who gets alerts, then check that the email arrives:
       tender recipients add you@example.com
       tender email test
  5. Install the daemon on that machine, with the token when it asks:
       curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh \
         | sudo sh -s -- daemon --hub ` + url + ` --machine NAME`
	if !email {
		next += `

Alert emails are off until the hub has an SMTP server. Run this again with the settings:
  sudo tender install hub --smtp-host HOST --smtp-from "Tender <alerts@example.com>" --smtp-username USER`
	}
	return next
}

// HubOptions are the settings of "tender install hub". Empty options leave what hub.env has.
type HubOptions struct {
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
}

// Hub installs and starts the hub, then publishes it with Tailscale Serve. The hub's settings
// live in /etc/tender/hub.env, which keeps its encryption key on every run.
func (in *Installer) Hub(ctx context.Context, o HubOptions) error {
	if err := in.preflight(); err != nil {
		return err
	}
	old, err := os.ReadFile(in.path(hubEnvPath))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	env, newKey, err := hubEnv(old, o)
	if err != nil {
		return err
	}
	unit, err := units.ReadFile("units/tenderhub.service")
	if err != nil {
		return err
	}
	oldUnit := in.installed(oldUnitPath(oldHubUnit)) // a tender-hub unit, from v0.2.1 or earlier
	_, statErr := os.Stat(in.path(hubUnitPath))
	existed := statErr == nil || oldUnit

	in.changed = false
	if err := in.prepare(ctx); err != nil {
		return err
	}
	what := "Updated " + in.path(hubEnvPath)
	if newKey {
		what = "Saved " + in.path(hubEnvPath) + " with a new encryption key. Back this file up: without the key, the hub cannot read the secrets it stores."
	}
	if _, err := in.writeSaying(ctx, hubEnvPath, env, 0o600, "", what); err != nil {
		return err
	}
	unitChanged, err := in.writeSaying(ctx, hubUnitPath, unit, 0o644, "", "Wrote "+in.path(hubUnitPath))
	if err != nil {
		return err
	}
	if oldUnit { // both listen on the same port, so the old hub stops right before the new one starts
		if err := in.stopOldUnit(ctx, oldHubUnit); err != nil {
			return err
		}
	}
	if err := in.enable(ctx, "tenderhub", unitChanged, existed); err != nil {
		return err
	}
	if oldUnit {
		if err := in.removeOldUnit(ctx, oldHubUnit, "tenderhub"); err != nil {
			return err
		}
	}
	listen := envValue(env, "TENDER_LISTEN")
	if listen == "" {
		listen = defaultListen
	}
	url, published := in.serve(ctx, "http://"+listen)
	switch {
	case !existed:
		in.say(hubNext(url, listen, envValue(env, "TENDER_SMTP_HOST") != ""))
	case !in.changed && !published && !in.Refreshing:
		in.say("tenderhub is already installed and up to date. Nothing to change.")
	}
	return nil
}
