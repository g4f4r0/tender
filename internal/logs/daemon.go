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
	sinks     []DaemonSink
	changed   chan struct{}
}

// NewDaemon returns a daemon that keeps its state under stateDir/logs.
func NewDaemon(hub *client.Client, machine, stateDir, dockerSocket string, log *slog.Logger) *Daemon {
	return &Daemon{Hub: hub, Machine: machine, StateDir: stateDir, DockerSocket: dockerSocket, Journalctl: "journalctl", Log: log,
		BufferBytes: BufferBytes, now: time.Now, pipelines: map[int64]*pipeline{}, changed: make(chan struct{}, 1)}
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

// refresh asks the hub for this machine's sinks. A hub that doesn't know the route, from before
// log sinks, has none for it.
func (d *Daemon) refresh(ctx context.Context) {
	var list server.List[DaemonSink]
	err := d.Hub.Do(ctx, http.MethodGet, d.path("logs/sinks"), "", nil, &list)
	var e *client.Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		list, err = server.List[DaemonSink]{}, nil
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
// word: it is saved, and the state of sinks it lacks is deleted.
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
			d.Log.Error("can't send logs to a sink", "sink", s.Name, "error", err)
			continue
		}
		d.pipelines[s.ID] = p
		d.Log.Info("sending logs", "sink", s.Name, "type", s.Type)
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
		if _, ok := want[id]; err == nil && e.IsDir() && !ok {
			if err := os.RemoveAll(filepath.Join(d.dir(), e.Name())); err != nil {
				d.Log.Error("can't delete the buffer of a removed log sink", "error", err)
			}
		}
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

// report tells the hub how sending to each sink goes. A failed report waits for the next one.
func (d *Daemon) report(ctx context.Context) {
	if len(d.pipelines) == 0 {
		return
	}
	in := StatusReport{Data: []StatusInput{}}
	for _, s := range d.sinks {
		if p, ok := d.pipelines[s.ID]; ok {
			in.Data = append(in.Data, p.report())
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
