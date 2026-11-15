package services

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
	"sync/atomic"
	"time"

	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/server"
)

const (
	reportEvery = time.Minute
	// pollWait is how long the hub holds a long poll; the client allows pollTimeout for it.
	pollWait    = 25
	pollTimeout = 40 * time.Second
	// actionTimeout bounds one start, stop, restart or logs read.
	actionTimeout = 5 * time.Minute
	// maxEncoded keeps the report of a logs action under the hub's 1 MiB body limit, however
	// many characters JSON has to escape.
	maxEncoded = 900 << 10
)

// listTimeout bounds listing and inspecting every container for one report.
var listTimeout = 30 * time.Second

// Daemon reports this machine's Docker containers to the hub and runs the actions the hub
// queues for them. Set the exported fields after NewDaemon. Run drives it; nothing else does.
type Daemon struct {
	Hub     *client.Client
	Machine string
	Docker  *Docker
	Log     *slog.Logger
	Every   time.Duration // between reports, a minute; also the wait before polling again after an error

	poll *client.Client // Hub, with room for the long poll
	// turn lets one report (list and PUT) or one action (run and PATCH) go at a time, so a report
	// can't carry a state read before an action and reach the hub after the action's result.
	turn    sync.Mutex
	report  chan struct{} // asks for a report now, after an action changed a container
	found   chan struct{} // Docker became reachable, so the action loop can start polling
	reached atomic.Bool   // the last report reached Docker
	seen    bool          // Docker answered since the daemon started, so losing it is an outage
	problem string        // the last Docker problem logged, so it is logged once
	hubErr  string        // the last hub error logged, so it is logged once
	pollErr string
}

// NewDaemon returns a daemon that reaches Docker at socket.
func NewDaemon(hub *client.Client, machine, socket string, log *slog.Logger) *Daemon {
	return &Daemon{
		Hub: hub, Machine: machine, Docker: NewDocker(socket), Log: log, Every: reportEvery,
		poll:   hub.WithTimeout(pollTimeout),
		report: make(chan struct{}, 1), found: make(chan struct{}, 1),
	}
}

func (d *Daemon) path(rest string) string {
	return "/v1/machines/" + url.PathEscape(d.Machine) + "/services" + rest
}

func (d *Daemon) actionsPath(rest string) string {
	return "/v1/machines/" + url.PathEscape(d.Machine) + "/service-actions" + rest
}

// Run reports the containers now and every minute, and runs queued actions, until ctx ends. A
// machine that never had Docker reports nothing and polls for nothing; it checks for Docker each
// minute.
func (d *Daemon) Run(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.runActions(ctx)
	}()
	d.reportNow(ctx)
	t := time.NewTicker(d.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			<-done
			return
		case <-t.C:
		case <-d.report:
		}
		d.reportNow(ctx)
	}
}

// reportNow sends the hub the full container list. It first pings Docker: when its socket is
// there but doesn't answer, or this daemon has seen Docker before and can't reach it now, it tells
// the hub Docker is unreachable. A machine that never had Docker sends nothing. A list that fails
// after Docker answered is logged, and the hub keeps the last one.
func (d *Daemon) reportNow(ctx context.Context) {
	d.turn.Lock()
	defer d.turn.Unlock()
	if err := d.Docker.Ping(ctx); err != nil {
		if ctx.Err() == nil {
			d.unreachable(ctx, err, errors.Is(err, errUnreachable) || d.seen)
		}
		return
	}
	d.seen = true
	lctx, cancel := context.WithTimeout(ctx, listTimeout)
	list, err := d.Docker.Containers(lctx)
	cancel()
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, errUnreachable), errors.Is(err, errNoDocker), errors.Is(err, errDenied): // lost meanwhile
		d.unreachable(ctx, err, true)
		return
	case err != nil: // Docker answers, so it isn't down; the hub keeps the last list
		d.dockerProblem(err, false)
		return
	}
	if !d.reached.Swap(true) {
		if d.problem != "" {
			d.Log.Info("Docker is reachable", "containers", len(list))
		}
		select {
		case d.found <- struct{}{}:
		default:
		}
	}
	d.problem = ""
	d.send(ctx, Report{Docker: DockerRunning, Services: list})
}

// unreachable logs once why Docker can't be reached and, when tell, tells the hub.
func (d *Daemon) unreachable(ctx context.Context, err error, tell bool) {
	d.reached.Store(false)
	d.dockerProblem(err, tell)
	if tell {
		d.send(ctx, Report{Docker: DockerUnreachable})
	}
}

// send puts one report on the hub.
func (d *Daemon) send(ctx context.Context, r Report) {
	err := d.Hub.Do(ctx, http.MethodPut, d.path(""), "", r, nil)
	switch {
	case err == nil:
		d.hubErr = ""
	case ctx.Err() != nil:
	case err.Error() != d.hubErr: // the usage reports already warn each minute while the hub is down
		d.Log.Warn("could not send the services", "error", err)
		d.hubErr = err.Error()
	}
}

// dockerProblem logs why Docker can't be listed, once until it changes. told says the hub
// heard that Docker is unreachable.
func (d *Daemon) dockerProblem(err error, told bool) {
	problem, log, msg := "other", d.Log.Warn, "could not list the Docker containers"
	switch {
	case errors.Is(err, errNoDocker) && told:
		problem, msg = "gone", "Docker's socket is gone from this machine"
	case errors.Is(err, errNoDocker):
		problem, log, msg = "none", d.Log.Info, "this machine has no Docker"
	case errors.Is(err, errUnreachable):
		problem, msg = "unreachable", "Docker is not answering on this machine"
	case errors.Is(err, errDenied):
		problem, msg = "denied", "Tender may not use Docker on this machine. If Docker was installed after Tender, run: sudo tender install refresh"
	}
	if told {
		problem += " told"
		if problem == "other told" {
			msg = "Docker can't be reached on this machine"
		}
		msg = strings.TrimSuffix(msg, ".") + "; told the hub, which alerts after two minutes"
	}
	if problem == d.problem {
		return
	}
	d.problem = problem
	log(msg, "error", err)
}

// runActions long-polls the hub for actions while Docker is reachable, and runs them in order.
func (d *Daemon) runActions(ctx context.Context) {
	for ctx.Err() == nil {
		if !d.reached.Load() {
			select {
			case <-ctx.Done():
			case <-d.found:
			}
			continue
		}
		var list server.List[Action]
		err := d.poll.Do(ctx, http.MethodGet, d.actionsPath("?wait="+strconv.Itoa(pollWait)), "", nil, &list)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err.Error() != d.pollErr {
				d.Log.Warn("could not ask the hub for service actions", "error", err)
				d.pollErr = err.Error()
			}
			select {
			case <-ctx.Done():
			case <-time.After(d.Every):
			}
			continue
		}
		d.pollErr = ""
		for _, a := range list.Data {
			d.run(ctx, a)
		}
	}
}

// run does one action and reports how it ended. The output, which may be logs, is never logged.
func (d *Daemon) run(ctx context.Context, a Action) {
	d.finish(ctx, a)
	// A report after a start, stop or restart shows the new state at once, even after a failure,
	// which may have changed the container too. The action stays readable when its container is gone.
	if a.Type != TypeLogs {
		select {
		case d.report <- struct{}{}:
		default:
		}
	}
}

// finish does one action and tells the hub how it ended, in its turn.
func (d *Daemon) finish(ctx context.Context, a Action) {
	d.turn.Lock()
	defer d.turn.Unlock()
	actx, cancel := context.WithTimeout(ctx, actionTimeout)
	var output string
	var err error
	switch a.Type {
	case TypeStart, TypeStop, TypeRestart:
		err = d.Docker.Act(actx, a.Type, a.Service)
	case TypeLogs:
		lines := DefaultLines
		if a.Lines != nil {
			lines = *a.Lines
		}
		output, err = d.Docker.Logs(actx, a.Service, lines)
	default:
		err = fmt.Errorf("this daemon doesn't know the action %q; update Tender on this machine", a.Type)
	}
	cancel()
	p := ActionPatch{Status: StatusCompleted}
	if err != nil {
		p.Status, output = StatusFailed, failure(a, err)
	}
	if output != "" || a.Type == TypeLogs {
		output = fitJSON(output)
		p.Output = &output
	}
	d.Log.Info("service action finished", "id", a.ID, "type", a.Type, "service", a.Service, "status", p.Status)

	// The report goes out even when the daemon is stopping. Every retry reuses the key.
	rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer rcancel()
	key := client.NewKey()
	for try := 1; ; try++ {
		err = d.Hub.Do(rctx, http.MethodPatch, d.actionsPath("/"+strconv.FormatInt(a.ID, 10)), key, p, nil)
		if err == nil || !client.Retryable(err) || try == 3 {
			break
		}
		select {
		case <-rctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		d.Log.Error("could not tell the hub how a service action ended", "id", a.ID, "type", a.Type, "error", err)
	}
}

// failure says why an action failed, as one sentence.
func failure(a Action, err error) string {
	verb := a.Type
	if a.Type == TypeLogs {
		verb = "read the logs of"
	}
	msg := fmt.Sprintf("Could not %s %s: %v", verb, a.Service, err)
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
