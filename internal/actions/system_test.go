package actions

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/client"
)

// fakeSystem upgrades at once and keeps the update job's result, as the state folder does.
type fakeSystem struct {
	mu       sync.Mutex
	upgrades []string
	updates  []string
	result   map[string][2]string // version: status, output
	cleared  int
	// update runs as the job; nil leaves the result at once.
	update func(ctx context.Context, version string) error
	// upgrade runs as the upgrade; nil ends it at once.
	upgrade func(ctx context.Context) (string, error)
	resumes int
}

func (f *fakeSystem) Upgrade(ctx context.Context, packages string) (string, error) {
	f.mu.Lock()
	f.upgrades = append(f.upgrades, packages)
	run := f.upgrade
	f.mu.Unlock()
	if run != nil {
		return run(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if packages == PackagesAll {
		return "", errors.New("dpkg was interrupted")
	}
	return "3 upgraded", nil
}

func (f *fakeSystem) ResumeUpgrade(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes++
	return "All upgrades installed", nil
}

func (f *fakeSystem) Update(ctx context.Context, version string) error {
	f.mu.Lock()
	f.updates = append(f.updates, version)
	run := f.update
	f.mu.Unlock()
	if run != nil {
		return run(ctx, version)
	}
	f.setResult(version, StatusCompleted, "Tender is now "+version+".")
	return nil
}

func (f *fakeSystem) setResult(version, status, output string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.result == nil {
		f.result = map[string][2]string{}
	}
	f.result[version] = [2]string{status, output}
}

func (f *fakeSystem) UpdateResult(version string) (string, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.result[version]
	return r[0], r[1], ok
}

func (f *fakeSystem) ClearUpdateResult() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result = nil
	f.cleared++
}

func startSystemDaemon(t *testing.T, base, stateDir string, sys System, busy func() (func(), error)) context.CancelFunc {
	t.Helper()
	d := NewDaemon(client.New(base, "token-a"), "a", slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	d.Services, d.StateDir, d.Every, d.System, d.Busy = &fakeRunner{}, stateDir, 50*time.Millisecond, sys, busy
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { d.Run(ctx); close(stopped) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-stopped }) }
	t.Cleanup(stop)
	return stop
}

func TestUpgradeAction(t *testing.T) {
	h := newTestHub(t)
	sys := &fakeSystem{}
	startSystemDaemon(t, h.URL, t.TempDir(), sys, nil)
	ctx := context.Background()
	sec, err := h.store.Create(ctx, 1, Input{Type: TypeUpgrade})
	if err != nil || deref(sec.Packages) != PackagesSecurity {
		t.Fatalf("create = %+v, %v", sec, err)
	}
	got, err := h.store.Await(ctx, 1, sec.ID, 10*time.Second)
	if err != nil || got.Status != StatusCompleted || deref(got.Output) != "3 upgraded" {
		t.Fatalf("security upgrade = %+v, %v", got, err)
	}
	all, _ := h.store.Create(ctx, 1, Input{Type: TypeUpgrade, Packages: ptr(PackagesAll)})
	got, err = h.store.Await(ctx, 1, all.ID, 10*time.Second)
	if err != nil || got.Status != StatusFailed || deref(got.Output) != "Could not upgrade every package: dpkg was interrupted." {
		t.Fatalf("all upgrade = %+v, %v; output %q", got, err, deref(got.Output))
	}
}

// An action waits while Tender updates: the daemon leaves it unclaimed, and the hub hands it out again.
func TestActionWaitsForTheUpdate(t *testing.T) {
	h := newTestHub(t)
	var updating atomic.Bool
	updating.Store(true)
	var held atomic.Int32
	busy := func() (func(), error) {
		if updating.Load() {
			return nil, errors.New("Tender is updating")
		}
		held.Add(1)
		return func() { held.Add(-1) }, nil
	}
	startSystemDaemon(t, h.URL, t.TempDir(), &fakeSystem{}, busy)
	ctx := context.Background()
	a, _ := h.store.Create(ctx, 1, Input{Type: TypeUpgrade})
	time.Sleep(300 * time.Millisecond)
	if got, _ := h.store.Get(ctx, 1, a.ID); got.Status != StatusPending {
		t.Fatalf("the action ran during the update: %+v", got)
	}
	updating.Store(false)
	if _, err := h.store.db.Write.Exec(`UPDATE actions SET handed_out_at = NULL`); err != nil {
		t.Fatal(err)
	}
	got, err := h.store.Await(ctx, 1, a.ID, 10*time.Second)
	if err != nil || got.Status != StatusCompleted || held.Load() != 0 {
		t.Fatalf("after the update = %+v, %v, held %d", got, err, held.Load())
	}
}

func TestUpdateAction(t *testing.T) {
	h := newTestHub(t)
	ctx := context.Background()
	sys := &fakeSystem{}
	startSystemDaemon(t, h.URL, t.TempDir(), sys, nil)
	a, err := h.store.CreateUpdate(ctx, 1, "0.3.1")
	if err != nil || deref(a.Version) != "0.3.1" {
		t.Fatalf("create = %+v, %v", a, err)
	}
	got, err := h.store.Await(ctx, 1, a.ID, 10*time.Second)
	if err != nil || got.Status != StatusCompleted || deref(got.Output) != "Tender is now 0.3.1." {
		t.Fatalf("update = %+v, %v", got, err)
	}
	eventually(t, "the result to be cleared", func() bool { sys.mu.Lock(); defer sys.mu.Unlock(); return sys.cleared == 1 })
}

// The job restarts the daemon: the next daemon sends the job's result from the state folder.
func TestUpdateReportOutlivesTheDaemon(t *testing.T) {
	h := newTestHub(t)
	ctx := context.Background()
	state := t.TempDir()
	started := make(chan struct{})
	sys := &fakeSystem{update: func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done() // the job stops the daemon
		return ctx.Err()
	}}
	stop := startSystemDaemon(t, h.URL, state, sys, nil)
	a, _ := h.store.CreateUpdate(ctx, 1, "0.3.1")
	<-started
	stop()
	if got, _ := h.store.Get(ctx, 1, a.ID); got.Status != StatusInProgress {
		t.Fatalf("during the job = %+v", got)
	}

	next := &fakeSystem{}
	startSystemDaemon(t, h.URL, state, next, nil)
	time.Sleep(200 * time.Millisecond) // no result yet: the report waits
	if got, _ := h.store.Get(ctx, 1, a.ID); got.Status != StatusInProgress {
		t.Fatalf("before the result = %+v", got)
	}
	next.setResult("0.3.1", StatusFailed, "Tender 0.3.1 failed its health check, so it rolled back to Tender 0.3.0.")
	var got Action
	eventually(t, "the job's result", func() bool { got, _ = h.store.Get(ctx, 1, a.ID); return got.Finished() })
	if got.Status != StatusFailed || deref(got.Output) != "Tender 0.3.1 failed its health check, so it rolled back to Tender 0.3.0." {
		t.Errorf("update = %+v, output %q", got, deref(got.Output))
	}
}

// A daemon that restarts while an upgrade runs waits for that upgrade and reports it, without
// starting another.
func TestUpgradeIsPickedUpAfterARestart(t *testing.T) {
	h := newTestHub(t)
	ctx := context.Background()
	state := t.TempDir()
	started := make(chan struct{})
	sys := &fakeSystem{upgrade: func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done() // the daemon stops; the upgrade runs on in its unit
		return "", ctx.Err()
	}}
	stop := startSystemDaemon(t, h.URL, state, sys, nil)
	packages := PackagesSecurity
	a, err := h.store.Create(ctx, 1, Input{Type: TypeUpgrade, Packages: &packages})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	stop()

	next := &fakeSystem{}
	startSystemDaemon(t, h.URL, state, next, nil)
	var got Action
	eventually(t, "the upgrade's report", func() bool { got, _ = h.store.Get(ctx, 1, a.ID); return got.Finished() })
	if got.Status != StatusCompleted || deref(got.Output) != "All upgrades installed" {
		t.Errorf("upgrade = %+v, output %q", got, deref(got.Output))
	}
	next.mu.Lock()
	defer next.mu.Unlock()
	if next.resumes != 1 || len(next.upgrades) != 0 {
		t.Errorf("resumed %d, started %v", next.resumes, next.upgrades)
	}
}

// People can't queue an update; only a rollout does.
func TestPeopleCantQueueAnUpdate(t *testing.T) {
	h := newTestHub(t)
	if _, err := h.store.Create(context.Background(), 1, Input{Type: TypeUpdate}); err == nil {
		t.Error("an update was queued by hand")
	}
}
