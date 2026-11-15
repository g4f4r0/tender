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
	// actionTimeout bounds one action.
	actionTimeout = 5 * time.Minute
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

// Daemon long-polls the hub for this machine's actions and runs them one at a time. It claims each
// before it runs it, and keeps each report until the hub has it. Set the exported fields after
// NewDaemon. Run drives it; nothing else does.
type Daemon struct {
	Hub      *client.Client
	Machine  string
	Log      *slog.Logger
	Every    time.Duration // the wait before polling again after an error, or sending a waiting report again
	Services Runner        // runs the service actions; without it they fail
	// StateDir is the daemon's state folder, where reports wait in actions/ until the hub has them.
	// Empty keeps them in memory only.
	StateDir string

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
	for ctx.Err() == nil {
		d.deliver(ctx)
		wait := pollWait
		if len(d.waiting) > 0 {
			wait = 0
		}
		var list server.List[Action]
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

func (d *Daemon) sleep(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(d.Every):
	}
}

// claimFor bounds the tries of one claim. It is under the hub's 30 s, after which it hands the
// action out again.
const claimFor = 25 * time.Second

// run claims an action, does it and reports how it ended. The output, which may be logs, is never
// logged.
func (d *Daemon) run(ctx context.Context, a Action) {
	if !d.claim(ctx, a) {
		return
	}
	service := a.Service != nil
	if service && d.Services != nil {
		d.Services.Lock()
	}
	r := &sent{ID: a.ID, Key: client.NewKey(), Type: a.Type}
	d.keep(r) // before it runs, so a daemon that stops meanwhile still reports it
	r.Patch = d.do(ctx, a)
	d.keep(r)
	d.send(ctx, r)
	if service && d.Services != nil {
		d.Services.Unlock()
		if a.Type != TypeLogs {
			d.Services.Changed()
		}
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
func (d *Daemon) do(ctx context.Context, a Action) *Patch {
	actx, cancel := context.WithTimeout(ctx, actionTimeout)
	output, err := d.execute(actx, a)
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
func (d *Daemon) execute(ctx context.Context, a Action) (string, error) {
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
