package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/logs/sinks"
	"github.com/g4f4r0/tender/internal/server"
)

// Variables so tests can wait less.
var (
	sinksEvery  = 30 * time.Second // how often the daemon asks the hub for its sinks
	reportEvery = time.Minute      // how often it reports on them
	reportSoon  = 5 * time.Second  // the soonest it reports after a change
)

// stateKept is how long a sink the hub no longer lists for this machine keeps its buffer and
// cursors, so a sink disabled, or missing for a while, resumes where it was.
const stateKept = 7 * 24 * time.Hour

// startFailure is why a sink's pipeline can't start, and since when.
type startFailure struct {
	err   string
	since time.Time
}

// Daemon sends this machine's logs to the sinks the hub sets for it, straight to each sink. Set
// the exported fields after NewDaemon. Run drives it; nothing else touches it.
type Daemon struct {
	Hub          *client.Client
	Machine      string
	StateDir     string // its state goes in StateDir/logs
	DockerSocket string
	Journalctl   string // default journalctl on PATH
	Log          *slog.Logger

	BufferBytes int64 // each sink's buffer; default BufferBytes

	now       func() time.Time
	pipelines map[int64]*pipeline
	failures  map[int64]startFailure // sinks whose pipeline can't start, reported as failing
	sinks     []DaemonSink
	changed   chan struct{}
}

// NewDaemon returns a daemon that keeps its state under stateDir/logs.
func NewDaemon(hub *client.Client, machine, stateDir, dockerSocket string, log *slog.Logger) *Daemon {
	return &Daemon{Hub: hub, Machine: machine, StateDir: stateDir, DockerSocket: dockerSocket, Journalctl: "journalctl", Log: log,
		BufferBytes: BufferBytes, now: time.Now, pipelines: map[int64]*pipeline{}, failures: map[int64]startFailure{}, changed: make(chan struct{}, 1)}
}

func (d *Daemon) dir() string             { return filepath.Join(d.StateDir, "logs") }
func (d *Daemon) sinksFile() string       { return filepath.Join(d.dir(), "sinks.json") }
func (d *Daemon) sinkDir(id int64) string { return filepath.Join(d.dir(), strconv.FormatInt(id, 10)) }

func (d *Daemon) path(rest string) string {
	return "/v1/machines/" + url.PathEscape(d.Machine) + "/" + rest
}

// Run sends logs until ctx ends. It starts with the sinks it saved, so it goes on sending while
// the hub is down, and asks the hub for changes every 30 seconds.
func (d *Daemon) Run(ctx context.Context) {
	var saved []DaemonSink
	if err := readJSON(d.sinksFile(), &saved); err != nil {
		d.Log.Error("the saved log sinks can't be read; waiting for the hub's", "error", err)
	}
	d.apply(ctx, saved, false)
	d.refresh(ctx)

	fetch := time.NewTicker(sinksEvery)
	defer fetch.Stop()
	report := time.NewTicker(reportEvery)
	defer report.Stop()
	var soon <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			d.shutdown()
			return
		case <-fetch.C:
			d.refresh(ctx)
		case <-report.C:
			d.report(ctx)
		case <-d.changed:
			if soon == nil {
				soon = time.After(reportSoon)
			}
		case <-soon:
			soon = nil
			d.report(ctx)
		}
	}
}

// shutdown stops every pipeline, writing what each holds to its buffer, and reports once more.
func (d *Daemon) shutdown() {
	for _, p := range d.pipelines {
		p.stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d.report(ctx)
}

// refresh asks the hub for this machine's sinks. Any failure, a 404 from a hub before log sinks
// included, keeps the sinks it has: only the hub's list can stop one.
func (d *Daemon) refresh(ctx context.Context) {
	var list server.List[DaemonSink]
	err := d.Hub.Do(ctx, http.MethodGet, d.path("logs/sinks"), "", nil, &list)
	if err == nil && list.Data == nil {
		err = errors.New("the hub sent no list of sinks")
	}
	if err != nil {
		if ctx.Err() == nil {
			d.Log.Warn("could not get the log sinks from the hub; sending to the saved ones", "error", err)
		}
		return
	}
	d.apply(ctx, list.Data, true)
}

// apply starts, restarts and stops pipelines so they match list. With fromHub, list is the hub's
// word: it is saved. A sink it lacks keeps its state for stateKept, so it resumes if it comes
// back, as a disabled sink does when enabled again; after that its state is deleted.
func (d *Daemon) apply(ctx context.Context, list []DaemonSink, fromHub bool) {
	same := func(a, b DaemonSink) bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return bytes.Equal(x, y)
	}
	want := map[int64]DaemonSink{}
	for _, s := range list {
		want[s.ID] = s
	}
	for id, p := range d.pipelines {
		if s, ok := want[id]; ok && same(s, p.sink) {
			continue
		}
		p.stop()
		delete(d.pipelines, id)
		if _, ok := want[id]; !ok {
			d.Log.Info("stopped sending logs to a sink the hub no longer has for this machine", "sink", p.sink.Name)
		}
	}
	for _, s := range list {
		if _, running := d.pipelines[s.ID]; running {
			continue
		}
		p, err := d.newPipeline(s)
		if err == nil {
			err = p.start(ctx)
		}
		if err != nil {
			if f, ok := d.failures[s.ID]; !ok || f.err != err.Error() {
				d.Log.Error("can't send logs to a sink; trying again", "sink", s.Name, "error", err)
				d.failures[s.ID] = startFailure{err: err.Error(), since: d.now().UTC()}
				d.soon()
			}
			continue
		}
		if _, failed := d.failures[s.ID]; failed {
			delete(d.failures, s.ID)
			d.soon()
		}
		_ = os.Remove(filepath.Join(d.sinkDir(s.ID), missingFile))
		d.pipelines[s.ID] = p
		d.Log.Info("sending logs", "sink", s.Name, "type", s.Type)
	}
	for id := range d.failures {
		if _, ok := want[id]; !ok {
			delete(d.failures, id)
		}
	}
	d.sinks = list
	if !fromHub {
		return
	}
	if err := os.MkdirAll(d.dir(), 0o700); err != nil {
		d.Log.Error("can't save the log sinks", "error", err)
		return
	}
	if list == nil {
		list = []DaemonSink{}
	}
	if err := writeJSON(d.sinksFile(), list); err != nil {
		d.Log.Error("can't save the log sinks", "error", err)
	}
	entries, _ := os.ReadDir(d.dir())
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if _, ok := want[id]; err != nil || !e.IsDir() || ok {
			continue
		}
		d.forget(id)
	}
}

// missingFile, in a sink's state folder, holds since when the hub no longer lists the sink.
const missingFile = "missing.json"

// forget deletes the state of a sink the hub no longer lists once it has been missing for
// stateKept, and notes when it went missing until then.
func (d *Daemon) forget(id int64) {
	dir := d.sinkDir(id)
	var since time.Time
	if err := readJSON(filepath.Join(dir, missingFile), &since); err != nil || since.IsZero() {
		since = d.now().UTC()
		if err := writeJSON(filepath.Join(dir, missingFile), since); err != nil {
			d.Log.Error("can't note that a log sink is gone", "error", err)
		}
	}
	if d.now().Sub(since) < stateKept {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		d.Log.Error("can't delete the buffer of a log sink gone for a week", "error", err)
	}
}

func (d *Daemon) newPipeline(s DaemonSink) (*pipeline, error) {
	t, ok := sinks.Get(s.Type)
	if !ok {
		return nil, errors.New("this daemon doesn't know the sink type " + strconv.Quote(s.Type) + "; update it")
	}
	sender, err := t.New(s.Config, d.Machine)
	if err != nil {
		return nil, err
	}
	dir := d.sinkDir(s.ID)
	sp, err := openSpool(filepath.Join(dir, "buffer"), d.BufferBytes)
	if err != nil {
		return nil, err
	}
	log := d.Log.With("sink", s.Name)
	p := &pipeline{sink: s, dir: dir, sender: sender, spool: sp, log: d.Log, now: d.now, changed: d.soon}
	if j := s.Sources.Journal; j != nil {
		p.sources = append(p.sources, journalSource{bin: d.Journalctl, filter: *j, log: log})
	}
	if sv := s.Sources.Services; sv != nil {
		p.sources = append(p.sources, dockerSource{socket: d.DockerSocket, filter: *sv, log: log, poll: dockerPoll})
	}
	return p, nil
}

// soon asks for a report within reportSoon.
func (d *Daemon) soon() {
	select {
	case d.changed <- struct{}{}:
	default:
	}
}

// report tells the hub how sending to each sink goes: a sink whose pipeline can't start is
// failing, with why. A failed report waits for the next one.
func (d *Daemon) report(ctx context.Context) {
	if len(d.pipelines) == 0 && len(d.failures) == 0 {
		return
	}
	in := StatusReport{Data: []StatusInput{}}
	for _, s := range d.sinks {
		if p, ok := d.pipelines[s.ID]; ok {
			in.Data = append(in.Data, p.report())
		} else if f, ok := d.failures[s.ID]; ok {
			var st sinkStatus // the counters it had, if it ever ran
			_ = readJSON(filepath.Join(d.sinkDir(s.ID), "status.json"), &st)
			msg, since := "can't start sending: "+f.err, f.since
			if len(msg) > 1024 {
				msg = msg[:1024]
			}
			in.Data = append(in.Data, StatusInput{SinkID: s.ID, RecordsSent: st.RecordsSent, RecordsDropped: st.RecordsDropped,
				LastSentAt: st.LastSentAt, LastError: &msg, FailingSince: &since, LastDroppedAt: st.LastDroppedAt})
		}
	}
	if err := d.Hub.Do(ctx, http.MethodPut, d.path("logs/status"), "", in, nil); err != nil && ctx.Err() == nil {
		var e *client.Error
		if errors.As(err, &e) && e.Status == http.StatusNotFound {
			return
		}
		d.Log.Warn("could not report on the log sinks", "error", err)
	}
}
