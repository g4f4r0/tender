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

func running(name, id string) ContainerInput {
	return ContainerInput{Name: name, ContainerID: id, Image: "img", State: StateRunning}
}

// composed is a container of a compose project, with the labels compose gives it.
func composed(project, service, id string) ContainerInput {
	c := running(project+"-"+service+"-1", id)
	c.Project, c.ComposeService = &project, &service
	return c
}

// containerOf returns the container named name in the machine's service.
func containerOf(t *testing.T, s *Store, machineID int64, service, name string) Container {
	t.Helper()
	sv, err := s.Get(context.Background(), machineID, service)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range sv.Containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("service %s has no container %s: %+v", service, name, sv.Containers)
	return Container{}
}

// lone returns a container without a compose project, which is its own service.
func lone(t *testing.T, s *Store, machineID int64, name string) Container {
	t.Helper()
	return containerOf(t, s, machineID, name, name)
}

func TestReplace(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	started := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	web := composed("shop", "web", "c1")
	web.StartedAt, web.Health = &started, ptr(HealthHealthy)
	pg := running("shop-db-1", "c2") // without the compose service label: its name less the project's
	pg.Project = ptr("shop")
	if err := s.Replace(ctx, 1, []ContainerInput{web, pg, running("worker", "c3")}); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, 1)
	if err != nil || len(list) != 2 || list[0].Name != "shop" || list[1].Name != "worker" {
		t.Fatalf("first report: %+v, %v", list, err)
	}
	shop := list[0]
	if shop.Machine != "a" || shop.Managed || shop.State != ServiceRunning || !shop.AlertsEnabled || shop.DeployStatus != nil ||
		len(shop.Containers) != 2 || shop.Containers[0].Name != "db" || shop.Containers[1].Name != "web" {
		t.Fatalf("shop = %+v", shop)
	}
	w := shop.Containers[1]
	if w.Runtime != RuntimeDocker || !w.StartedAt.Equal(started) || w.StoppedByTender || *w.Health != HealthHealthy || w.ContainerID != "c1" {
		t.Errorf("web = %+v", w)
	}
	if err := s.Replace(ctx, 2, []ContainerInput{running("web", "c9")}); err != nil {
		t.Fatal(err)
	}

	// Alerts off follow the service across a new container; db and worker are gone.
	if _, err := s.SetAlerts(ctx, 1, "shop", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(ctx, 1, []ContainerInput{composed("shop", "web", "c4")}); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List(ctx, 1)
	shop = list[0]
	if shop.AlertsEnabled || shop.State != ServiceRunning || shop.Containers[0].State != StateRemoved ||
		shop.Containers[1].ID != w.ID || shop.Containers[1].ContainerID != "c4" {
		t.Errorf("second report, shop = %+v", shop)
	}
	if list[1].State != ServiceStopped || list[1].Containers[0].State != StateRemoved {
		t.Errorf("second report, worker = %+v", list[1])
	}
	if all, _ := s.List(ctx, 0); len(all) != 3 || all[2].Machine != "b" {
		t.Errorf("every machine's services = %+v", all)
	}
	if _, err := s.SetAlerts(ctx, 1, "nope", true); status(err) != http.StatusNotFound {
		t.Errorf("alerts of an unknown service: %v", err)
	}

	// An empty report removes everything.
	if err := s.Replace(ctx, 1, []ContainerInput{}); err != nil {
		t.Fatal(err)
	}
	if c := containerOf(t, s, 1, "shop", "web"); c.State != StateRemoved {
		t.Errorf("empty report: web = %+v", c)
	}

	for name, bad := range map[string][]ContainerInput{
		"no name":      {{ContainerID: "c", State: StateRunning}},
		"no container": {{Name: "x", State: StateRunning}},
		"bad state":    {{Name: "x", ContainerID: "c", State: "sleeping"}},
		"bad health":   {{Name: "x", ContainerID: "c", State: StateRunning, Health: ptr("fine")}},
		"negative":     {{Name: "x", ContainerID: "c", State: StateRunning, RestartCount: -1}},
		"twice":        {running("x", "c"), running("x", "d")},
		"bad runtime":  {{Name: "x", Runtime: "systemd", ContainerID: "c", State: StateRunning}},
	} {
		if err := s.Replace(ctx, 1, bad); status(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422", name, err)
		}
	}
}

// A service's state comes from the containers that should run; one that did its job doesn't count.
func TestServiceState(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	migrate := composed("notes", "migrate", "c3")
	migrate.State, migrate.ExitCode = StateExited, ptr(int64(0))
	down := composed("notes", "worker", "c2")
	down.State, down.ExitCode, down.RestartPolicy = StateExited, ptr(int64(1)), PolicyAlways
	for _, tc := range []struct {
		name string
		in   []ContainerInput
		want string
	}{
		{"all up", []ContainerInput{composed("notes", "web", "c1"), migrate}, ServiceRunning},
		{"one down", []ContainerInput{composed("notes", "web", "c1"), down, migrate}, ServiceDegraded},
		{"none up", []ContainerInput{down, migrate}, ServiceStopped},
	} {
		if err := s.Replace(ctx, 1, tc.in); err != nil {
			t.Fatal(err)
		}
		if sv, _ := s.Get(ctx, 1, "notes"); sv.State != tc.want {
			t.Errorf("%s: state %s, want %s", tc.name, sv.State, tc.want)
		}
	}
	if err := s.Unreachable(ctx, 1, ReasonRefused); err != nil {
		t.Fatal(err)
	}
	if sv, _ := s.Get(ctx, 1, "notes"); sv.State != ServiceUnknown {
		t.Errorf("unreachable: state %s", sv.State)
	}
}

func TestReportsInARow(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	report := func(in ContainerInput) Container {
		t.Helper()
		if err := s.Replace(ctx, 1, []ContainerInput{in}); err != nil {
			t.Fatal(err)
		}
		return lone(t, s, 1, in.Name)
	}
	in := running("web", "c1")
	in.State, in.ExitCode = StateExited, ptr(int64(1))
	if c := report(in); c.DownReports != 1 || *c.ExitCode != 1 {
		t.Errorf("down once = %+v", c)
	}
	if c := report(in); c.DownReports != 2 {
		t.Errorf("down twice = %+v", c)
	}
	in = running("web", "c1")
	in.ExitCode = ptr(int64(0)) // kept only while exited or dead
	if c := report(in); c.DownReports != 0 || c.ExitCode != nil {
		t.Errorf("running again = %+v", c)
	}

	// The restart count grows from 0 three times in a row, then stays.
	for i, want := range []int{1, 2, 3, 0} {
		in.RestartCount = []int64{1, 2, 3, 3}[i]
		if c := report(in); c.RestartingReports != want {
			t.Errorf("restart count %d: reports = %d, want %d", in.RestartCount, c.RestartingReports, want)
		}
	}
	// A new container starts its count again; that isn't growth.
	in.ContainerID, in.RestartCount = "c2", 5
	if c := report(in); c.RestartingReports != 0 {
		t.Errorf("new container = %+v", c)
	}
	in.State = StateRestarting
	report(in)
	if c := report(in); c.RestartingReports != 2 {
		t.Errorf("restarting state = %+v", c)
	}
	in.State, in.Health = StateRunning, ptr(HealthUnhealthy)
	report(in)
	if c := report(in); c.UnhealthyReports != 2 || c.RestartingReports != 0 {
		t.Errorf("unhealthy = %+v", c)
	}
}

// stopped_by_tender clears when a Tender start or restart completes, when the container runs again,
// and when it is made again or started outside Tender.
func TestStoppedByTender(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	first := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	exited := func(id string, started time.Time) ContainerInput {
		in := running("web", id)
		in.State, in.ExitCode, in.StartedAt = StateExited, ptr(int64(0)), &started
		return in
	}
	up := running("web", "c1")
	up.StartedAt = &first
	if err := s.Replace(ctx, 1, []ContainerInput{up}); err != nil {
		t.Fatal(err)
	}
	act := func(typ string) {
		t.Helper()
		if err := s.Finished(ctx, 1, "web", "", typ); err != nil {
			t.Fatal(err)
		}
	}
	flag := func() bool {
		t.Helper()
		return lone(t, s, 1, "web").StoppedByTender
	}
	report := func(in ContainerInput) {
		t.Helper()
		if err := s.Replace(ctx, 1, []ContainerInput{in}); err != nil {
			t.Fatal(err)
		}
	}

	act(actStop)
	report(exited("c1", first))
	if !flag() {
		t.Fatal("a completed stop didn't mark the container")
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

	// A stop of one container marks only it.
	if err := s.Replace(ctx, 1, []ContainerInput{composed("notes", "web", "n1"), composed("notes", "db", "n2")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finished(ctx, 1, "notes", "db", actStop); err != nil {
		t.Fatal(err)
	}
	if !containerOf(t, s, 1, "notes", "db").StoppedByTender || containerOf(t, s, 1, "notes", "web").StoppedByTender {
		t.Error("a stop of db marked the wrong containers")
	}
}

// Usage is stored as reported; memory_high_reports counts reports above 90% of the limit, and an
// outage empties the usage and starts the count again.
func TestUsage(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	report := func(mem, limit *int64, cpu *float64) Container {
		t.Helper()
		in := running("web", "c1")
		in.MemoryBytes, in.MemoryLimitBytes, in.CPUPercent = mem, limit, cpu
		if err := s.Replace(ctx, 1, []ContainerInput{in}); err != nil {
			t.Fatal(err)
		}
		return lone(t, s, 1, "web")
	}
	gib := int64(1 << 30)
	high, low := int64(950<<20), int64(100<<20)
	if c := report(&high, &gib, ptr(12.5)); c.MemoryHighReports != 1 || *c.MemoryBytes != high || *c.MemoryLimitBytes != gib || *c.CPUPercent != 12.5 {
		t.Errorf("first = %+v", c)
	}
	if c := report(&high, &gib, nil); c.MemoryHighReports != 2 || c.CPUPercent != nil {
		t.Errorf("second = %+v", c)
	}
	if c := report(&low, &gib, nil); c.MemoryHighReports != 0 {
		t.Errorf("below = %+v", c)
	}
	if c := report(&high, nil, nil); c.MemoryHighReports != 0 || c.MemoryLimitBytes != nil {
		t.Errorf("no limit = %+v", c)
	}
	report(&high, &gib, nil)
	if err := s.Unreachable(ctx, 1, ReasonRefused); err != nil {
		t.Fatal(err)
	}
	if c := lone(t, s, 1, "web"); c.MemoryBytes != nil || c.CPUPercent != nil || c.MemoryHighReports != 0 {
		t.Errorf("while unreachable = %+v", c)
	}
	for name, in := range map[string]ContainerInput{
		"negative memory": {Name: "x", ContainerID: "c", State: StateRunning, MemoryBytes: ptr(int64(-1))},
		"zero limit":      {Name: "x", ContainerID: "c", State: StateRunning, MemoryLimitBytes: ptr(int64(0))},
		"negative cpu":    {Name: "x", ContainerID: "c", State: StateRunning, CPUPercent: ptr(-1.0)},
	} {
		if err := s.Replace(ctx, 1, []ContainerInput{in}); status(err) != http.StatusUnprocessableEntity {
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
	if err := s.Apply(ctx, 1, RunningReport([]ContainerInput{down})); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.Apply(ctx, 1, UnreachableReport(ReasonRefused)); err != nil {
			t.Fatal(err)
		}
	}
	ms, err := s.Machines(ctx, 0)
	if err != nil || len(ms) != 1 || *ms[0].Docker != DockerUnreachable || ms[0].UnreachableReports() != 3 ||
		ms[0].DockerReason == nil || *ms[0].DockerReason != ReasonRefused {
		t.Fatalf("unreachable: %+v, %v", ms, err)
	}
	for name, bad := range map[string]Report{
		"no reason":           {Docker: DockerUnreachable},
		"unknown reason":      {Docker: DockerUnreachable, DockerReason: "gremlins"},
		"reason when it runs": {Docker: DockerRunning, DockerReason: ReasonTimeout, Containers: &[]ContainerInput{}},
		"both lists":          {Docker: DockerRunning, Containers: &[]ContainerInput{}, Services: &[]ContainerInput{}},
		"no list":             {Docker: DockerRunning},
	} {
		if err := s.Apply(ctx, 1, bad); status(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The list stays with its state unknown and no exit code, and the counts start again, so two reports in a row
	// can't span the outage.
	if c := ms[0].Data[0].Containers[0]; c.State != StateUnknown || c.DownReports != 0 || c.RestartingReports != 0 || c.UnhealthyReports != 0 || c.ExitCode != nil {
		t.Errorf("container while unreachable = %+v", c)
	}
	// A daemon before v0.4 sends its list as services.
	if err := s.Apply(ctx, 1, Report{Docker: DockerRunning, Services: &[]ContainerInput{down}}); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.Machines(ctx, 1)
	if *ms[0].Docker != DockerRunning || ms[0].UnreachableReports() != 0 || ms[0].Data[0].Containers[0].DownReports != 1 || ms[0].DockerReason != nil {
		t.Errorf("back: %+v", ms[0])
	}
}

// A running container whose stats couldn't be read keeps its last memory numbers and its count of
// reports above the limit, so a memory alert neither resolves nor restarts its count.
func TestStatsUnavailable(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	gib, high := int64(1<<30), int64(950<<20)
	send := func(in ContainerInput) Container {
		t.Helper()
		if err := s.Replace(ctx, 1, []ContainerInput{in}); err != nil {
			t.Fatal(err)
		}
		return lone(t, s, 1, "web")
	}
	in := running("web", "c1")
	in.MemoryBytes, in.MemoryLimitBytes, in.CPUPercent = &high, &gib, ptr(12.5)
	send(in)
	send(in)
	failed := running("web", "c1")
	failed.StatsUnavailable = true
	if c := send(failed); c.MemoryHighReports != 2 || c.MemoryBytes == nil || *c.MemoryBytes != high ||
		c.MemoryLimitBytes == nil || *c.MemoryLimitBytes != gib || c.CPUPercent != nil {
		t.Errorf("with stats unavailable = %+v; want the last memory, the count kept and no CPU", c)
	}
	if c := send(in); c.MemoryHighReports != 3 {
		t.Errorf("after stats came back = %+v; want the count going on", c)
	}
	// A new container has no numbers to keep.
	other := failed
	other.ContainerID = "c2"
	if c := send(other); c.MemoryBytes != nil || c.MemoryHighReports != 0 {
		t.Errorf("a new container with stats unavailable = %+v", c)
	}
	for name, bad := range map[string]ContainerInput{
		"stopped":      {Name: "x", ContainerID: "c", State: StateExited, StatsUnavailable: true},
		"with numbers": {Name: "x", ContainerID: "c", State: StateRunning, StatsUnavailable: true, MemoryBytes: &high},
	} {
		if err := s.Replace(ctx, 1, []ContainerInput{bad}); status(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422", name, err)
		}
	}
}

// A container that is gone keeps its row as removed: its counts start again so its alerts resolve,
// actions refuse it, and a container that comes back under the name takes the row back. After 7
// days removed it is purged, and an unmanaged service left without containers goes with it.
func TestRemovedContainer(t *testing.T) {
	d := newDB(t)
	s := NewStore(d)
	ctx := context.Background()
	worker := running("worker", "c2")
	worker.State, worker.ExitCode, worker.StartedAt = StateExited, ptr(int64(1)), ptr(time.Now().Add(-time.Hour))
	if err := s.Replace(ctx, 1, []ContainerInput{running("web", "c1"), worker}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAlerts(ctx, 1, "worker", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(ctx, 1, []ContainerInput{running("web", "c1")}); err != nil { // docker rm, say
		t.Fatal(err)
	}
	gone := lone(t, s, 1, "worker")
	if gone.State != StateRemoved || gone.ExitCode != nil || gone.DownReports != 0 || gone.RemovedAt == nil || gone.StartedAt != nil {
		t.Fatalf("removed = %+v", gone)
	}
	if err := s.Check(ctx, 1, "worker", ""); status(err) != http.StatusConflict {
		t.Errorf("an action on a service without containers: %v", err)
	}
	if err := s.Check(ctx, 1, "worker", "worker"); status(err) != http.StatusConflict {
		t.Errorf("an action on a removed container: %v", err)
	}
	if err := s.Check(ctx, 1, "web", "nope"); status(err) != http.StatusNotFound {
		t.Errorf("an action on an unknown container: %v", err)
	}
	if err := s.Check(ctx, 1, "web", "web"); err != nil {
		t.Errorf("an action on a running container: %v", err)
	}
	if err := s.Unreachable(ctx, 1, ReasonRefused); err != nil {
		t.Fatal(err)
	}
	if got := lone(t, s, 1, "worker"); got.State != StateRemoved {
		t.Errorf("Docker unreachable made a removed container %s", got.State)
	}

	if err := s.Replace(ctx, 1, []ContainerInput{running("web", "c1"), running("worker", "c9")}); err != nil {
		t.Fatal(err)
	}
	if got := lone(t, s, 1, "worker"); got.ID != gone.ID || got.State != StateRunning || got.RemovedAt != nil {
		t.Errorf("back = %+v; want row %d, running", got, gone.ID)
	}
	if sv, _ := s.Get(ctx, 1, "worker"); sv.AlertsEnabled {
		t.Error("the service's alerts came back on")
	}

	if err := s.Replace(ctx, 1, []ContainerInput{running("web", "c1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec(`UPDATE containers SET removed_at = ? WHERE name = 'worker'`, time.Now().Add(-7*24*time.Hour-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(ctx, 1, []ContainerInput{running("web", "c1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, 1, "worker"); status(err) != http.StatusNotFound {
		t.Errorf("removed 7 days ago, still there: %v", err)
	}
}

// Docker turned off forgets the machine's Docker: its engine row goes and every container is
// removed, with its counts at 0 and its service's alert switch kept. Another off report changes
// nothing, and a machine that never reported stays without a row.
func TestOff(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	down := running("web", "c1")
	down.State, down.ExitCode = StateExited, ptr(int64(1))
	for range 2 {
		if err := s.Apply(ctx, 1, RunningReport([]ContainerInput{down, running("db", "c2")})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetAlerts(ctx, 1, "db", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, 1, UnreachableReport(ReasonRefused)); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, 1, OffReport()); err != nil {
		t.Fatal(err)
	}
	ms, err := s.Machines(ctx, 1)
	if err != nil || ms[0].Docker != nil || ms[0].DockerReason != nil || ms[0].UnreachableReports() != 0 || ms[0].ReportedAt() != nil || len(ms[0].Data) != 2 {
		t.Fatalf("after off: %+v, %v", ms, err)
	}
	for _, sv := range ms[0].Data {
		c := sv.Containers[0]
		if c.State != StateRemoved || c.RemovedAt == nil || c.DownReports != 0 || c.ExitCode != nil || sv.AlertsEnabled != (sv.Name == "web") {
			t.Errorf("%s after off: %+v", sv.Name, sv)
		}
	}
	removedAt := ms[0].Data[0].Containers[0].RemovedAt
	if err := s.Apply(ctx, 1, OffReport()); err != nil {
		t.Fatal(err)
	}
	if again, err := s.Machines(ctx, 1); err != nil || again[0].Docker != nil || !again[0].Data[0].Containers[0].RemovedAt.Equal(*removedAt) {
		t.Errorf("after a second off: %+v, %v", again, err)
	}
	if err := s.Apply(ctx, 2, OffReport()); err != nil {
		t.Fatal(err)
	}
	if all, err := s.Machines(ctx, 0); err != nil || len(all) != 1 || all[0].Machine != "a" {
		t.Errorf("a machine whose only report is off is listed: %+v, %v", all, err)
	}
}
