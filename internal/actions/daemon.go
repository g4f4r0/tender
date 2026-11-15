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

// Daemon long-polls the hub for this machine's actions and runs them one at a time. Set the
// exported fields after NewDaemon. Run drives it; nothing else does.
type Daemon struct {
	Hub      *client.Client
	Machine  string
	Log      *slog.Logger
	Every    time.Duration // the wait before polling again after an error
	Services Runner        // runs the service actions; without it they fail

	poll    *client.Client // Hub, with room for the long poll
	pollErr string         // the last poll error logged, so it is logged once
}

// NewDaemon returns a daemon for machine.
func NewDaemon(hub *client.Client, machine string, log *slog.Logger) *Daemon {
	return &Daemon{Hub: hub, Machine: machine, Log: log, Every: time.Minute, poll: hub.WithTimeout(pollTimeout)}
}

func (d *Daemon) path(rest string) string {
	return "/v1/machines/" + url.PathEscape(d.Machine) + "/actions" + rest
}

// Run asks the hub for actions and runs them, until ctx ends.
func (d *Daemon) Run(ctx context.Context) {
	for ctx.Err() == nil {
		var list server.List[Action]
		err := d.poll.Do(ctx, http.MethodGet, d.path("?wait="+strconv.Itoa(pollWait)), "", nil, &list)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err.Error() != d.pollErr {
				d.Log.Warn("could not ask the hub for actions", "error", err)
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
	service := a.Service != nil
	if service && d.Services != nil {
		d.Services.Lock()
	}
	d.do(ctx, a)
	if service && d.Services != nil {
		d.Services.Unlock()
		if a.Type != TypeLogs {
			d.Services.Changed()
		}
	}
}

func (d *Daemon) do(ctx context.Context, a Action) {
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

	// The report goes out even when the daemon is stopping. Every retry reuses the key.
	rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer rcancel()
	key := client.NewKey()
	for try := 1; ; try++ {
		err = d.Hub.Do(rctx, http.MethodPatch, d.path("/"+strconv.FormatInt(a.ID, 10)), key, p, nil)
		if err == nil || !client.Retryable(err) || try == 3 {
			break
		}
		select {
		case <-rctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		d.Log.Error("could not tell the hub how an action ended", "id", a.ID, "type", a.Type, "error", err)
	}
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
