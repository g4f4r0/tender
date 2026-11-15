package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/server"
)

const (
	// pollWait is how long the hub holds a long poll; the client allows pollTimeout for it.
	pollWait    = 25
	pollTimeout = 40 * time.Second
	// actionTimeout bounds one action, and upgradeTimeout an upgrade of the system's packages.
	actionTimeout  = 5 * time.Minute
	upgradeTimeout = 40 * time.Minute
	// maxEncoded keeps the report of a logs action under the hub's 1 MiB body limit, however many
	// characters JSON has to escape.
	maxEncoded = 900 << 10
)

// Runner does the service actions on the daemon. The services domain implements it.
type Runner interface {
	// Service starts, stops or restarts the service, or returns its last lines of logs since then.
	Service(ctx context.Context, typ, service string, lines int, since time.Duration) (string, error)
	// The lock makes a service action and its report to the hub take turns with the services
	// report, so that report can't carry a state from before the action and land after it.
	sync.Locker
	// Changed asks for a services report now, after a start, stop or restart.
	Changed()
}

// Backups tests backup destinations on the daemon. The backups domain implements it.
type Backups interface {
	// TestDestination opens this machine's repository at the destination target describes, or
	// makes it, and says what it found.
	TestDestination(ctx context.Context, target json.RawMessage) (string, error)
}

// System does the actions on the machine itself, outside the daemon's sandbox. cmd/tender wires it.
type System interface {
	// Upgrade upgrades the system's packages, security or all, and returns what the upgrade printed.
	Upgrade(ctx context.Context, packages string) (string, error)
	// ResumeUpgrade waits for the upgrade a daemon before this one started, or reads how it ended,
	// and never starts one.
	ResumeUpgrade(ctx context.Context) (string, error)
	// Update starts the job that installs a Tender release. The job restarts the daemon, so how it
	// ended comes from UpdateResult, maybe in the next daemon.
	Update(ctx context.Context, version string) error
	// UpdateResult returns how the last update job to version ended, and false while there is no
	// result for it yet.
	UpdateResult(version string) (status, output string, ok bool)
	// ClearUpdateResult forgets that result, once the hub has it.
	ClearUpdateResult()
	// UpdateRunning reports whether the update job still runs.
	UpdateRunning(ctx context.Context) bool
}

// Daemon long-polls the hub for this machine's actions and runs them one at a time. It claims each
// before it runs it, and keeps each report until the hub has it. Set the exported fields after
// NewDaemon. Run drives it; nothing else does.
type Daemon struct {
	Hub      *client.Client
	Machine  string
	Log      *slog.Logger
	Every    time.Duration // the wait before polling again after an error, or sending a waiting report again
	Services Runner        // runs the service actions; without it they fail
	Backups  Backups       // runs the destination tests; without it they fail
	// StateDir is the daemon's state folder, where reports wait in actions/ until the hub has them.
	// Empty keeps them in memory only.
	StateDir string
	System   System // runs the update and upgrade actions; without it they fail
	// Busy marks the daemon busy while it runs an action, so an update of Tender waits for it, and
	// says when an update runs, so the action waits. Nil never waits.
	Busy func() (release func(), err error)

	poll    *client.Client  // Hub, with room for the long poll
	pollErr string          // the last poll error logged, so it is logged once
	waiting map[int64]*sent // the reports the hub doesn't have yet, by action id
	sendErr string          // the last report error logged, so it is logged once
}

// NewDaemon returns a daemon for machine.
func NewDaemon(hub *client.Client, machine string, log *slog.Logger) *Daemon {
	return &Daemon{Hub: hub, Machine: machine, Log: log, Every: time.Minute, poll: hub.WithTimeout(pollTimeout), waiting: map[int64]*sent{}}
}

func (d *Daemon) path(rest string) string {
	return "/v1/machines/" + url.PathEscape(d.Machine) + "/actions" + rest
}

// Run asks the hub for actions and runs them, until ctx ends. Before each poll it sends the reports
// still waiting; while some wait, it polls without waiting and tries again every Every.
func (d *Daemon) Run(ctx context.Context) {
	d.load()
	d.resumeUpgrades(ctx)
	for ctx.Err() == nil {
		d.deliver(ctx)
		wait := pollWait
		if len(d.waiting) > 0 {
			wait = 0
		}
		var list server.List[Handout]
		err := d.poll.Do(ctx, http.MethodGet, d.path("?wait="+strconv.Itoa(wait)), "", nil, &list)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err.Error() != d.pollErr {
				d.Log.Warn("could not ask the hub for actions", "error", err)
				d.pollErr = err.Error()
			}
			d.sleep(ctx)
			continue
		}
		d.pollErr = ""
		for _, a := range list.Data {
			d.run(ctx, a)
		}
		if len(d.waiting) > 0 && len(list.Data) == 0 {
			d.sleep(ctx)
		}
	}
}

// updateCheck is how often the daemon looks for the update job's result while an update's report
// waits for it.
const updateCheck = 5 * time.Second

func (d *Daemon) sleep(ctx context.Context) {
	wait := d.Every
	for _, r := range d.waiting {
		if r.Patch == nil && r.Type == TypeUpdate {
			wait = min(wait, updateCheck)
		}
	}
	select {
	case <-ctx.Done():
	case <-time.After(wait):
	}
}

// claimFor bounds the tries of one claim. It is under the hub's 30 s, after which it hands the
// action out again.
const claimFor = 25 * time.Second

// run claims an action, does it and reports how it ended. The output, which may be logs, is never
// logged.
func (d *Daemon) run(ctx context.Context, h Handout) {
	a := h.Action
	if a.Type == TypeUpdate {
		d.runUpdate(ctx, a)
		return
	}
	if d.Busy != nil {
		release, err := d.Busy()
		if err != nil { // Tender is updating; the hub hands the action out again in 30 s
			d.Log.Info("waiting for the update of Tender before an action", "id", a.ID, "type", a.Type, "error", err)
			return
		}
		defer release()
	}
	if !d.claim(ctx, a) {
		return
	}
	service := a.Service != nil
	if service && d.Services != nil {
		d.Services.Lock()
	}
	r := &sent{ID: a.ID, Key: client.NewKey(), Type: a.Type, Packages: deref(a.Packages)}
	d.keep(r) // before it runs, so a daemon that stops meanwhile still reports it
	r.Patch = d.do(ctx, h)
	if a.Type == TypeUpgrade && ctx.Err() != nil {
		r.Patch = nil // the daemon stops, and the upgrade runs on in its unit: the next daemon reports it
		return
	}
	d.keep(r)
	d.send(ctx, r)
	if service && d.Services != nil {
		d.Services.Unlock()
		if a.Type != TypeLogs {
			d.Services.Changed()
		}
	}
}

// resumeUpgrades reports the upgrades that ran when the daemon before this one stopped. An upgrade
// runs on in its unit, so this waits for it, or reads how it ended, and never starts another.
func (d *Daemon) resumeUpgrades(ctx context.Context) {
	for _, r := range d.waiting {
		if r.Patch != nil || r.Type != TypeUpgrade {
			continue
		}
		if d.Busy != nil {
			if release, err := d.Busy(); err == nil {
				defer release()
			}
		}
		a := Action{ID: r.ID, Type: TypeUpgrade, Packages: &r.Packages}
		d.Log.Info("waiting for the upgrade the daemon before this one started", "id", r.ID)
		actx, cancel := context.WithTimeout(ctx, upgradeTimeout)
		output, err := "", errors.New("this daemon can't upgrade the machine")
		if d.System != nil {
			output, err = d.System.ResumeUpgrade(actx)
		}
		cancel()
		if ctx.Err() != nil {
			return // stopping again: the next daemon picks it up
		}
		p := Patch{Status: StatusCompleted}
		if err != nil {
			p.Status, output = StatusFailed, failure(a, err)
		}
		output = fitJSON(output)
		p.Output = &output
		r.Patch = &p
		d.keep(r)
		d.Log.Info("action finished", "id", r.ID, "type", r.Type, "status", p.Status)
	}
}

// claim tells the hub the daemon is about to run the action, and says whether it may. Every try
// reuses one key: a claim that reached the hub but whose answer was lost gets that answer again,
// so the action runs once. When no answer comes in time it doesn't run; a report that it failed
// waits for the hub, in case the claim did reach it.
func (d *Daemon) claim(ctx context.Context, a Action) bool {
	cctx, cancel := context.WithTimeout(ctx, claimFor)
	defer cancel()
	key := client.NewKey()
	for {
		err := d.Hub.Do(cctx, http.MethodPatch, d.path("/"+strconv.FormatInt(a.ID, 10)), key, Patch{Status: StatusInProgress}, nil)
		if err == nil {
			return true
		}
		if !client.Retryable(err) && cctx.Err() == nil { // the hub answered no: it expired, or isn't pending
			d.Log.Info("the hub did not let this daemon run an action", "id", a.ID, "type", a.Type, "error", err)
			return false
		}
		select {
		case <-time.After(time.Second):
			continue
		case <-cctx.Done():
		}
		d.Log.Warn("could not claim an action from the hub, so it did not run", "id", a.ID, "type", a.Type, "error", err)
		out := notClaimed
		d.keep(&sent{ID: a.ID, Key: client.NewKey(), Type: a.Type, Patch: &Patch{Status: StatusFailed, Output: &out}})
		return false
	}
}

// do runs the action and returns its report.
func (d *Daemon) do(ctx context.Context, h Handout) *Patch {
	a := h.Action
	timeout := actionTimeout
	if a.Type == TypeUpgrade {
		timeout = upgradeTimeout
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	output, err := d.execute(actx, h)
	cancel()
	p := Patch{Status: StatusCompleted}
	if err != nil {
		p.Status, output = StatusFailed, failure(a, err)
	}
	if output != "" || a.Type == TypeLogs {
		output = fitJSON(output)
		p.Output = &output
	}
	d.Log.Info("action finished", "id", a.ID, "type", a.Type, "service", deref(a.Service), "status", p.Status)
	return &p
}

// execute runs the action and returns its output.
func (d *Daemon) execute(ctx context.Context, h Handout) (string, error) {
	a := h.Action
	lines := DefaultLines
	if a.Lines != nil {
		lines = *a.Lines
	}
	var since time.Duration
	if a.Since != nil {
		var err error
		if since, err = ParseSince(*a.Since); err != nil {
			return "", err
		}
	}
	switch {
	case a.Service != nil && d.Services == nil:
		return "", errors.New("this daemon can't run service actions")
	case a.Service != nil && (a.Type == TypeStart || a.Type == TypeStop || a.Type == TypeRestart || a.Type == TypeLogs):
		return d.Services.Service(ctx, a.Type, *a.Service, lines, since)
	case a.Service == nil && a.Type == TypeLogs:
		return ReadJournal(ctx, Query{Unit: deref(a.Unit), Priority: deref(a.Priority), Since: since, Lines: lines}, time.Now())
	case a.Type == TypeTest && d.Backups == nil:
		return "", errors.New("this daemon can't test destinations")
	case a.Type == TypeTest:
		return d.Backups.TestDestination(ctx, h.Target)
	case a.Type == TypeUpgrade && d.System == nil:
		return "", errors.New("this daemon can't upgrade the machine")
	case a.Type == TypeUpgrade:
		return d.System.Upgrade(ctx, deref(a.Packages))
	}
	return "", fmt.Errorf("this daemon doesn't know the action %q; update Tender on this machine", a.Type)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// failure says why an action failed, as one sentence.
func failure(a Action, err error) string {
	var msg string
	switch {
	case a.Type == TypeTest:
		msg = fmt.Sprintf("Could not reach the destination %s from %s: %v", deref(a.Destination), a.Machine, err)
	case a.Type == TypeUpgrade:
		msg = fmt.Sprintf("Could not upgrade %s: %v", PackagesText(deref(a.Packages)), err)
	case a.Service == nil:
		msg = fmt.Sprintf("Could not read the machine's logs: %v", err)
	case a.Type == TypeLogs:
		msg = fmt.Sprintf("Could not read the logs of %s: %v", *a.Service, err)
	default:
		msg = fmt.Sprintf("Could not %s %s: %v", a.Type, *a.Service, err)
	}
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

// fitJSON drops the oldest lines of s until it fits in maxEncoded bytes as a JSON string.
func fitJSON(s string) string {
	for {
		b, err := json.Marshal(s)
		if err == nil && len(b) <= maxEncoded {
			return s
		}
		i := strings.IndexByte(s[len(s)/4:], '\n')
		if i < 0 {
			return ""
		}
		s = s[len(s)/4+i+1:]
	}
}

// runUpdate claims an update and starts the job that installs the release. The job restarts the
// daemon when it swaps the binary, so the report waits, without a result, in the state folder: this
// daemon or the next one sends it once the job has left its result. The update doesn't mark the
// daemon busy: the job itself waits for a running backup or action, as long as it takes.
func (d *Daemon) runUpdate(ctx context.Context, a Action) {
	if !d.claim(ctx, a) {
		return
	}
	r := &sent{ID: a.ID, Key: client.NewKey(), Type: a.Type, Version: deref(a.Version), Started: time.Now().UTC()}
	d.keep(r)
	var err error
	if d.System == nil {
		err = errors.New("this daemon can't update Tender")
	} else {
		err = d.System.Update(ctx, r.Version)
	}
	if ctx.Err() != nil {
		return // the job restarts the daemon; the next one reports
	}
	if !d.updateResult(ctx, r) {
		if err == nil {
			return // the job runs on; deliver sends the report once it ends
		}
		out := fmt.Sprintf("Could not update Tender to %s: %v.", r.Version, err)
		r.Patch = &Patch{Status: StatusFailed, Output: &out}
		d.keep(r)
	}
	d.Log.Info("action finished", "id", a.ID, "type", a.Type, "version", r.Version, "status", r.Patch.Status)
	if d.send(ctx, r) && d.System != nil {
		d.System.ClearUpdateResult()
	}
}

// updateResult fills in an update's report from the job's result, and says whether it did. While
// the job runs, however long it waits for a backup, the report waits too; a job that ended without
// a result failed.
func (d *Daemon) updateResult(ctx context.Context, r *sent) bool {
	if d.System != nil {
		if status, output, ok := d.System.UpdateResult(r.Version); ok {
			if status != StatusCompleted {
				status = StatusFailed
			}
			r.Patch = &Patch{Status: status, Output: &output}
			d.keep(r)
			return true
		}
		if d.System.UpdateRunning(ctx) {
			return false
		}
		if _, _, ok := d.System.UpdateResult(r.Version); ok { // it ended just now
			return d.updateResult(ctx, r)
		}
	}
	out := fmt.Sprintf("The update job to Tender %s ended without saying how. See /var/lib/tender/daemon/tenderupdate.log.", r.Version)
	r.Patch = &Patch{Status: StatusFailed, Output: &out}
	d.keep(r)
	return true
}
