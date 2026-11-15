package security

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
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

// Upgrade upgrades the machine's packages in the transient unit tenderupgrade, outside the daemon's
// sandbox: after apt-get update, the security updates with unattended-upgrade, or every pending update
// with apt-get upgrade --with-new-pkgs, keeping the current config files. It returns what the upgrade printed, its end when it is long.
func Upgrade(ctx context.Context, packages string) (string, error) {
	args := []string{"--wait", "--collect", "--quiet", "--pipe", "--unit", "tenderupgrade",
		"--setenv", "DEBIAN_FRONTEND=noninteractive", "--setenv", "LC_ALL=C"}
	// Both read the package lists fresh first. upgrade --with-new-pkgs installs new packages, such as
	// a new kernel, but never removes one.
	switch packages {
	case "security":
		args = append(args, "/bin/sh", "-c", "apt-get update -q && exec unattended-upgrade -v")
	case "all":
		args = append(args, "/bin/sh", "-c", "apt-get update -q && exec apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade --with-new-pkgs")
	default:
		return "", fmt.Errorf("packages must be security or all, not %q", packages)
	}
	cmd := exec.CommandContext(ctx, config.SystemdRun(), args...)
	cmd.Stdin = nil
	var out tailBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := strings.ToValidUTF8(out.String(), "�")
	if err != nil {
		last := lastLine(text)
		if last == "" {
			last = err.Error()
		}
		return text, fmt.Errorf("the upgrade failed: %s", last)
	}
	return text, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// tailBuffer keeps the last maxUpgradeOutput bytes written to it, from a line start.
type tailBuffer struct{ b bytes.Buffer }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b.Write(p)
	if t.b.Len() > 2*maxUpgradeOutput {
		keep := t.b.Bytes()[t.b.Len()-maxUpgradeOutput:]
		if i := bytes.IndexByte(keep, '\n'); i >= 0 {
			keep = keep[i+1:]
		}
		t.b = *bytes.NewBuffer(append([]byte(nil), keep...))
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	b := t.b.Bytes()
	if len(b) > maxUpgradeOutput {
		b = b[len(b)-maxUpgradeOutput:]
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return string(b)
}
