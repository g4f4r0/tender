package notifications

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// up is machine a, reporting at t0 plus after, with these services.
func up(after time.Duration, svcs ...Service) Machine {
	m := quiet(after)
	m.Services = svcs
	return m
}

func TestEvaluateServices(t *testing.T) {
	down := func(reports int) Service {
		return Service{Name: "web", State: "exited", ExitCode: ptr(int64(1)), AlertsEnabled: true, DownReports: reports}
	}
	cases := []struct {
		name string
		s    Service
		want []string
	}{
		{"running", Service{Name: "web", State: "running", AlertsEnabled: true}, []string{}},
		{"down in one report", down(1), []string{}},
		{"down in two reports", down(2), []string{"service.down:critical"}},
		{"down in five reports", down(5), []string{"service.down:critical"}},
		{"down, alerts off", func() Service { s := down(2); s.AlertsEnabled = false; return s }(), []string{}},
		{"stopped by Tender", func() Service { s := down(3); s.StoppedByTender = true; return s }(), []string{}},
		{"finished: exit 0, policy no", Service{Name: "job", State: "exited", ExitCode: ptr(int64(0)), RestartPolicy: "no", AlertsEnabled: true, DownReports: 9}, []string{}},
		{"finished: exit 0, policy on-failure", Service{Name: "job", State: "exited", ExitCode: ptr(int64(0)), RestartPolicy: "on-failure", AlertsEnabled: true, DownReports: 9}, []string{}},
		{"exit 0, policy always", Service{Name: "web", State: "exited", ExitCode: ptr(int64(0)), RestartPolicy: "always", AlertsEnabled: true, DownReports: 2}, []string{"service.down:critical"}},
		{"exit 0, policy unless-stopped", Service{Name: "web", State: "exited", ExitCode: ptr(int64(0)), RestartPolicy: "unless-stopped", AlertsEnabled: true, DownReports: 2}, []string{"service.down:critical"}},
		{"exit 1, policy no", Service{Name: "job", State: "exited", ExitCode: ptr(int64(1)), RestartPolicy: "no", AlertsEnabled: true, DownReports: 2}, []string{"service.down:critical"}},
		{"down streak, but its state is unknown now", Service{Name: "web", State: "unknown", ExitCode: ptr(int64(1)), AlertsEnabled: true, DownReports: 2}, []string{}},
		{"restarting in one report", Service{Name: "web", State: "restarting", AlertsEnabled: true, RestartingReports: 1}, []string{}},
		{"restarting in two reports", Service{Name: "web", State: "restarting", AlertsEnabled: true, RestartingReports: 2, RestartCount: 7},
			[]string{"service.restarting:critical"}},
		{"unhealthy in one report", Service{Name: "web", State: "running", AlertsEnabled: true, UnhealthyReports: 1}, []string{}},
		{"unhealthy in two reports", Service{Name: "web", State: "running", AlertsEnabled: true, UnhealthyReports: 2},
			[]string{"service.unhealthy:warning"}},
		{"restarting and unhealthy", Service{Name: "web", State: "running", AlertsEnabled: true, RestartingReports: 2, UnhealthyReports: 2},
			[]string{"service.restarting:critical", "service.unhealthy:warning"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summary(evaluate(up(0, c.s), t0)); !slices.Equal(got, c.want) {
				t.Errorf("findings = %v, want %v", got, c.want)
			}
		})
	}

	// A silent machine alerts as silent, not for its services.
	m := silent()
	m.Services = []Service{down(2)}
	if got := summary(evaluate(m, t0)); !slices.Equal(got, []string{"machine.silent:critical"}) {
		t.Errorf("silent machine findings = %v", got)
	}

	f := evaluate(up(0, down(2)), t0)
	if f[0].service != "web" || f[0].content != "The service web on a stopped with exit code 1." {
		t.Errorf("down finding = %+v", f[0])
	}
	dead := down(2)
	dead.State = "dead"
	if f := evaluate(up(0, dead), t0); !strings.Contains(f[0].content, "is dead") {
		t.Errorf("dead finding = %+v", f[0])
	}
	if f := evaluate(up(0, Service{Name: "web", AlertsEnabled: true, RestartingReports: 2, RestartCount: 1}), t0); f[0].content !=
		"The service web on a keeps restarting. Docker has restarted it 1 time." {
		t.Errorf("restarting finding = %+v", f[0])
	}
}

// serviceRows lists every notification as "event service status".
func (r *rig) serviceRows() []string {
	r.t.Helper()
	rows, err := r.db.Read.Query(`SELECT event, coalesce(service, '-'), status FROM notifications ORDER BY id`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, a+" "+b+" "+c)
	}
	return out
}

func TestServiceAlerts(t *testing.T) {
	r := newRig(t)
	web := Service{Name: "web", State: "exited", ExitCode: ptr(int64(1)), AlertsEnabled: true, DownReports: 2}
	worker := Service{Name: "worker", State: "exited", ExitCode: ptr(int64(2)), AlertsEnabled: true, DownReports: 2}

	// Two down services are two alerts, each emailed once.
	r.tick(0, up(0, web, worker))
	r.tick(time.Minute, up(time.Minute, web, worker))
	if got, want := r.serviceRows(), []string{"service.down web sent", "service.down worker sent"}; !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
	if want := []string{"a: web stopped", "a: worker stopped"}; !slices.Equal(r.sender.subjects, want) {
		t.Errorf("emails = %q, want %q", r.sender.subjects, want)
	}
	if !strings.Contains(r.sender.texts[0], "The service web on a stopped with exit code 1.") ||
		!strings.Contains(r.sender.texts[0], "call list_services for a") {
		t.Errorf("opened text:\n%s", r.sender.texts[0])
	}

	// The machine goes silent: its service alerts stay open, and nothing new is emailed about them.
	m := silent()
	m.LastSeen = ptr(t0.Add(time.Minute))
	m.Services = []Service{web, worker}
	r.tick(20*time.Minute, m)
	if got, want := r.serviceRows(), []string{"service.down web sent", "service.down worker sent", "machine.silent - sent"}; !slices.Equal(got, want) {
		t.Errorf("silent rows = %q, want %q", got, want)
	}

	// Back, with alerts off for web: web's problem resolves, worker's stays.
	web.AlertsEnabled = false
	r.tick(21*time.Minute, up(21*time.Minute, web, worker))
	want := []string{"service.down web sent", "service.down worker sent", "machine.silent - sent", "service.down.resolved web sent", "machine.silent.resolved - sent"}
	if got := r.serviceRows(); !slices.Equal(got, want) {
		t.Errorf("rows after alerts off = %q, want %q", got, want)
	}

	// worker is removed: its problem resolves too.
	r.tick(22*time.Minute, up(22*time.Minute, web))
	if got, want := r.serviceRows(), append(want, "service.down.resolved worker sent"); !slices.Equal(got, want) {
		t.Errorf("rows after removal = %q, want %q", got, want)
	}
	if want := "a: worker stopped, resolved"; r.sender.subjects[len(r.sender.subjects)-1] != want {
		t.Errorf("last email = %q, want %q", r.sender.subjects[len(r.sender.subjects)-1], want)
	}

	// Machine alerts still have no service.
	var n int
	if err := r.db.Read.QueryRow(`SELECT count(*) FROM notifications WHERE event = 'machine.silent' AND service IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Errorf("machine.silent rows without a service = %d, %v", n, err)
	}
}

func TestServiceAlertListsItsService(t *testing.T) {
	r := newRig(t)
	r.tick(0, up(0, Service{Name: "web", State: "running", AlertsEnabled: true, UnhealthyReports: 2}))
	ns, err := r.engine.store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n, ok := ns[key{EventServiceUnhealthy, "a", "web", ""}]
	if !ok || n.Service == nil || *n.Service != "web" || n.Severity != SeverityWarning {
		t.Errorf("open = %+v", ns)
	}
}

// After a Docker outage a service's counts start again, so its open alert stays while the
// reports still find it down, and resolves only when they don't. Alerts off resolve it at once,
// outage or not.
func TestServiceAlertsAcrossAnOutage(t *testing.T) {
	r := newRig(t)
	down := func(reports int) Service {
		return Service{Name: "web", State: "exited", ExitCode: ptr(int64(1)), AlertsEnabled: true, DownReports: reports}
	}
	unreachable := func(after time.Duration, s Service) Machine {
		m := up(after, s)
		m.Docker, m.DockerUnreachableReports = "unreachable", 2
		return m
	}
	r.tick(0, up(0, down(2)))
	unknown := down(0)
	unknown.State = "unknown"
	r.tick(time.Minute, unreachable(time.Minute, unknown))
	for i, reports := range []int{1, 2} {
		after := time.Duration(2+i) * time.Minute
		m := up(after, down(reports))
		m.Docker = "running"
		r.tick(after, m)
		if got, want := r.serviceRows(), []string{"service.down web sent", "docker.unreachable - sent", "docker.unreachable.resolved - sent"}; !slices.Equal(got, want) {
			t.Errorf("back, down in %d report(s): rows = %q, want %q", reports, got, want)
		}
	}
	if want := []string{"a: web stopped", "a: Docker unreachable", "a: Docker unreachable, resolved"}; !slices.Equal(r.sender.subjects, want) {
		t.Errorf("emails = %q, want %q", r.sender.subjects, want)
	}
	r.tick(4*time.Minute, up(4*time.Minute, Service{Name: "web", State: "running", AlertsEnabled: true}))
	if got := r.serviceRows(); got[3] != "service.down.resolved web sent" {
		t.Errorf("running again: rows = %q", got)
	}

	// Down again, then Docker stops answering and alerts go off for web: it resolves meanwhile.
	r.tick(5*time.Minute, up(5*time.Minute, down(2)))
	off := unknown
	off.AlertsEnabled = false
	r.tick(6*time.Minute, unreachable(6*time.Minute, off))
	if got := r.serviceRows(); len(got) != 7 || got[4] != "service.down web sent" || got[6] != "service.down.resolved web sent" {
		t.Errorf("alerts off while Docker is unreachable: rows = %q", got)
	}
}

func TestMemoryHigh(t *testing.T) {
	gib, used := int64(1<<30), int64(950<<20)
	svc := func(reports int, limit *int64) Service {
		return Service{Name: "web", State: "running", AlertsEnabled: true, MemoryBytes: &used, MemoryLimitBytes: limit, MemoryHighReports: reports}
	}
	for _, c := range []struct {
		name string
		s    Service
		want []string
	}{
		{"one report", svc(1, &gib), []string{}},
		{"two reports", svc(2, &gib), []string{"service.memory_high:warning"}},
		{"no limit", svc(2, nil), []string{}},
		{"alerts off", func() Service { s := svc(2, &gib); s.AlertsEnabled = false; return s }(), []string{}},
	} {
		if got := summary(evaluate(up(0, c.s), t0)); !slices.Equal(got, c.want) {
			t.Errorf("%s: findings = %v, want %v", c.name, got, c.want)
		}
	}
	if f := evaluate(up(0, svc(2, &gib)), t0); f[0].content != "The service web on a uses 950 MiB, 92% of its 1 GiB memory limit." {
		t.Errorf("content = %q", f[0].content)
	}

	// It opens, emails, and resolves once the service is back under the limit.
	r := newRig(t)
	r.tick(0, up(0, svc(2, &gib)))
	below := svc(0, &gib)
	r.tick(time.Minute, up(time.Minute, below))
	if got, want := r.serviceRows(), []string{"service.memory_high web sent", "service.memory_high.resolved web sent"}; !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
	if want := []string{"a: web memory high", "a: web memory high, resolved"}; !slices.Equal(r.sender.subjects, want) {
		t.Errorf("emails = %q, want %q", r.sender.subjects, want)
	}
}

func TestEvaluateDocker(t *testing.T) {
	down := Service{Name: "web", State: "exited", ExitCode: ptr(int64(1)), AlertsEnabled: true, DownReports: 2}
	for _, c := range []struct {
		name    string
		docker  string
		reports int
		want    []string
	}{
		{"running", "running", 0, []string{"service.down:critical"}},
		{"unreachable once", "unreachable", 1, []string{}},
		{"unreachable twice", "unreachable", 2, []string{"docker.unreachable:critical"}},
		{"before any report", "", 0, []string{"service.down:critical"}},
	} {
		m := up(0, down)
		m.Docker, m.DockerUnreachableReports = c.docker, c.reports
		if got := summary(evaluate(m, t0)); !slices.Equal(got, c.want) {
			t.Errorf("%s: findings = %v, want %v", c.name, got, c.want)
		}
	}
	m := silent()
	m.Docker, m.DockerUnreachableReports = "unreachable", 5
	if got := summary(evaluate(m, t0)); !slices.Equal(got, []string{"machine.silent:critical"}) {
		t.Errorf("silent machine with Docker unreachable = %v", got)
	}
}

func TestDockerUnreachableAlert(t *testing.T) {
	r := newRig(t)
	web := Service{Name: "web", State: "exited", ExitCode: ptr(int64(1)), AlertsEnabled: true, DownReports: 2}
	r.tick(0, up(0, web))
	unreachable := func(after time.Duration, reports int) Machine {
		m := up(after, Service{Name: "web", State: "unknown", AlertsEnabled: true, DownReports: 2})
		m.Docker, m.DockerUnreachableReports = "unreachable", reports
		return m
	}
	// One report: nothing yet. Two: one machine alert; web's alert stays open though its state is unknown.
	r.tick(time.Minute, unreachable(time.Minute, 1))
	r.tick(2*time.Minute, unreachable(2*time.Minute, 2))
	if got, want := r.serviceRows(), []string{"service.down web sent", "docker.unreachable - sent"}; !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
	if want := []string{"a: web stopped", "a: Docker unreachable"}; !slices.Equal(r.sender.subjects, want) {
		t.Errorf("emails = %q, want %q", r.sender.subjects, want)
	}
	if !strings.Contains(r.sender.texts[1], "Docker on a can't be reached: ") {
		t.Errorf("text:\n%s", r.sender.texts[1])
	}

	// The machine goes silent: both stay as they are.
	m := unreachable(2*time.Minute, 2)
	r.tick(20*time.Minute, m)
	if got, want := r.serviceRows(), []string{"service.down web sent", "docker.unreachable - sent", "machine.silent - sent"}; !slices.Equal(got, want) {
		t.Errorf("silent rows = %q, want %q", got, want)
	}

	// Docker answers again, web runs: both resolve.
	web.State, web.DownReports = "running", 0
	r.tick(21*time.Minute, up(21*time.Minute, web))
	if got, want := r.serviceRows(), []string{"service.down web sent", "docker.unreachable - sent", "machine.silent - sent",
		"service.down.resolved web sent", "docker.unreachable.resolved - sent", "machine.silent.resolved - sent"}; !slices.Equal(got, want) {
		t.Errorf("rows after Docker is back = %q, want %q", got, want)
	}
}

// A daemon that still sends usage but stopped sending services reports, after it had sent them,
// has lost Docker without saying so: after 5 minutes that is docker.unreachable, and the services'
// states are unknown. A machine whose usage stopped too is just silent.
func TestDockerReportsStop(t *testing.T) {
	down := Service{Name: "web", State: "exited", ExitCode: ptr(int64(1)), AlertsEnabled: true, DownReports: 2}
	for _, c := range []struct {
		name               string
		reported, lastSeen time.Duration // before t0
		want               []string
	}{
		{"fresh", time.Minute, 0, []string{"service.down:critical"}},
		{"4 minutes old", 4 * time.Minute, 0, []string{"service.down:critical"}},
		{"5 minutes old, usage fresh", 5 * time.Minute, 30 * time.Second, []string{"docker.unreachable:critical"}},
		{"usage stopped as well", 6 * time.Minute, 6 * time.Minute, []string{"service.down:critical"}},
	} {
		m := up(-c.lastSeen, down)
		m.Docker, m.DockerReportedAt = "running", ptr(t0.Add(-c.reported))
		if got := summary(evaluate(m, t0)); !slices.Equal(got, c.want) {
			t.Errorf("%s: findings = %v, want %v", c.name, got, c.want)
		}
	}

	// The service's open alert stays while Docker's reports are missing, and resolves with it.
	r := newRig(t)
	r.tick(0, up(0, down))
	m := up(6*time.Minute, down)
	m.Docker, m.DockerReportedAt = "running", ptr(t0)
	m.Services[0].State = "running"
	r.tick(6*time.Minute, m)
	if got, want := r.serviceRows(), []string{"service.down web sent", "docker.unreachable - sent"}; !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
}

// The docker.unreachable alert says why Docker can't be reached, as the daemon said.
func TestDockerUnreachableSaysWhy(t *testing.T) {
	for reason, want := range map[string]string{
		"refused":           "its socket refuses connections, so Docker is stopped",
		"timeout":           "it didn't answer within 5 seconds, so it is stuck or overloaded",
		"socket_missing":    "its socket is gone, so Docker was stopped or removed",
		"permission_denied": "Tender may not use its socket. If Docker was installed after Tender, run: sudo tender install refresh",
		"":                  "the daemon couldn't say why",
	} {
		m := up(0)
		m.Docker, m.DockerReason, m.DockerUnreachableReports = "unreachable", reason, 2
		f := evaluate(m, t0)
		if len(f) != 1 || !strings.Contains(f[0].content, "Docker on a can't be reached: "+want+".") || strings.Contains(f[0].content, "refuses connections or doesn't answer") {
			t.Errorf("%q: %+v", reason, f)
		}
	}
}
