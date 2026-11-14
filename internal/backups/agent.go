package backups

import (
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

	"github.com/robfig/cron/v3"

	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/server"
)

const (
	tickEvery     = 30 * time.Second
	settingsEvery = 10 * time.Minute
)

// Runner is the restic work the agent needs. *Restic is the real one.
type Runner interface {
	Backup(ctx context.Context, paths, exclude []string) Outcome
	Cleanup(ctx context.Context, retentionDays int) Outcome
	Check(ctx context.Context) Outcome
	Snapshots(ctx context.Context) ([]SnapshotInput, error)
}

// Agent runs this machine's backups on the hub's schedule and reports to the hub.
// Set the exported fields after NewAgent. Run drives one goroutine; nothing else touches the agent.
type Agent struct {
	Hub            *client.Client
	Machine        string
	Paths          []string
	Exclude        []string
	StateDir       string
	CredentialsDir string // where the restic password and S3 keys are
	ResticBin      string // default "restic" on PATH
	Log            *slog.Logger

	newRunner  func(Settings) (Runner, error)
	now        func() time.Time
	queue      pendingQueue
	settings   *Settings
	schedule   cron.Schedule
	settingsAt time.Time // last time the hub was asked for settings
}

// NewAgent returns an agent that keeps its state under stateDir.
func NewAgent(hub *client.Client, machine, stateDir string, log *slog.Logger) *Agent {
	a := &Agent{
		Hub: hub, Machine: machine, StateDir: stateDir, Log: log,
		CredentialsDir: config.CredentialsDir(),
		now:            time.Now,
		queue:          pendingQueue{dir: filepath.Join(stateDir, "queue")},
	}
	a.newRunner = a.newRestic
	return a
}

// agentRun is one history entry, from its start to the hub's acknowledgement. Queued runs are
// stored as this JSON. The keys are fixed when the run starts, so every retry reuses them.
type agentRun struct {
	HistoryID  *int64    `json:"history_id,omitempty"` // set when the hub took the in_progress row
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	ResticID   string    `json:"restic_id,omitempty"`
	SnapshotID *int64    `json:"snapshot_id,omitempty"` // the hub's id for ResticID
	Synced     bool      `json:"synced,omitempty"`      // the snapshot list reached the hub
	CreatedAt  time.Time `json:"created_at"`
	PostKey    string    `json:"post_key"`
	PatchKey   string    `json:"patch_key"`
}

// Run fetches settings, replays queued reports and runs backups when due, until ctx ends.
// A running restic gets SIGINT when ctx ends, and its outcome is still reported.
func (a *Agent) Run(ctx context.Context) {
	a.loadSavedSettings()
	a.tick(ctx)
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.tick(ctx)
		}
	}
}

func (a *Agent) tick(ctx context.Context) {
	if a.now().Sub(a.settingsAt) >= settingsEvery {
		a.refreshSettings(ctx)
	}
	a.replay(ctx)
	if a.settings != nil && ctx.Err() == nil {
		a.runDue(ctx)
	}
}

func (a *Agent) path(rest string) string {
	return "/v1/machines/" + url.PathEscape(a.Machine) + "/" + rest
}

func (a *Agent) settingsFile() string { return filepath.Join(a.StateDir, "settings.json") }

func (a *Agent) loadSavedSettings() {
	b, err := os.ReadFile(a.settingsFile())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var s Settings
	if err == nil {
		err = json.Unmarshal(b, &s)
	}
	if err == nil {
		err = a.apply(s)
	}
	if err != nil {
		a.Log.Error("ignoring the saved settings", "error", err)
	}
}

// apply makes s the current settings if its schedule is valid.
func (a *Agent) apply(s Settings) error {
	if s.Destination.Name == "" {
		return errors.New("settings have no destination")
	}
	sched, err := parseSchedule(s.Schedule)
	if err != nil {
		return err
	}
	a.settings, a.schedule = &s, sched
	return nil
}

// refreshSettings asks the hub for the settings and saves the last good copy. When the hub is
// down or has none, the agent keeps the settings it has.
func (a *Agent) refreshSettings(ctx context.Context) {
	a.settingsAt = a.now()
	var s Settings
	err := a.Hub.Do(ctx, http.MethodGet, a.path("backups/settings"), "", nil, &s)
	var he *client.Error
	switch {
	case err == nil:
	case errors.As(err, &he) && he.Status == http.StatusNotFound:
		a.Log.Warn("the hub has no backup settings for this machine yet", "error", err)
		return
	default:
		a.Log.Warn("could not fetch backup settings, using the last ones", "error", err)
		return
	}
	if err := a.apply(s); err != nil {
		a.Log.Error("the hub sent unusable backup settings", "error", err)
		return
	}
	b, err := json.Marshal(s)
	if err == nil {
		err = writeFileAtomic(a.settingsFile(), b)
	}
	if err != nil {
		a.Log.Error("save the backup settings", "error", err)
	}
}

func (a *Agent) runDue(ctx context.Context) {
	statePath := filepath.Join(a.StateDir, "agent-state.json")
	st, err := loadState(statePath)
	if err != nil {
		a.Log.Error("ignoring the agent state", "error", err)
	}
	if !isDue(a.schedule, st.LastBackup, a.now()) {
		return
	}

	a.Log.Info("backup due", "schedule", a.settings.Schedule)
	o := a.do(ctx, TypeBackup, func(r Runner) Outcome { return r.Backup(ctx, a.Paths, a.Exclude) })
	if ctx.Err() != nil {
		return // stopped mid-run: the next start runs it again
	}
	st.LastBackup = a.now().UTC()
	a.save(statePath, st)
	if o.Status != StatusCompleted {
		return
	}

	a.do(ctx, TypeCleanup, func(r Runner) Outcome { return r.Cleanup(ctx, a.settings.RetentionDays) })
	if ctx.Err() != nil || a.now().Sub(st.LastCheck) < checkEvery {
		return
	}
	if c := a.do(ctx, TypeCheck, func(r Runner) Outcome { return r.Check(ctx) }); c.Status == StatusCompleted && ctx.Err() == nil {
		st.LastCheck = a.now().UTC()
		a.save(statePath, st)
	}
}

func (a *Agent) save(path string, st agentState) {
	if err := saveState(path, st); err != nil {
		a.Log.Error("save the agent state", "error", err)
	}
}

// do runs one restic job between its start and end reports.
func (a *Agent) do(ctx context.Context, typ string, work func(Runner) Outcome) Outcome {
	run := a.begin(ctx, typ)
	var o Outcome
	if r, err := a.newRunner(*a.settings); err != nil {
		o = failed(err)
	} else {
		o = work(r)
	}
	a.Log.Info("run finished", "type", typ, "status", o.Status, "error", o.Error)
	a.finish(ctx, run, o)
	return o
}

// begin tells the hub the run started. If it can't, the run is reported whole when it ends.
func (a *Agent) begin(ctx context.Context, typ string) *agentRun {
	run := &agentRun{
		Type:      typ,
		CreatedAt: a.now().UTC().Truncate(time.Second),
		PostKey:   client.NewKey(),
		PatchKey:  client.NewKey(),
	}
	in := HistoryInput{Type: typ, Status: StatusInProgress, CreatedAt: run.CreatedAt}
	startKey := client.NewKey()
	var h History
	var err error
	for range 2 { // one quick retry with the same key, in case the answer was lost
		if err = a.Hub.Do(ctx, http.MethodPost, a.path("backups/history"), startKey, in, &h); err == nil || !client.Retryable(err) {
			break
		}
	}
	if err != nil {
		a.Log.Warn("could not tell the hub the run started, will report it when done", "type", typ, "error", err)
		return run
	}
	run.HistoryID = &h.ID
	return run
}

// finish reports the end of a run. A report the hub can't take now is queued; one it rejects
// is dropped. The context ignores shutdown so a stopped run still gets reported.
func (a *Agent) finish(ctx context.Context, run *agentRun, o Outcome) {
	run.Status, run.Error, run.ResticID = o.Status, o.Error, o.ResticID
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	err := a.report(ctx, run)
	switch {
	case err == nil:
	case client.Retryable(err):
		a.Log.Warn("hub unreachable, queued the run report", "type", run.Type, "error", err)
		if qerr := a.queue.add(run, a.now()); qerr != nil {
			a.Log.Error("could not queue the run report, it is lost", "type", run.Type, "error", qerr)
		}
	default:
		a.Log.Error("the hub rejected the run report, dropped", "type", run.Type, "error", err)
	}
}

// report sends a finished run: first the snapshot list (after a backup or cleanup), then the run
// itself, as a PATCH of its in_progress row or, if the hub never saw it start, as one POST.
// Live runs and queued runs both come through here.
func (a *Agent) report(ctx context.Context, run *agentRun) error {
	if !run.Synced && (run.Type == TypeBackup || run.Type == TypeCleanup) {
		if err := a.syncSnapshots(ctx, run); err != nil {
			return err
		}
	}
	var errText *string
	if run.Error != "" {
		errText = &run.Error
	}
	if run.HistoryID != nil {
		patch := HistoryPatch{Status: &run.Status, Error: errText, SnapshotID: run.SnapshotID}
		return a.Hub.Do(ctx, http.MethodPatch, a.path("backups/history/"+strconv.FormatInt(*run.HistoryID, 10)), run.PatchKey, patch, nil)
	}
	in := HistoryInput{Type: run.Type, Status: run.Status, Error: errText, SnapshotID: run.SnapshotID, CreatedAt: run.CreatedAt}
	return a.Hub.Do(ctx, http.MethodPost, a.path("backups/history"), run.PostKey, in, nil)
}

// syncSnapshots replaces the hub's snapshot list with restic's and finds the hub id of the
// run's snapshot. A restic failure is logged and skipped; only a hub error is returned.
func (a *Agent) syncSnapshots(ctx context.Context, run *agentRun) error {
	if a.settings == nil {
		return nil
	}
	r, err := a.newRunner(*a.settings)
	var list []SnapshotInput
	if err == nil {
		list, err = r.Snapshots(ctx)
	}
	if err != nil {
		a.Log.Warn("could not list restic snapshots, skipping the sync", "error", err)
		return nil
	}
	var synced server.List[Snapshot]
	if err := a.Hub.Do(ctx, http.MethodPut, a.path("backups/snapshots"), "", list, &synced); err != nil {
		return err
	}
	run.Synced = true
	for _, s := range synced.Data {
		if run.ResticID != "" && s.ResticID == run.ResticID {
			id := s.ID
			run.SnapshotID = &id
		}
	}
	return nil
}

// newRestic builds the real runner for the settings' destination from the credential files.
func (a *Agent) newRestic(s Settings) (Runner, error) {
	d := s.Destination.Name
	var keys [2]string
	for i, name := range []string{d + ".access-key-id", d + ".secret-access-key"} {
		v, err := config.ReadCredential(a.CredentialsDir, name)
		if err != nil {
			return nil, err
		}
		keys[i] = v
	}
	password := d + ".password"
	if _, err := config.ReadCredential(a.CredentialsDir, password); err != nil {
		return nil, err
	}
	cache := os.Getenv("RESTIC_CACHE_DIR")
	if cache == "" {
		cache = filepath.Join(a.StateDir, "cache")
	}
	return &Restic{
		Bin:          a.ResticBin,
		Repository:   s.Destination.Repository(a.Machine),
		PasswordFile: filepath.Join(a.CredentialsDir, password),
		Host:         a.Machine,
		Env:          []string{"AWS_ACCESS_KEY_ID=" + keys[0], "AWS_SECRET_ACCESS_KEY=" + keys[1]},
		CacheDir:     cache,
		StateDir:     a.StateDir,
	}, nil
}
