package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

var t0 = time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

type action struct {
	machine int64
	version string
	status  string
	output  string
	changed time.Time
}

type fakeActions struct{ list []*action }

func (f *fakeActions) CreateUpdate(_ context.Context, machineID int64, version string) (int64, error) {
	f.list = append(f.list, &action{machine: machineID, version: version, status: StatusPending, changed: t0})
	return int64(len(f.list)), nil
}

func (f *fakeActions) Status(_ context.Context, machineID, id int64) (string, string, time.Time, error) {
	if id < 1 || int(id) > len(f.list) || f.list[id-1].machine != machineID {
		return "", "", time.Time{}, errors.New("no such action")
	}
	a := f.list[id-1]
	return a.status, a.output, a.changed, nil
}

type fakeReleases struct {
	latest string
	auto   bool
	err    error
}

func (f *fakeReleases) Latest(context.Context) (string, bool, error) { return f.latest, f.auto, f.err }

func (f *fakeReleases) Official(_ context.Context, version string) error {
	if strings.Contains(version, "-") {
		return fmt.Errorf("Tender v%s is a prerelease, and Tender only updates to stable releases", version)
	}
	return nil
}

type rig struct {
	t        *testing.T
	store    *Store
	machines []Machine
	actions  *fakeActions
	releases *fakeReleases
	srv      *httptest.Server
}

// newRig has three machines that run 0.3.0 and report: hub (the hub's own), a and b, and a
// fourth, old, that runs v0.2.2 and reports no version.
func newRig(t *testing.T) *rig {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	_, err = d.Write.Exec(`CREATE TABLE machines (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, token_hash BLOB NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) STRICT;
		INSERT INTO machines VALUES (1, 'hub', x'00', 0, 0), (2, 'a', x'00', 0, 0), (3, 'b', x'00', 0, 0), (4, 'old', x'00', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background(), Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, actions: &fakeActions{}, releases: &fakeReleases{latest: "0.3.1", auto: true}}
	seen := ptr(t0)
	r.machines = []Machine{
		{ID: 1, Name: "hub", Version: ptr("0.3.0"), Updates: ModeInherit, LastSeen: seen},
		{ID: 2, Name: "a", Version: ptr("0.3.0"), Updates: ModeInherit, LastSeen: seen},
		{ID: 3, Name: "b", Version: ptr("0.3.0"), Updates: ModeInherit, LastSeen: seen},
		{ID: 4, Name: "old", Updates: ModeInherit, LastSeen: seen},
	}
	r.store = NewStore(d, func(context.Context) ([]Machine, error) { return slices.Clone(r.machines), nil }, r.actions, r.releases)
	r.store.HubMachine = "hub"
	srv := server.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	r.store.Routes(srv)
	r.srv = httptest.NewServer(srv)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) machine(name string) *Machine {
	i := slices.IndexFunc(r.machines, func(m Machine) bool { return m.Name == name })
	return &r.machines[i]
}

func (r *rig) check(now time.Time) {
	r.t.Helper()
	v, auto, err := r.releases.Latest(context.Background())
	if err := r.store.setLatest(context.Background(), v, auto, err, now); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) step(now time.Time) {
	r.t.Helper()
	if err := r.store.Step(context.Background(), now); err != nil {
		r.t.Fatal(err)
	}
}

// steps is "machine:status" of each step of rollout id.
func (r *rig) steps(id int64) []string {
	r.t.Helper()
	ro, err := r.store.Rollout(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	out := []string{ro.Status}
	for _, s := range ro.Machines {
		out = append(out, s.Machine+":"+s.Status)
	}
	return out
}

func (r *rig) want(id int64, want ...string) {
	r.t.Helper()
	if got := r.steps(id); !slices.Equal(got, want) {
		r.t.Errorf("rollout %d = %q, want %q", id, got, want)
	}
}

// complete has the last queued action complete and its machine report version.
func (r *rig) complete(version string, at time.Time) {
	a := r.actions.list[len(r.actions.list)-1]
	a.status, a.changed = StatusCompleted, at
	for i := range r.machines {
		if r.machines[i].ID == a.machine {
			r.machines[i].Version = &version
		}
	}
}

func (r *rig) call(method, path string, body any) (int, string) {
	r.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			r.t.Fatal(err)
		}
	}
	req, _ := http.NewRequest(method, r.srv.URL+path, &buf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestAutoRolloutOneMachineAtATimeHubFirst(t *testing.T) {
	r := newRig(t)
	r.step(t0) // nothing known of GitHub yet
	if len(r.actions.list) != 0 {
		t.Fatal("a rollout started before the hub knew of a release")
	}
	r.check(t0)
	r.step(t0)
	r.want(1, "in_progress", "hub:in_progress", "a:pending", "b:pending")
	if len(r.actions.list) != 1 || r.actions.list[0].machine != 1 || r.actions.list[0].version != "0.3.1" {
		t.Fatalf("actions = %+v", r.actions.list[0])
	}
	r.step(t0.Add(time.Minute)) // a's update hasn't ended
	if len(r.actions.list) != 1 {
		t.Fatal("the next machine started before the first ended")
	}
	r.complete("0.3.1", t0.Add(2*time.Minute))
	r.step(t0.Add(2 * time.Minute))
	r.step(t0.Add(2 * time.Minute))
	r.want(1, "in_progress", "hub:completed", "a:in_progress", "b:pending")
	r.complete("0.3.1", t0.Add(3*time.Minute))
	r.step(t0.Add(3 * time.Minute))
	r.step(t0.Add(3 * time.Minute))
	r.complete("0.3.1", t0.Add(4*time.Minute))
	r.step(t0.Add(4 * time.Minute))
	r.step(t0.Add(4 * time.Minute))
	r.want(1, "completed", "hub:completed", "a:completed", "b:completed")
	if got := []int64{r.actions.list[0].machine, r.actions.list[1].machine, r.actions.list[2].machine}; !slices.Equal(got, []int64{1, 2, 3}) {
		t.Errorf("update order = %v, want the hub, then a and b", got)
	}
	r.step(t0.Add(5 * time.Minute)) // every machine is on 0.3.1: no new rollout
	if rs, _ := r.store.Rollouts(context.Background()); len(rs) != 1 {
		t.Errorf("rollouts = %d", len(rs))
	}
}

func TestAutoModeLeavesOut(t *testing.T) {
	r := newRig(t)
	r.machine("a").Updates = ModeManual
	r.machine("b").LastSeen = ptr(t0.Add(-time.Hour)) // silent
	r.check(t0)
	r.step(t0)
	r.want(1, "in_progress", "hub:in_progress")

	// Manual mode for the hub leaves out every machine that inherits it; auto on a machine still updates it.
	r = newRig(t)
	if _, err := r.store.SetSettings(context.Background(), SettingsInput{Mode: ModeManual}); err != nil {
		t.Fatal(err)
	}
	r.machine("b").Updates = ModeAuto
	r.check(t0)
	r.step(t0)
	r.want(1, "in_progress", "b:in_progress")

	// A release whose tender-release.json says no, or outside the window, starts nothing.
	r = newRig(t)
	r.releases.auto = false
	r.check(t0)
	r.step(t0)
	r.releases.auto = true
	r.check(t0)
	if _, err := r.store.SetSettings(context.Background(), SettingsInput{Mode: ModeAuto, Window: ptr("04:00-05:00")}); err != nil {
		t.Fatal(err)
	}
	r.step(t0)
	if len(r.actions.list) != 0 {
		t.Fatalf("auto mode acted: %+v", r.actions.list)
	}
	for i := range r.machines {
		r.machines[i].LastSeen = ptr(t0.Add(time.Hour))
	}
	r.step(t0.Add(time.Hour + time.Minute))
	r.want(1, "in_progress", "hub:in_progress", "a:pending", "b:pending")
}

func TestFailedUpdateStopsTheRollout(t *testing.T) {
	r := newRig(t)
	r.check(t0)
	r.step(t0)
	a := r.actions.list[0]
	a.status, a.output = StatusFailed, "Tender 0.3.1 failed its health check, so it rolled back to Tender 0.3.0: tenderd is not active."
	r.step(t0.Add(time.Minute))
	r.want(1, "failed", "hub:failed", "a:pending", "b:pending")
	ro, _ := r.store.Rollout(context.Background(), 1)
	if ro.Error == nil || !strings.Contains(*ro.Error, "stopped at hub") || !strings.Contains(*ro.Machines[0].Error, "rolled back") {
		t.Errorf("rollout = %+v", ro)
	}
	f, err := r.store.Failures(context.Background())
	if err != nil || len(f) != 1 || f[1].Version != "0.3.1" || !strings.Contains(f[1].Reason, "health check") {
		t.Errorf("failures = %+v %v", f, err)
	}
	r.step(t0.Add(2 * time.Minute)) // auto mode doesn't try 0.3.1 again
	if rs, _ := r.store.Rollouts(context.Background()); len(rs) != 1 {
		t.Errorf("auto mode retried a failed release: %d rollouts", len(rs))
	}

	// A person can try again; a success clears the failure.
	if _, err := r.store.Start(context.Background(), RolloutInput{Machines: []string{"hub"}}); err != nil {
		t.Fatal(err)
	}
	r.step(t0.Add(3 * time.Minute))
	r.complete("0.3.1", t0.Add(4*time.Minute))
	r.step(t0.Add(4 * time.Minute))
	if f, _ := r.store.Failures(context.Background()); len(f) != 0 {
		t.Errorf("failures after a success = %+v", f)
	}
}

func TestStepTimeouts(t *testing.T) {
	r := newRig(t)
	r.check(t0)
	r.step(t0)
	r.actions.list[0].status, r.actions.list[0].changed = StatusCompleted, t0.Add(time.Minute) // completed, but the hub still reports 0.3.0
	r.step(t0.Add(3 * time.Minute))
	r.want(1, "in_progress", "hub:in_progress", "a:pending", "b:pending")
	r.step(t0.Add(7 * time.Minute))
	r.want(1, "failed", "hub:failed", "a:pending", "b:pending")

	r = newRig(t)
	r.check(t0)
	r.step(t0)
	r.step(t0.Add(49 * time.Minute))
	r.want(1, "in_progress", "hub:in_progress", "a:pending", "b:pending")
	r.step(t0.Add(51 * time.Minute))
	if ro, _ := r.store.Rollout(context.Background(), 1); ro.Status != StatusFailed || !strings.Contains(*ro.Machines[0].Error, "online") {
		t.Errorf("rollout = %+v", ro)
	}
}

func TestStartRefuses(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		in     RolloutInput
		status int
		text   string
	}{
		"prerelease": {RolloutInput{Version: "0.3.2-rc.1"}, 422, "prerelease"},
		"not one":    {RolloutInput{Version: "latest"}, 422, "such as 0.3.1"},
		"downgrade":  {RolloutInput{Version: "0.2.9", Machines: []string{"a"}}, 422, "never downgrades"},
		"old":        {RolloutInput{Machines: []string{"old"}}, 422, "before v0.3.0"},
		"missing":    {RolloutInput{Machines: []string{"nope"}}, 404, "doesn't exist"},
	} {
		_, err := r.store.Start(ctx, c.in)
		var e *server.Error
		if !errors.As(err, &e) || e.Status != c.status || !strings.Contains(e.Detail, c.text) {
			t.Errorf("%s: %v", name, err)
		}
	}
	r.releases.err = errors.New("GitHub is down")
	if _, err := r.store.Start(ctx, RolloutInput{}); err == nil || !strings.Contains(err.Error(), "GitHub is down") {
		t.Errorf("no latest: %v", err)
	}
	r.releases.err = nil
	ro, err := r.store.Start(ctx, RolloutInput{Version: "v0.3.1"})
	if err != nil || ro.Version != "0.3.1" || len(ro.Machines) != 3 || ro.Machines[0].Machine != "hub" || ro.Auto {
		t.Fatalf("every machine: %+v %v", ro, err)
	}
	var e *server.Error
	if _, err := r.store.Start(ctx, RolloutInput{}); !errors.As(err, &e) || e.Status != 409 {
		t.Errorf("a second rollout: %v", err)
	}
}

func TestSettingsAndWindow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	st, err := r.store.Settings(ctx)
	if err != nil || st.Mode != ModeAuto || st.Window != nil {
		t.Fatalf("defaults = %+v %v", st, err)
	}
	for _, w := range []string{"2-5", "02:00-02:00", "24:00-01:00", "02:00"} {
		if _, err := r.store.SetSettings(ctx, SettingsInput{Mode: ModeAuto, Window: &w}); err == nil {
			t.Errorf("window %q was taken", w)
		}
	}
	if _, err := r.store.SetSettings(ctx, SettingsInput{Mode: "sometimes"}); err == nil {
		t.Error("mode sometimes was taken")
	}
	before, _ := r.store.Settings(ctx)
	st, err = r.store.SetSettings(ctx, SettingsInput{Mode: ModeManual, Window: ptr("22:00-02:00")})
	if err != nil || st.Mode != ModeManual || *st.Window != "22:00-02:00" {
		t.Fatalf("set = %+v %v", st, err)
	}
	if !st.UpdatedAt.After(before.UpdatedAt) {
		t.Error("a change didn't move updated_at, which tells the engine to look at GitHub")
	}
	at := func(h, m int) time.Time { return time.Date(2026, 11, 15, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		window string
		at     time.Time
		in     bool
	}{
		{"22:00-02:00", at(23, 0), true}, {"22:00-02:00", at(1, 59), true}, {"22:00-02:00", at(2, 0), false},
		{"02:00-05:00", at(2, 0), true}, {"02:00-05:00", at(5, 0), false}, {"02:00-05:00", at(12, 0), false},
	} {
		if got := inWindow(&c.window, c.at); got != c.in {
			t.Errorf("inWindow(%s, %s) = %v", c.window, c.at.Format("15:04"), got)
		}
	}
	if !inWindow(nil, at(12, 0)) {
		t.Error("no window is any time")
	}
}

func TestRoutes(t *testing.T) {
	r := newRig(t)
	r.check(t0)
	status, body := r.call("GET", "/v1/updates", nil)
	var o Overview
	if err := json.Unmarshal([]byte(body), &o); status != 200 || err != nil || o.Mode != ModeAuto || *o.Latest.Version != "0.3.1" ||
		len(o.Data) != 4 || !o.Data[0].UpdateAvailable || o.Data[3].UpdateAvailable || o.Rollout != nil {
		t.Fatalf("GET /v1/updates: %d %s", status, body)
	}
	if status, body := r.call("PUT", "/v1/updates/settings", map[string]any{"mode": "manual", "window": "02:00-05:00"}); status != 200 ||
		!strings.Contains(body, `"window":"02:00-05:00"`) {
		t.Errorf("PUT settings: %d %s", status, body)
	}
	if status, body := r.call("GET", "/v1/updates/settings", nil); status != 200 || !strings.Contains(body, `"mode":"manual"`) {
		t.Errorf("GET settings: %d %s", status, body)
	}
	if status, body := r.call("PUT", "/v1/updates/settings", map[string]any{"mode": "never"}); status != 422 {
		t.Errorf("bad mode: %d %s", status, body)
	}
	status, body = r.call("POST", "/v1/updates/rollouts", map[string]any{"machines": []string{"hub", "b"}})
	if status != 202 || !strings.Contains(body, `"machines":[{"machine":"hub"`) {
		t.Fatalf("POST rollouts: %d %s", status, body)
	}
	if status, body := r.call("GET", "/v1/updates/rollouts/1", nil); status != 200 || !strings.Contains(body, `"version":"0.3.1"`) {
		t.Errorf("GET rollout: %d %s", status, body)
	}
	if status, _ := r.call("GET", "/v1/updates/rollouts/9", nil); status != 404 {
		t.Errorf("missing rollout: %d", status)
	}
	if status, body := r.call("GET", "/v1/updates/rollouts", nil); status != 200 || !strings.HasPrefix(body, `{"data":[{"id":1`) {
		t.Errorf("GET rollouts: %d %s", status, body)
	}
	if status, body := r.call("GET", "/v1/updates", nil); !strings.Contains(body, `"rollout":{"id":1`) {
		t.Errorf("overview during a rollout: %d %s", status, body)
	}
}

// A machine on a development build may be newer than the release: the hub never updates it.
func TestDevBuildsStayOut(t *testing.T) {
	r := newRig(t)
	r.machine("a").Version = ptr("0.3.0-4-gabc123")
	r.check(t0)
	_, err := r.store.Start(context.Background(), RolloutInput{Machines: []string{"a"}})
	if err == nil || !strings.Contains(err.Error(), "development build") {
		t.Errorf("start a development build: %v", err)
	}
	o, err := r.store.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range o.Data {
		if m.Machine == "a" && m.UpdateAvailable {
			t.Error("a development build shows an update available")
		}
	}
	r.step(t0)
	r.want(1, "in_progress", "hub:in_progress", "b:pending")
}
