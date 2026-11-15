package services

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

func ptr[T any](v T) *T { return &v }

// newDB has machines a (id 1) and b (id 2) and this domain's tables; services can't import machines.
func newDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = d.Write.Exec(`CREATE TABLE machines (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, token_hash BLOB NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) STRICT;
		INSERT INTO machines VALUES (1, 'a', x'00', 0, 0), (2, 'b', x'00', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background(), Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	return d
}

func status(err error) int {
	var e *server.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func running(name, id string) ServiceInput {
	return ServiceInput{Name: name, ContainerID: id, Image: "img", State: StateRunning}
}

func TestReplace(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	started := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	web := running("web", "c1")
	web.Project, web.StartedAt, web.Health = ptr("shop"), &started, ptr(HealthHealthy)
	list, err := s.Replace(ctx, 1, []ServiceInput{web, running("worker", "c2")})
	if err != nil || len(list) != 2 {
		t.Fatalf("first report: %+v, %v", list, err)
	}
	w := list[0]
	if w.Machine != "a" || w.Name != "web" || *w.Project != "shop" || !w.StartedAt.Equal(started) || !w.AlertsEnabled || w.StoppedByTender {
		t.Errorf("web = %+v", w)
	}
	if _, err := s.Replace(ctx, 2, []ServiceInput{running("web", "c9")}); err != nil {
		t.Fatal(err)
	}

	// Alerts off follow the name across a new container; worker is gone.
	if _, err := s.SetAlerts(ctx, 1, "web", false); err != nil {
		t.Fatal(err)
	}
	list, err = s.Replace(ctx, 1, []ServiceInput{running("web", "c3")})
	if err != nil || len(list) != 1 || list[0].ContainerID != "c3" || list[0].AlertsEnabled || list[0].ID != w.ID {
		t.Fatalf("second report: %+v, %v", list, err)
	}
	if all, _ := s.List(ctx, 0); len(all) != 2 || all[1].Machine != "b" {
		t.Errorf("every machine's services = %+v", all)
	}
	if _, err := s.Get(ctx, 1, "worker"); status(err) != http.StatusNotFound {
		t.Errorf("removed service: %v", err)
	}
	if _, err := s.SetAlerts(ctx, 1, "nope", true); status(err) != http.StatusNotFound {
		t.Errorf("alerts of an unknown service: %v", err)
	}

	// An empty report removes everything.
	if list, err := s.Replace(ctx, 1, []ServiceInput{}); err != nil || len(list) != 0 {
		t.Errorf("empty report: %+v, %v", list, err)
	}

	for name, bad := range map[string][]ServiceInput{
		"no name":      {{ContainerID: "c", State: StateRunning}},
		"no container": {{Name: "x", State: StateRunning}},
		"bad state":    {{Name: "x", ContainerID: "c", State: "sleeping"}},
		"bad health":   {{Name: "x", ContainerID: "c", State: StateRunning, Health: ptr("fine")}},
		"negative":     {{Name: "x", ContainerID: "c", State: StateRunning, RestartCount: -1}},
		"twice":        {running("x", "c"), running("x", "d")},
	} {
		if _, err := s.Replace(ctx, 1, bad); status(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422", name, err)
		}
	}
}

func TestReportsInARow(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	report := func(in ServiceInput) Service {
		t.Helper()
		list, err := s.Replace(ctx, 1, []ServiceInput{in})
		if err != nil {
			t.Fatal(err)
		}
		return list[0]
	}
	in := running("web", "c1")
	in.State, in.ExitCode = StateExited, ptr(int64(1))
	if sv := report(in); sv.DownReports != 1 || *sv.ExitCode != 1 {
		t.Errorf("down once = %+v", sv)
	}
	if sv := report(in); sv.DownReports != 2 {
		t.Errorf("down twice = %+v", sv)
	}
	in = running("web", "c1")
	in.ExitCode = ptr(int64(0)) // kept only while exited or dead
	if sv := report(in); sv.DownReports != 0 || sv.ExitCode != nil {
		t.Errorf("running again = %+v", sv)
	}

	// The restart count grows from 0 three times in a row, then stays.
	for i, want := range []int{1, 2, 3, 0} {
		in.RestartCount = []int64{1, 2, 3, 3}[i]
		if sv := report(in); sv.RestartingReports != want {
			t.Errorf("restart count %d: reports = %d, want %d", in.RestartCount, sv.RestartingReports, want)
		}
	}
	// A new container starts its count again; that isn't growth.
	in.ContainerID, in.RestartCount = "c2", 5
	if sv := report(in); sv.RestartingReports != 0 {
		t.Errorf("new container = %+v", sv)
	}
	in.State = StateRestarting
	report(in)
	if sv := report(in); sv.RestartingReports != 2 {
		t.Errorf("restarting state = %+v", sv)
	}
	in.State, in.Health = StateRunning, ptr(HealthUnhealthy)
	report(in)
	if sv := report(in); sv.UnhealthyReports != 2 || sv.RestartingReports != 0 {
		t.Errorf("unhealthy = %+v", sv)
	}
}

// stopped_by_tender clears when a Tender start or restart completes, when the service runs again,
// and when its container is made again or started outside Tender.
func TestStoppedByTender(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	first := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	exited := func(id string, started time.Time) ServiceInput {
		in := running("web", id)
		in.State, in.ExitCode, in.StartedAt = StateExited, ptr(int64(0)), &started
		return in
	}
	up := running("web", "c1")
	up.StartedAt = &first
	if _, err := s.Replace(ctx, 1, []ServiceInput{up}); err != nil {
		t.Fatal(err)
	}
	act := func(typ string) {
		t.Helper()
		if err := s.Finished(ctx, 1, "web", typ); err != nil {
			t.Fatal(err)
		}
	}
	flag := func() bool {
		t.Helper()
		sv, err := s.Get(ctx, 1, "web")
		if err != nil {
			t.Fatal(err)
		}
		return sv.StoppedByTender
	}
	report := func(in ServiceInput) {
		t.Helper()
		if _, err := s.Replace(ctx, 1, []ServiceInput{in}); err != nil {
			t.Fatal(err)
		}
	}

	act(actStop)
	report(exited("c1", first))
	if !flag() {
		t.Fatal("a completed stop didn't mark the service")
	}
	for _, typ := range []string{actStart, actRestart} {
		act(actStop)
		act(typ)
		if flag() {
			t.Errorf("still stopped_by_tender after a completed %s", typ)
		}
		report(exited("c1", first))
	}

	act(actStop)
	report(exited("c2", first))
	if flag() {
		t.Error("still stopped_by_tender after the container was made again")
	}

	act(actStop)
	report(exited("c2", first.Add(time.Hour)))
	if flag() {
		t.Error("still stopped_by_tender after the container was started outside Tender")
	}
}

// Usage is stored as reported; memory_high_reports counts reports above 90% of the limit, and an
// outage empties the usage and starts the count again.
func TestUsage(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	report := func(mem, limit *int64, cpu *float64) Service {
		t.Helper()
		in := running("web", "c1")
		in.MemoryBytes, in.MemoryLimitBytes, in.CPUPercent = mem, limit, cpu
		list, err := s.Replace(ctx, 1, []ServiceInput{in})
		if err != nil {
			t.Fatal(err)
		}
		return list[0]
	}
	gib := int64(1 << 30)
	high, low := int64(950<<20), int64(100<<20)
	if sv := report(&high, &gib, ptr(12.5)); sv.MemoryHighReports != 1 || *sv.MemoryBytes != high || *sv.MemoryLimitBytes != gib || *sv.CPUPercent != 12.5 {
		t.Errorf("first = %+v", sv)
	}
	if sv := report(&high, &gib, nil); sv.MemoryHighReports != 2 || sv.CPUPercent != nil {
		t.Errorf("second = %+v", sv)
	}
	if sv := report(&low, &gib, nil); sv.MemoryHighReports != 0 {
		t.Errorf("below = %+v", sv)
	}
	if sv := report(&high, nil, nil); sv.MemoryHighReports != 0 || sv.MemoryLimitBytes != nil {
		t.Errorf("no limit = %+v", sv)
	}
	report(&high, &gib, nil)
	if err := s.Unreachable(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if sv, _ := s.Get(ctx, 1, "web"); sv.MemoryBytes != nil || sv.CPUPercent != nil || sv.MemoryHighReports != 0 {
		t.Errorf("while unreachable = %+v", sv)
	}
	for name, in := range map[string]ServiceInput{
		"negative memory": {Name: "x", ContainerID: "c", State: StateRunning, MemoryBytes: ptr(int64(-1))},
		"zero limit":      {Name: "x", ContainerID: "c", State: StateRunning, MemoryLimitBytes: ptr(int64(0))},
		"negative cpu":    {Name: "x", ContainerID: "c", State: StateRunning, CPUPercent: ptr(-1.0)},
	} {
		if _, err := s.Replace(ctx, 1, []ServiceInput{in}); status(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422", name, err)
		}
	}
}

func TestUnreachable(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	if ms, err := s.Machines(ctx, 0); err != nil || len(ms) != 0 {
		t.Errorf("before any report: %+v, %v", ms, err)
	}
	if ms, err := s.Machines(ctx, 2); err != nil || len(ms) != 1 || ms[0].Machine != "b" || ms[0].Docker != nil || len(ms[0].Data) != 0 {
		t.Errorf("one machine before any report: %+v, %v", ms, err)
	}
	down := running("web", "c1")
	down.State, down.ExitCode = StateExited, ptr(int64(1))
	if err := s.Apply(ctx, 1, RunningReport([]ServiceInput{down})); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.Apply(ctx, 1, UnreachableReport()); err != nil {
			t.Fatal(err)
		}
	}
	ms, err := s.Machines(ctx, 0)
	if err != nil || len(ms) != 1 || *ms[0].Docker != DockerUnreachable || ms[0].UnreachableReports() != 3 {
		t.Fatalf("unreachable: %+v, %v", ms, err)
	}
	// The list stays with its state unknown, and the counts start again, so two reports in a row
	// can't span the outage.
	if sv := ms[0].Data[0]; sv.State != StateUnknown || sv.DownReports != 0 || sv.RestartingReports != 0 || sv.UnhealthyReports != 0 || *sv.ExitCode != 1 {
		t.Errorf("service while unreachable = %+v", sv)
	}
	if err := s.Apply(ctx, 1, RunningReport([]ServiceInput{down})); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.Machines(ctx, 1)
	if *ms[0].Docker != DockerRunning || ms[0].UnreachableReports() != 0 || ms[0].Data[0].DownReports != 1 {
		t.Errorf("back: %+v", ms[0])
	}
}
