package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/server"
)

const (
	tickEvery     = 30 * time.Second
	settingsEvery = 10 * time.Minute
)

// Runner is the restic work the daemon needs. *Restic is the real one.
type Runner interface {
	Backup(ctx context.Context, paths, exclude []string) Outcome
	Cleanup(ctx context.Context, retentionDays int) Outcome
	Check(ctx context.Context) Outcome
	Snapshots(ctx context.Context) ([]SnapshotInput, error)
}

// Daemon runs this machine's backups on the hub's schedule and reports to the hub.
// Set the exported fields after NewDaemon. Run drives one goroutine; nothing else touches the daemon.
type Daemon struct {
	Hub       *client.Client
	Machine   string
	Paths     []string
	Exclude   []string
	StateDir  string
	ResticBin string // default "restic" on PATH
	Log       *slog.Logger

	newRunner  func(Settings) (Runner, error)
	now        func() time.Time
	queue      pendingQueue
	settings   *Settings
	schedule   cron.Schedule
	settingsAt time.Time // last time the hub was asked for settings
	waiting    bool      // a backup is due but the hub has sent no credentials yet
	unusable   string    // the last unusable settings the hub sent, logged once
}

// NewDaemon returns a daemon that keeps its state under stateDir.
func NewDaemon(hub *client.Client, machine, stateDir string, log *slog.Logger) *Daemon {
	a := &Daemon{
		Hub: hub, Machine: machine, StateDir: stateDir, Log: log,
		now:   time.Now,
		queue: pendingQueue{dir: filepath.Join(stateDir, "queue")},
	}
	a.newRunner = a.newRestic
	return a
}

// daemonRun is one history entry, from its start to the hub's acknowledgement. Queued runs are
// stored as this JSON. The keys are fixed when the run starts, so every retry reuses them.
type daemonRun struct {
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
func (a *Daemon) Run(ctx context.Context) {
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

// tick asks the hub for settings every settingsEvery, or on every tick while the daemon has
// none, so a machine starts backing up within a tick of getting them.
func (a *Daemon) tick(ctx context.Context) {
	if a.settings == nil || a.now().Sub(a.settingsAt) >= settingsEvery {
		a.refreshSettings(ctx)
	}
	a.replay(ctx)
	if a.settings != nil && ctx.Err() == nil {
		a.runDue(ctx)
	}
}

func (a *Daemon) path(rest string) string {
	return "/v1/machines/" + url.PathEscape(a.Machine) + "/" + rest
}

func (a *Daemon) settingsFile() string { return filepath.Join(a.StateDir, "settings.json") }

func (a *Daemon) passwordFile() string { return filepath.Join(a.StateDir, "repository-password") }

// stopSettings stops backups until the hub sends settings again. With deleteSaved, it also
// deletes the saved settings, which hold the keys, and the repository password. It logs why once,
// when there was something to stop or delete, and quietly after that.
func (a *Daemon) stopSettings(why string, err error, deleteSaved bool) {
	had := a.settings != nil
	a.settings, a.schedule, a.waiting = nil, nil, false
	if deleteSaved {
		for _, p := range []string{a.settingsFile(), a.passwordFile()} {
			if rerr := os.Remove(p); rerr == nil {
				had = true
			} else if !errors.Is(rerr, os.ErrNotExist) {
				a.Log.Error("delete the saved backup settings", "error", rerr)
			}
		}
	}
	switch {
	case had && deleteSaved:
		a.Log.Warn(why+"; stopped backing up and deleted the saved settings", "error", err)
	case had:
		a.Log.Error(why+"; stopped backing up", "error", err)
	default:
		a.Log.Debug(why, "error", err)
	}
}

func (a *Daemon) loadSavedSettings() {
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
func (a *Daemon) apply(s Settings) error {
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

// refreshSettings asks the hub for the settings and saves the last good copy, which holds the
// destination's credentials, at 0600. Settings without credentials are used but not saved over
// a good copy. When the hub can't be reached, or answers another error, the daemon keeps the
// settings it has.
//
// When the hub says the machine has no backup settings, the daemon stops backing up and deletes
// its saved settings and password, so the keys don't stay behind. When the hub no longer takes
// the token (401: the machine was removed, its token replaced, or the hub reinstalled), the daemon
// stops backing up but keeps its files: they may hold the only copy of the repository password.
func (a *Daemon) refreshSettings(ctx context.Context) {
	a.settingsAt = a.now()
	var s Settings
	err := a.Hub.Do(ctx, http.MethodGet, a.path("backups/settings"), "", nil, &s)
	var he *client.Error
	switch {
	case err == nil:
	case errors.As(err, &he) && he.Status == http.StatusNotFound && he.Type == NoSettingsType:
		a.stopSettings("the hub has no backup settings for this machine", err, true)
		return
	case errors.As(err, &he) && he.Status == http.StatusUnauthorized:
		a.stopSettings("the hub no longer accepts this machine's token; install the daemon again with a new one", err, false)
		return
	case a.settings == nil: // asked again every tick; the usage reports already warn when the hub is down
		a.Log.Debug("could not fetch backup settings", "error", err)
		return
	default:
		a.Log.Warn("could not fetch backup settings, using the last ones", "error", err)
		return
	}
	if err := a.apply(s); err != nil {
		if err.Error() != a.unusable { // asked again every tick while the daemon has no settings
			a.Log.Error("the hub sent unusable backup settings", "error", err)
		}
		a.unusable = err.Error()
		return
	}
	a.unusable = ""
	if s.Credentials == nil {
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

func (a *Daemon) runDue(ctx context.Context) {
	statePath := filepath.Join(a.StateDir, "state.json")
	st, err := loadState(statePath)
	if err != nil {
		a.Log.Error("ignoring the daemon state", "error", err)
	}
	if !isDue(a.schedule, st.LastBackup, a.now()) {
		return
	}
	// Settings without credentials mean the hub can't hand the keys over yet, not that a backup
	// failed: wait for them, and back up as soon as they're there.
	if _, err := a.newRunner(*a.settings); errors.Is(err, errNoCredentials) {
		if !a.waiting {
			a.Log.Warn("backup due, waiting for the hub to send the destination's credentials", "error", err)
			a.waiting = true
		}
		return
	}
	a.waiting = false

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

func (a *Daemon) save(path string, st daemonState) {
	if err := saveState(path, st); err != nil {
		a.Log.Error("save the daemon state", "error", err)
	}
}

// do runs one restic job between its start and end reports.
func (a *Daemon) do(ctx context.Context, typ string, work func(Runner) Outcome) Outcome {
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
func (a *Daemon) begin(ctx context.Context, typ string) *daemonRun {
	run := &daemonRun{
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
func (a *Daemon) finish(ctx context.Context, run *daemonRun, o Outcome) {
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
func (a *Daemon) report(ctx context.Context, run *daemonRun) error {
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
func (a *Daemon) syncSnapshots(ctx context.Context, run *daemonRun) error {
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

// errNoCredentials means the hub's settings carry no destination credentials.
var errNoCredentials = errors.New("the hub sent no credentials for this destination")

// newRestic builds the real runner for the settings' destination. The repository password goes
// to restic through a 0600 file in the state folder; the S3 keys go in the child's environment
// only. Neither ever appears on a command line.
func (a *Daemon) newRestic(s Settings) (Runner, error) {
	c := s.Credentials
	if c == nil || c.AccessKeyID == "" || c.SecretAccessKey == "" || c.Password == "" {
		return nil, errNoCredentials
	}
	password := a.passwordFile()
	if err := writeFileAtomic(password, []byte(c.Password)); err != nil {
		return nil, fmt.Errorf("save the repository password: %w", err)
	}
	cache := os.Getenv("RESTIC_CACHE_DIR")
	if cache == "" {
		cache = filepath.Join(a.StateDir, "cache")
	}
	return &Restic{
		Bin:          a.ResticBin,
		Repository:   s.Destination.Repository(a.Machine),
		PasswordFile: password,
		Host:         a.Machine,
		Env:          []string{"AWS_ACCESS_KEY_ID=" + c.AccessKeyID, "AWS_SECRET_ACCESS_KEY=" + c.SecretAccessKey},
		CacheDir:     cache,
		StateDir:     a.StateDir,
	}, nil
}
