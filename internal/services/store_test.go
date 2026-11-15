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

func TestActions(t *testing.T) {
	d := newDB(t)
	s := NewStore(d)
	ctx := context.Background()
	if _, err := s.Replace(ctx, 1, []ServiceInput{running("web", "c1")}); err != nil {
		t.Fatal(err)
	}

	for name, in := range map[string]ActionInput{
		"no type":        {},
		"unknown type":   {Type: "kill"},
		"lines on start": {Type: TypeStart, Lines: ptr(5)},
		"zero lines":     {Type: TypeLogs, Lines: ptr(0)},
		"too many lines": {Type: TypeLogs, Lines: ptr(2001)},
	} {
		if _, err := s.CreateAction(ctx, 1, "web", in); status(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422", name, err)
		}
	}
	if _, err := s.CreateAction(ctx, 1, "nope", ActionInput{Type: TypeStart}); status(err) != http.StatusNotFound {
		t.Errorf("unknown service: %v", err)
	}
	logs, err := s.CreateAction(ctx, 1, "web", ActionInput{Type: TypeLogs})
	if err != nil || logs.Status != StatusPending || *logs.Lines != DefaultLines || logs.Service != "web" || logs.Machine != "a" {
		t.Fatalf("logs action = %+v, %v", logs, err)
	}
	stop, err := s.CreateAction(ctx, 1, "web", ActionInput{Type: TypeStop})
	if err != nil {
		t.Fatal(err)
	}

	// Another machine's daemon sees none of them.
	if list, err := s.Take(ctx, 2); err != nil || len(list) != 0 {
		t.Errorf("machine b took %+v, %v", list, err)
	}
	list, err := s.Take(ctx, 1)
	if err != nil || len(list) != 2 || list[0].ID != logs.ID || list[1].Status != StatusInProgress {
		t.Fatalf("take = %+v, %v", list, err)
	}
	if again, _ := s.Take(ctx, 1); len(again) != 0 {
		t.Errorf("taken twice: %+v", again)
	}

	if err := s.Finish(ctx, 1, logs.ID, ActionPatch{Status: "done"}); status(err) != http.StatusUnprocessableEntity {
		t.Errorf("bad status: %v", err)
	}
	if err := s.Finish(ctx, 1, logs.ID, ActionPatch{Status: StatusCompleted, Output: ptr("hello\n")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, 1, logs.ID, ActionPatch{Status: StatusFailed}); status(err) != http.StatusConflict {
		t.Errorf("finished twice: %v", err)
	}
	if err := s.Finish(ctx, 2, stop.ID, ActionPatch{Status: StatusCompleted}); status(err) != http.StatusNotFound {
		t.Errorf("another machine's action: %v", err)
	}
	if got, err := s.Action(ctx, 1, logs.ID); err != nil || got.Status != StatusCompleted || *got.Output != "hello\n" {
		t.Errorf("logs = %+v, %v", got, err)
	}

	// A completed stop marks the service, until it runs again.
	if err := s.Finish(ctx, 1, stop.ID, ActionPatch{Status: StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	in := running("web", "c1")
	in.State = StateExited
	if list, _ := s.Replace(ctx, 1, []ServiceInput{in}); !list[0].StoppedByTender {
		t.Errorf("after a Tender stop = %+v", list[0])
	}
	if list, _ := s.Replace(ctx, 1, []ServiceInput{running("web", "c1")}); list[0].StoppedByTender {
		t.Errorf("running again = %+v", list[0])
	}

	// Await gives up after its timeout, with the action as it is.
	start, _ := s.CreateAction(ctx, 1, "web", ActionInput{Type: TypeStart})
	if got, err := s.Await(ctx, 1, start.ID, 10*time.Millisecond); err != nil || got.Status != StatusPending {
		t.Errorf("await = %+v, %v", got, err)
	}

	// Old actions fail: one never picked up, one never answered.
	if _, err := s.Take(ctx, 1); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.CreateAction(ctx, 1, "web", ActionInput{Type: TypeRestart})
	old := time.Now().Add(-16 * time.Minute).Unix()
	if _, err := d.Write.Exec(`UPDATE service_actions SET created_at = ?, updated_at = ? WHERE id IN (?, ?)`, old, old, pending.ID, start.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Action(ctx, 1, pending.ID); got.Status != StatusFailed || *got.Output != notPickedUp {
		t.Errorf("never picked up = %+v", got)
	}
	if got, _ := s.Action(ctx, 1, start.ID); got.Status != StatusFailed || *got.Output != notAnswered {
		t.Errorf("never answered = %+v", got)
	}

	// Each machine keeps its newest 100.
	for range 110 {
		if _, err := s.CreateAction(ctx, 1, "web", ActionInput{Type: TypeStart}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := d.Read.QueryRow(`SELECT count(*) FROM service_actions WHERE machine_id = 1`).Scan(&n); err != nil || n != 100 {
		t.Errorf("kept %d actions, %v", n, err)
	}

	// A removed service takes its actions with it.
	if _, err := s.Replace(ctx, 1, []ServiceInput{}); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow(`SELECT count(*) FROM service_actions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d actions left after the service went, %v", n, err)
	}
}

func TestWait(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	if _, err := s.Replace(ctx, 1, []ServiceInput{running("web", "c1")}); err != nil {
		t.Fatal(err)
	}

	// Nothing pending: it waits the whole time, then answers an empty list.
	start := time.Now()
	if list, err := s.Wait(ctx, 1, 700*time.Millisecond, nil); err != nil || len(list) != 0 || time.Since(start) < 700*time.Millisecond {
		t.Errorf("empty wait = %+v, %v after %s", list, err, time.Since(start))
	}

	// An action queued meanwhile, from any process, answers within a poll.
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = NewStore(s.db).CreateAction(ctx, 1, "web", ActionInput{Type: TypeRestart})
	}()
	start = time.Now()
	list, err := s.Wait(ctx, 1, 10*time.Second, nil)
	if err != nil || len(list) != 1 || time.Since(start) > 2*time.Second {
		t.Errorf("wait = %+v, %v after %s", list, err, time.Since(start))
	}

	// The hub shutting down ends the wait at once.
	stop := make(chan struct{})
	close(stop)
	start = time.Now()
	if _, err := s.Wait(ctx, 1, 10*time.Second, stop); err != nil || time.Since(start) > time.Second {
		t.Errorf("wait while stopping: %v after %s", err, time.Since(start))
	}
}
