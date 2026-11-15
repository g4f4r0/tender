package services

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/g4f4r0/tender/internal/client"
)

const reportEvery = time.Minute

// listTimeout bounds listing and inspecting every container for one report.
var listTimeout = 30 * time.Second

// The usage of a report: statsAtOnce reads at a time, each within statsTimeout, all within
// statsBudget. A read that fails or runs out of time marks that container's stats unavailable.
var (
	statsAtOnce  = 4
	statsTimeout = 5 * time.Second
	statsBudget  = 20 * time.Second
)

// Daemon reports this machine's Docker containers to the hub, and does the actions the actions
// domain's daemon hands it: it is that daemon's Runner. Set the exported fields after NewDaemon.
// Run drives the reports; nothing else does.
type Daemon struct {
	Hub     *client.Client
	Machine string
	Docker  *Docker
	Log     *slog.Logger
	Every   time.Duration // between reports, a minute
	// StateDir is the daemon's state folder, where it remembers it saw Docker. Empty remembers
	// nothing past a restart.
	StateDir string

	// turn lets one report (list and PUT) or one action (run and PATCH, under Lock) go at a time,
	// so a report can't carry a state read before an action and reach the hub after its result.
	turn    sync.Mutex
	report  chan struct{}     // asks for a report now, after an action changed a container
	seen    bool              // Docker answered on this machine, so losing it is an outage; see sawDocker
	problem string            // the last Docker problem logged, so it is logged once
	hubErr  string            // the last hub error logged, so it is logged once
	samples map[string]sample // each running container's last usage reading, by sampleKey
}

// NewDaemon returns a daemon that reaches Docker at socket.
func NewDaemon(hub *client.Client, machine, socket string, log *slog.Logger) *Daemon {
	return &Daemon{Hub: hub, Machine: machine, Docker: NewDocker(socket), Log: log, Every: reportEvery, report: make(chan struct{}, 1),
		samples: map[string]sample{}}
}

func (d *Daemon) path(rest string) string {
	return "/v1/machines/" + url.PathEscape(d.Machine) + "/services" + rest
}

// Run reports the containers now and every minute, until ctx ends. A machine that never had
// Docker reports nothing; it checks for Docker each minute.
func (d *Daemon) Run(ctx context.Context) {
	d.reportNow(ctx)
	t := time.NewTicker(d.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.report:
		}
		d.reportNow(ctx)
	}
}

// Lock and Unlock make an action and its result take turns with the reports.
func (d *Daemon) Lock()   { d.turn.Lock() }
func (d *Daemon) Unlock() { d.turn.Unlock() }

// Changed asks for a report now: an action changed a container.
func (d *Daemon) Changed() {
	select {
	case d.report <- struct{}{}:
	default:
	}
}

// Service starts, stops or restarts a container, or returns its newest lines of logs since then,
// at most MaxLines and MaxOutputBytes.
func (d *Daemon) Service(ctx context.Context, typ, service string, lines int, since time.Duration) (string, error) {
	switch typ {
	case actStart, actStop, actRestart:
		return "", d.Docker.Act(ctx, typ, service)
	case actLogs:
		return d.Docker.Logs(ctx, service, lines, since)
	}
	return "", errors.New("this daemon doesn't know the action " + typ + "; update Tender on this machine")
}

// reportNow sends the hub the full container list. It first pings Docker: when its socket is
// there but doesn't answer, or Docker answered on this machine before (see sawDocker) and can't be reached now, it tells
// the hub Docker is unreachable. A machine that never had Docker sends nothing. A list that fails
// after Docker answered is logged, and the hub keeps the last one.
func (d *Daemon) reportNow(ctx context.Context) {
	d.turn.Lock()
	defer d.turn.Unlock()
	if err := d.Docker.Ping(ctx); err != nil {
		if ctx.Err() == nil {
			d.unreachable(ctx, err, errors.Is(err, errUnreachable) || d.sawDocker())
		}
		return
	}
	d.rememberDocker()
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
	d.usage(ctx, list)
	if d.problem != "" && d.problem != "other" { // it was a problem reaching Docker
		d.Log.Info("Docker is reachable", "containers", len(list))
	}
	d.problem = ""
	d.send(ctx, RunningReport(list))
}

// usage reads the memory and CPU of each running container in list, statsAtOnce at a time. CPU is
// measured against this daemon's previous reading of the same container, so it is null on a
// container's first report and after it restarts or is made again.
func (d *Daemon) usage(ctx context.Context, list []ServiceInput) {
	bctx, cancel := context.WithTimeout(ctx, statsBudget)
	defer cancel()
	now := make([]*sample, len(list))
	slots := make(chan struct{}, statsAtOnce)
	var wg sync.WaitGroup
	for i, sv := range list {
		if sv.State != StateRunning {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-bctx.Done():
				return
			}
			defer func() { <-slots }()
			sctx, scancel := context.WithTimeout(bctx, statsTimeout)
			defer scancel()
			if st, err := d.Docker.Stats(sctx, sv.ContainerID); err == nil {
				now[i] = &st
			}
		}()
	}
	wg.Wait()
	next := map[string]sample{}
	for i := range list {
		st := now[i]
		if st == nil {
			list[i].MemoryLimitBytes = nil // no usage without a reading
			if list[i].State == StateRunning {
				list[i].StatsUnavailable = true // unknown this time, so the hub keeps the last numbers
				if prev, ok := d.samples[sampleKey(list[i])]; ok {
					next[sampleKey(list[i])] = prev // the next reading measures CPU from the last good one
				}
			}
			continue
		}
		mem := st.memory
		list[i].MemoryBytes = &mem
		if prev, ok := d.samples[sampleKey(list[i])]; ok {
			if p, ok := cpuPercent(prev, *st); ok {
				list[i].CPUPercent = &p
			}
		}
		next[sampleKey(list[i])] = *st
	}
	d.samples = next
}

// sampleKey names one run of a container. A re-created container has a new id, and a restarted
// one a new start time and a CPU counter that starts again, so neither is measured against the
// reading before it.
func sampleKey(sv ServiceInput) string {
	if sv.StartedAt == nil {
		return sv.ContainerID
	}
	return sv.ContainerID + "@" + sv.StartedAt.Format(time.RFC3339Nano)
}

// seenFile marks, in the state folder, that this machine's daemon has seen Docker answer.
const seenFile = "docker-seen"

// sawDocker says this daemon, or one before it on this machine, has seen Docker answer.
func (d *Daemon) sawDocker() bool {
	if !d.seen && d.StateDir != "" {
		_, err := os.Stat(filepath.Join(d.StateDir, seenFile))
		d.seen = err == nil
	}
	return d.seen
}

// rememberDocker notes that Docker answered, in memory and once in the state folder, so a
// restart doesn't forget it.
func (d *Daemon) rememberDocker() {
	if d.sawDocker() {
		return
	}
	d.seen = true
	if d.StateDir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(d.StateDir, seenFile), nil, 0o600); err != nil {
		d.Log.Warn("could not note that this machine has Docker", "error", err)
	}
}

// unreachable logs once why Docker can't be reached and, when tell, tells the hub.
func (d *Daemon) unreachable(ctx context.Context, err error, tell bool) {
	d.dockerProblem(err, tell)
	if tell {
		d.send(ctx, UnreachableReport(unreachableReason(err)))
	}
}

// unreachableReason says why Docker can't be reached, in the hub's words.
func unreachableReason(err error) string {
	switch {
	case errors.Is(err, errNoDocker):
		return ReasonSocketMissing
	case errors.Is(err, errDenied):
		return ReasonPermissionDenied
	case errors.Is(err, syscall.ECONNREFUSED):
		return ReasonRefused
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return ReasonTimeout
	}
	return ReasonError
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
