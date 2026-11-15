package security

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/config"
)

// collectEvery is how often the daemon collects; it also collects when it starts and after an upgrade.
const collectEvery = 15 * time.Minute

// Daemon collects this machine's security state and reports it to the hub. Set the exported fields
// after NewDaemon. Run drives it.
type Daemon struct {
	Hub       *client.Client
	Machine   string
	Log       *slog.Logger
	Collector *Collector
	Every     time.Duration // between collections
	Retry     time.Duration // before sending a report the hub didn't take again

	again   chan struct{}
	pending *Report // collected, not yet taken by the hub
	hubErr  string
}

// NewDaemon returns a daemon for machine.
func NewDaemon(hub *client.Client, machine string, c *Collector, log *slog.Logger) *Daemon {
	return &Daemon{Hub: hub, Machine: machine, Log: log, Collector: c, Every: collectEvery, Retry: time.Minute, again: make(chan struct{}, 1)}
}

// Collect asks for a collection now, as after an upgrade.
func (d *Daemon) Collect() {
	select {
	case d.again <- struct{}{}:
	default:
	}
}

// Run collects now and every Every until ctx ends, and sends each report. A report the hub can't take
// is sent again every Retry, until a newer one replaces it.
func (d *Daemon) Run(ctx context.Context) {
	collect := time.NewTicker(d.Every)
	defer collect.Stop()
	retry := time.NewTicker(d.Retry)
	defer retry.Stop()
	d.collect(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-collect.C:
			d.collect(ctx)
		case <-d.again:
			d.collect(ctx)
		case <-retry.C:
			if d.pending != nil {
				d.send(ctx)
			}
		}
	}
}

func (d *Daemon) collect(ctx context.Context) {
	r := d.Collector.Collect(ctx)
	if ctx.Err() != nil {
		return
	}
	d.pending = &r
	d.send(ctx)
}

func (d *Daemon) send(ctx context.Context) {
	err := d.Hub.Do(ctx, http.MethodPut, "/v1/machines/"+url.PathEscape(d.Machine)+"/security", client.NewKey(), d.pending, nil)
	switch {
	case err == nil:
		d.pending, d.hubErr = nil, ""
	case !client.Retryable(err):
		d.Log.Warn("the hub refused the security report", "error", err)
		d.pending = nil
	case err.Error() != d.hubErr && ctx.Err() == nil:
		d.Log.Warn("could not send the security report; it is sent again", "error", err)
		d.hubErr = err.Error()
	}
}

// maxUpgradeOutput is how much of an upgrade's output the action keeps: its end.
const maxUpgradeOutput = 256 << 10

// The transient unit of an upgrade, and its output's file in the daemon's state folder.
const (
	UpgradeUnit = "tenderupgrade"
	UpgradeLog  = "tenderupgrade.log"
)

// upgradeEvery is how often the daemon asks systemd whether the upgrade ended.
var upgradeEvery = 2 * time.Second

// Upgrade upgrades the machine's packages in the transient unit tenderupgrade, outside the daemon's
// sandbox: after apt-get update, the security updates with unattended-upgrade, or every pending update
// with apt-get upgrade --with-new-pkgs, keeping the current config files. Nothing pipes its output,
// so the daemon stopping never stops apt in the middle of dpkg: the output goes to UpgradeLog in
// stateDir, and systemctl show says how the unit ended. When an upgrade already runs, it waits for
// that one instead of starting another. resume is a daemon that restarted while an upgrade ran: it
// only waits for that one, or reads how it ended. It returns the end of the upgrade's output.
func Upgrade(ctx context.Context, stateDir, packages string, resume bool) (string, error) {
	var script string
	switch packages {
	case "security":
		script = "apt-get update -q && exec unattended-upgrade -v"
	case "all":
		script = "apt-get update -q && exec apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade --with-new-pkgs"
	default:
		return "", fmt.Errorf("packages must be security or all, not %q", packages)
	}
	log := filepath.Join(stateDir, UpgradeLog)
	u, err := config.ShowUnit(ctx, UpgradeUnit)
	switch {
	case err != nil:
		return "", err
	case u.Running(): // started by the daemon before this one, or by the last action: wait for it
	case resume && !u.Exists():
		return "", errors.New("the upgrade didn't start before the daemon stopped. Run it again")
	case resume: // it ended while no daemon watched: read how
	default:
		config.ClearUnit(ctx, UpgradeUnit)
		if err := os.WriteFile(log, nil, 0o600); err != nil {
			return "", err
		}
		env := []string{"DEBIAN_FRONTEND=noninteractive", "LC_ALL=C"}
		if err := config.StartUnit(ctx, UpgradeUnit, log, env, "/bin/sh", "-c", script); err != nil {
			return "", err
		}
	}
	u, err = config.WaitUnit(ctx, UpgradeUnit, upgradeEvery)
	text := strings.ToValidUTF8(tail(log), "\uFFFD")
	if err != nil {
		return text, fmt.Errorf("the upgrade still runs in the %s unit, and the daemon stopped waiting for it (%w). Its output is in %s", UpgradeUnit, err, log)
	}
	config.ClearUnit(ctx, UpgradeUnit)
	if !u.Succeeded() {
		last := lastLine(text)
		if last == "" {
			last = fmt.Sprintf("it exited with status %d", u.Status)
		}
		return text, fmt.Errorf("the upgrade failed: %s", last)
	}
	return text, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// tail returns the last maxUpgradeOutput bytes of the file at path, from a line start.
func tail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err == nil && fi.Size() > maxUpgradeOutput {
		if _, err := f.Seek(fi.Size()-maxUpgradeOutput, io.SeekStart); err != nil {
			return ""
		}
	}
	b, _ := io.ReadAll(io.LimitReader(f, maxUpgradeOutput))
	if len(b) == maxUpgradeOutput {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return string(b)
}
