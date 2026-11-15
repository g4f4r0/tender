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
		{"down in two reports", down(2), []string{"service_down:critical"}},
		{"down in five reports", down(5), []string{"service_down:critical"}},
		{"down, alerts off", func() Service { s := down(2); s.AlertsEnabled = false; return s }(), []string{}},
		{"stopped by Tender", func() Service { s := down(3); s.StoppedByTender = true; return s }(), []string{}},
		{"finished: exit 0, policy no", Service{Name: "job", State: "exited", ExitCode: ptr(int64(0)), RestartPolicy: "no", AlertsEnabled: true, DownReports: 9}, []string{}},
		{"finished: exit 0, policy on-failure", Service{Name: "job", State: "exited", ExitCode: ptr(int64(0)), RestartPolicy: "on-failure", AlertsEnabled: true, DownReports: 9}, []string{}},
		{"exit 0, policy always", Service{Name: "web", State: "exited", ExitCode: ptr(int64(0)), RestartPolicy: "always", AlertsEnabled: true, DownReports: 2}, []string{"service_down:critical"}},
		{"exit 0, policy unless-stopped", Service{Name: "web", State: "exited", ExitCode: ptr(int64(0)), RestartPolicy: "unless-stopped", AlertsEnabled: true, DownReports: 2}, []string{"service_down:critical"}},
		{"exit 1, policy no", Service{Name: "job", State: "exited", ExitCode: ptr(int64(1)), RestartPolicy: "no", AlertsEnabled: true, DownReports: 2}, []string{"service_down:critical"}},
		{"down streak, but its state is unknown now", Service{Name: "web", State: "unknown", ExitCode: ptr(int64(1)), AlertsEnabled: true, DownReports: 2}, []string{}},
		{"restarting in one report", Service{Name: "web", State: "restarting", AlertsEnabled: true, RestartingReports: 1}, []string{}},
		{"restarting in two reports", Service{Name: "web", State: "restarting", AlertsEnabled: true, RestartingReports: 2, RestartCount: 7},
			[]string{"service_restarting:critical"}},
		{"unhealthy in one report", Service{Name: "web", State: "running", AlertsEnabled: true, UnhealthyReports: 1}, []string{}},
		{"unhealthy in two reports", Service{Name: "web", State: "running", AlertsEnabled: true, UnhealthyReports: 2},
			[]string{"service_unhealthy:warning"}},
		{"restarting and unhealthy", Service{Name: "web", State: "running", AlertsEnabled: true, RestartingReports: 2, UnhealthyReports: 2},
			[]string{"service_restarting:critical", "service_unhealthy:warning"}},
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
	if got := summary(evaluate(m, t0)); !slices.Equal(got, []string{"machine_silent:critical"}) {
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

// serviceRows lists every notification as "type service status".
func (r *rig) serviceRows() []string {
	r.t.Helper()
	rows, err := r.db.Read.Query(`SELECT type, coalesce(service, '-'), status FROM notifications ORDER BY id`)
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
	if got, want := r.serviceRows(), []string{"service_down web sent", "service_down worker sent"}; !slices.Equal(got, want) {
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
	if got, want := r.serviceRows(), []string{"service_down web sent", "service_down worker sent", "machine_silent - sent"}; !slices.Equal(got, want) {
		t.Errorf("silent rows = %q, want %q", got, want)
	}

	// Back, with alerts off for web: web's alert resolves, worker's stays.
	web.AlertsEnabled = false
	r.tick(21*time.Minute, up(21*time.Minute, web, worker))
	if got, want := r.serviceRows(), []string{"service_down web resolved", "service_down worker sent", "machine_silent - resolved"}; !slices.Equal(got, want) {
		t.Errorf("rows after alerts off = %q, want %q", got, want)
	}

	// worker is removed: its alert resolves too.
	r.tick(22*time.Minute, up(22*time.Minute, web))
	if got, want := r.serviceRows(), []string{"service_down web resolved", "service_down worker resolved", "machine_silent - resolved"}; !slices.Equal(got, want) {
		t.Errorf("rows after removal = %q, want %q", got, want)
	}
	if want := "a: worker stopped, resolved"; r.sender.subjects[len(r.sender.subjects)-1] != want {
		t.Errorf("last email = %q, want %q", r.sender.subjects[len(r.sender.subjects)-1], want)
	}

	// Machine alerts still have no service.
	var n int
	if err := r.db.Read.QueryRow(`SELECT count(*) FROM notifications WHERE type = 'machine_silent' AND service IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Errorf("machine_silent rows without a service = %d, %v", n, err)
	}
}

func TestServiceAlertListsItsService(t *testing.T) {
	r := newRig(t)
	r.tick(0, up(0, Service{Name: "web", State: "running", AlertsEnabled: true, UnhealthyReports: 2}))
	ns, err := r.engine.store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n, ok := ns[key{TypeServiceUnhealthy, "a", "web"}]
	if !ok || n.Service == nil || *n.Service != "web" || n.Severity != SeverityWarning {
		t.Errorf("open = %+v", ns)
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
		{"running", "running", 0, []string{"service_down:critical"}},
		{"unreachable once", "unreachable", 1, []string{}},
		{"unreachable twice", "unreachable", 2, []string{"docker_unreachable:critical"}},
		{"before any report", "", 0, []string{"service_down:critical"}},
	} {
		m := up(0, down)
		m.Docker, m.DockerUnreachableReports = c.docker, c.reports
		if got := summary(evaluate(m, t0)); !slices.Equal(got, c.want) {
			t.Errorf("%s: findings = %v, want %v", c.name, got, c.want)
		}
	}
	m := silent()
	m.Docker, m.DockerUnreachableReports = "unreachable", 5
	if got := summary(evaluate(m, t0)); !slices.Equal(got, []string{"machine_silent:critical"}) {
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
	if got, want := r.serviceRows(), []string{"service_down web sent", "docker_unreachable - sent"}; !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
	if want := []string{"a: web stopped", "a: Docker unreachable"}; !slices.Equal(r.sender.subjects, want) {
		t.Errorf("emails = %q, want %q", r.sender.subjects, want)
	}
	if !strings.Contains(r.sender.texts[1], "Docker on a is not answering") {
		t.Errorf("text:\n%s", r.sender.texts[1])
	}

	// The machine goes silent: both stay as they are.
	m := unreachable(2*time.Minute, 2)
	r.tick(20*time.Minute, m)
	if got, want := r.serviceRows(), []string{"service_down web sent", "docker_unreachable - sent", "machine_silent - sent"}; !slices.Equal(got, want) {
		t.Errorf("silent rows = %q, want %q", got, want)
	}

	// Docker answers again, web runs: both resolve.
	web.State, web.DownReports = "running", 0
	r.tick(21*time.Minute, up(21*time.Minute, web))
	if got, want := r.serviceRows(), []string{"service_down web resolved", "docker_unreachable - resolved", "machine_silent - resolved"}; !slices.Equal(got, want) {
		t.Errorf("rows after Docker is back = %q, want %q", got, want)
	}
}
