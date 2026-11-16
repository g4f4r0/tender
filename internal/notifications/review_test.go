package notifications

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

// inFlight delivers in the background with the sender held inside its send, runs during, then
// lets the send finish and waits for the delivery to be recorded.
func (r *rig) inFlight(at time.Duration, during func()) {
	r.t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	r.sender.hook = func() { close(entered); <-release }
	done := make(chan struct{})
	go func() {
		r.engine.deliver(context.Background(), func() time.Time { return t0.Add(at) }, true)
		close(done)
	}()
	<-entered
	r.sender.hook = nil
	during()
	close(release)
	<-done
}

// A delivery that finishes after its problem resolved leaves the superseded delivery and the
// notifications as they are.
func TestDeliveryAfterResolveChangesNothing(t *testing.T) {
	r := newRig(t)
	if err := r.engine.evaluate(t.Context(), t0, []Machine{silent()}); err != nil {
		t.Fatal(err)
	}
	r.inFlight(0, func() {
		if err := r.engine.evaluate(t.Context(), t0.Add(time.Minute), []Machine{quiet(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	})
	r.wantDeliveries("one machine.silent failed 0")
	r.want([]string{"machine.silent critical failed", "machine.silent.resolved critical failed"}, "a: not reporting")
}

// A delivery that fails after its channel was turned off doesn't bring its delivery back, nor
// count as the channel failing.
func TestDeliveryAfterChannelOffStaysOff(t *testing.T) {
	r := newRig(t)
	if err := r.engine.evaluate(t.Context(), t0, []Machine{silent()}); err != nil {
		t.Fatal(err)
	}
	r.sender.fail = true
	off := false
	r.inFlight(0, func() {
		if _, err := r.engine.store.PatchChannel(t.Context(), "one", ChannelPatch{Enabled: &off}); err != nil {
			t.Fatal(err)
		}
	})
	r.wantDeliveries("one machine.silent failed 0")
	r.want([]string{"machine.silent critical failed"})
	if n := r.count(`SELECT count(*) FROM notification_channels WHERE failing_since IS NOT NULL OR last_error IS NOT NULL`); n != 0 {
		t.Error("a try after the channel was turned off counts as it failing")
	}
}

// An escalation is its own notification: a drop before it went out doesn't cancel it, and a
// second rise adds none, so it is delivered once.
func TestEscalationIsDeliveredOnce(t *testing.T) {
	r := newRig(t)
	r.tick(0, staleAfter(0, 26))
	r.sender.fail = true
	r.tick(24*time.Hour, staleAfter(24*time.Hour, 50))
	r.sender.fail = false
	r.tick(25*time.Hour, staleAfter(25*time.Hour, 30)) // drops before the escalation went out
	r.tick(26*time.Hour, staleAfter(26*time.Hour, 50))
	r.want([]string{"backup.stale warning sent", "backup.stale.escalated critical sent"}, "a: no recent backup", "a: no recent backup, now critical")
}

// A test whose request ends mid-send is still recorded; a test is never owed by the delivery
// loop; and one the hub lost track of is failed after a minute.
func TestTestRecordedAfterCancel(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(t.Context())
	r.sender.hook = cancel
	if _, err := r.engine.store.SendTest(ctx, "one"); err != nil {
		t.Logf("SendTest: %v", err)
	}
	r.sender.hook = nil
	r.wantDeliveries("one notification.test delivered 1")
	r.want([]string{"notification.test info sent"}, "Tender test email")

	r.exec(`INSERT INTO notifications (id, event, severity, status, message, created_at, updated_at) VALUES (50, 'notification.test', 'info', 'pending', 'x', 1, 1)`)
	r.exec(`INSERT INTO notification_deliveries (notification_id, channel_id, status, attempts, created_at, updated_at) VALUES (50, 1, 'pending', 0, 1, 1)`)
	if ids, err := r.engine.store.channelsOwed(t.Context()); err != nil || len(ids) != 0 {
		t.Errorf("owed = %v, %v; want a test never owed", ids, err)
	}
	r.tick(0)
	if got := r.rows(); got[1] != "notification.test info failed" {
		t.Errorf("rows = %q, want the lost test failed", got)
	}
}

// failing counts only real failed tries on an enabled channel since its last success, or since
// it was turned on.
func TestFailingCountsRealFailuresOnly(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	failing := func() []string {
		t.Helper()
		f, err := r.engine.store.failing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for name := range f {
			out = append(out, name)
		}
		return out
	}
	// A real try that failed counts; a resolve that supersedes what is still owed adds nothing.
	r.sender.fail = true
	r.tick(0, silent())
	r.tick(30*time.Second, quiet(30*time.Second))
	if got := failing(); len(got) != 1 {
		t.Errorf("after a failed try: failing %q", got)
	}
	// Turning the channel off and on again starts afresh.
	off, on := false, true
	for _, e := range []*bool{&off, &on} {
		if _, err := r.engine.store.PatchChannel(ctx, "one", ChannelPatch{Enabled: e}); err != nil {
			t.Fatal(err)
		}
	}
	if got := failing(); len(got) != 0 {
		t.Errorf("after off and on: failing %q", got)
	}
	// A test while email is off says nothing about the channel; one that gets through ends a failing streak.
	email := r.engine.store.adapters
	r.engine.store.adapters = adapters(nil, testHub)
	if _, err := r.engine.store.SendTest(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if got := failing(); len(got) != 0 {
		t.Errorf("after an SMTP-off test: failing %q", got)
	}
	r.engine.store.adapters = email
	if _, err := r.engine.store.SendTest(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if got := failing(); len(got) != 1 {
		t.Errorf("after a failed test: failing %q", got)
	}
	r.sender.fail = false
	if _, err := r.engine.store.SendTest(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if got := failing(); len(got) != 0 {
		t.Errorf("after a test got through: failing %q", got)
	}
}

// The deliveries of tests and of problems resolved more than 30 days ago go; an open problem
// keeps its deliveries however old.
func TestOldDeliveriesPruned(t *testing.T) {
	r := newRig(t)
	r.tick(0, silent())
	r.tick(time.Minute, quiet(time.Minute))
	if _, err := r.engine.store.SendTest(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	r.exec(`UPDATE notifications SET created_at = 1 WHERE event = 'notification.test'`)
	r.exec(`INSERT INTO notifications (id, machine_id, event, severity, status, message, sent_at, created_at, updated_at)
		VALUES (60, 1, 'disk.full', 'warning', 'sent', 'x', 1, 1, 1)`)
	r.exec(`INSERT INTO notification_deliveries (notification_id, channel_id, status, attempts, created_at, updated_at) VALUES (60, 1, 'delivered', 1, 1, 1)`)
	m := quiet(31 * 24 * time.Hour)
	m.DiskUsedPercent = 91
	r.tick(31*24*time.Hour, m)
	if got := r.deliveries(); !slices.Equal(got, []string{"one disk.full delivered 1"}) {
		t.Errorf("deliveries left %q, want only the open problem's", got)
	}
	r.wantIndexes("notifications_problem", "notifications_created")
}

// Whether a notification is the latest of its problem is one search of the notifications_problem
// index on every key column.
func TestOpenProblemsUseTheIndex(t *testing.T) {
	r := newRig(t)
	rows, err := r.db.Read.Query(`EXPLAIN QUERY PLAN SELECT n.id FROM notifications n WHERE ` + latestOpen)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "SEARCH o USING INDEX notifications_problem (base=? AND <expr>=? AND <expr>=? AND <expr>=? AND <expr>=? AND id>?)") {
		t.Errorf("plan:\n%s", strings.Join(plan, "\n"))
	}
}

// A channel gets the events its patterns take, and a resolved or escalated one follows its base.
func TestEventsFilter(t *testing.T) {
	r := newRig(t)
	svc := newEndpoint(t, 200)
	c, err := r.engine.store.AddChannel(t.Context(), ChannelInput{Name: "svc", Type: TypeWebhook, Config: map[string]string{"url": svc.URL},
		MinSeverity: SeverityInfo, Events: []string{"service.*", "backup.failed"}})
	if err != nil || !slices.Equal(c.Events, []string{"service.*", "backup.failed"}) {
		t.Fatalf("add: %+v %v", c, err)
	}
	web := Service{Name: "web", State: "exited", ExitCode: ptr(int64(1)), AlertsEnabled: true, DownReports: 2}
	m := up(0, web)
	m.DiskUsedPercent = 91
	r.tick(0, m)
	web.State, web.DownReports = "running", 0
	r.tick(time.Minute, up(time.Minute, web))
	if got := svc.events(); !slices.Equal(got, []string{"service.down", "service.down.resolved"}) {
		t.Errorf("svc got %q, want the service's problem only", got)
	}
	for p, want := range map[string]bool{"*": true, "service.*": true, "service.down": true, "backup.*": false, "service.downs": false, "servic.*": false} {
		if got := matches([]string{p}, "service.down.resolved"); got != want {
			t.Errorf("%q takes service.down.resolved: %v, want %v", p, got, want)
		}
	}
}

// Every event the hub fires is registered, with what the docs and the emails need.
func TestRegistry(t *testing.T) {
	names := []string{}
	for _, e := range Events() {
		names = append(names, e.Name)
		if e.Title == "" || e.Tool == "" || e.Fires == "" || len(e.Severities) == 0 {
			t.Errorf("%+v", e)
		}
	}
	for _, want := range []string{EventMachineSilent, EventBackupFailed, EventBackupStale, EventDiskFull, EventServiceDown, EventServiceRestarting,
		EventServiceUnhealthy, EventServiceMemoryHigh, EventDockerUnreachable, EventChannelFailing, EventTest} {
		if !slices.Contains(names, want) {
			t.Errorf("%s is not registered", want)
		}
	}
	for _, bad := range []EventType{
		{Name: EventDiskFull, Severities: []string{SeverityWarning}, Title: "x", Tool: "x", Fires: "x"},
		{Name: "disk", Severities: []string{SeverityWarning}, Title: "x", Tool: "x", Fires: "x"},
		{Name: "disk.full.resolved", Severities: []string{SeverityWarning}, Title: "x", Tool: "x", Fires: "x"},
		{Name: "disk.resolved", Severities: []string{SeverityWarning}, Title: "x", Tool: "x", Fires: "x"},
		{Name: "Disk.Full", Severities: []string{SeverityWarning}, Title: "x", Tool: "x", Fires: "x"},
		{Name: "disk.other", Severities: []string{"loud"}, Title: "x", Tool: "x", Fires: "x"},
		{Name: "disk.other"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%+v registered", bad)
				}
			}()
			Register(bad)
		}()
	}
	// Another domain's finding fires its registered event, and resolves when it is gone.
	Register(EventType{Name: "logs.test_failing", Severities: []string{SeverityWarning}, Title: "logs failing", Tool: "list_machines", Fires: "x"})
	r := newRig(t)
	m := quiet(0)
	m.Findings = []Finding{{Event: "logs.test_failing", Severity: SeverityWarning, Message: "Logs fail."}, {Event: "logs.unknown", Severity: SeverityWarning}}
	r.tick(0, m)
	r.tick(time.Minute, quiet(time.Minute))
	r.want([]string{"logs.test_failing warning sent", "logs.test_failing.resolved warning sent"}, "a: logs failing", "a: logs failing, resolved")
}

// v0.2's alerts become notifications: a pending or sent one the notification it opened with, a
// resolved one that and its .resolved one, ids in time order. An alert v0.2 emailed is delivered
// to every recipient's channel, so its .resolved notification goes to them.
func TestV02AlertsMigrate(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	files := fstest.MapFS{}
	for _, name := range []string{"001_init.sql", "002_recipients.sql", "003_services.sql"} {
		b, err := fs.ReadFile(Migrations.Files, "migrations/"+name)
		if err != nil {
			t.Fatal(err)
		}
		files["migrations/"+name] = &fstest.MapFile{Data: b}
	}
	if _, err := d.Write.Exec(`CREATE TABLE machines (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, token_hash BLOB NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) STRICT; INSERT INTO machines VALUES (1, 'a', x'00', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.Migrate(ctx, db.Migrations{Domain: "notifications", Files: files, Dir: "migrations"}, server.Migrations); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec(`INSERT INTO notification_recipients (id, email, created_at, updated_at) VALUES (1, 'one@example.com', 1, 1);
		INSERT INTO notifications (id, machine_id, service, type, severity, status, content, escalated, created_at, updated_at) VALUES
		(1, 1, NULL, 'disk_full', 'warning', 'sent', 'The disk on a is 91% full.', 0, 10, 20),
		(2, 1, NULL, 'backup_failed', 'critical', 'resolved', 'The last backup failed.', 0, 5, 30),
		(3, 1, NULL, 'machine_silent', 'critical', 'pending', 'a is silent.', 0, 40, 40),
		(4, 1, 'web', 'service_down', 'critical', 'sent', 'web stopped.', 0, 50, 60)`); err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(ctx, Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Read.Query(`SELECT id, event, coalesce(service, '-'), status, coalesce(sent_at, 0), created_at, message FROM notifications ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id, sent, created int64
		var event, service, status, message string
		if err := rows.Scan(&id, &event, &service, &status, &sent, &created, &message); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Join([]string{itoa(id), event, service, status, itoa(sent), itoa(created), message}, " | "))
	}
	_ = rows.Close()
	want := []string{
		"1 | backup.failed | - | sent | 5 | 5 | The last backup failed.",
		"2 | disk.full | - | sent | 20 | 10 | The disk on a is 91% full.",
		"3 | backup.failed.resolved | - | sent | 30 | 30 | Resolved: The last backup failed.",
		"4 | machine.silent | - | pending | 0 | 40 | a is silent.",
		"5 | service.down | web | sent | 60 | 50 | web stopped.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("notifications:\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	sender := &fakeSender{}
	e := NewEngine(NewStore(d, testKey), sender, testHub, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	r := &rig{t: t, db: d, engine: e, sender: sender}
	r.wantDeliveries("one-example-com disk.full delivered 1", "one-example-com service.down delivered 1")
	open, err := e.store.List(ctx, 0, Filter{Open: true}, mustPage(t))
	if err != nil || len(open) != 3 || open[0].ID != 5 || open[1].ID != 4 || open[2].ID != 2 {
		t.Errorf("open: %+v %v", open, err)
	}
	// The disk is fine and the machine reports: the channel that got disk.full in v0.2 gets
	// disk.full.resolved; machine.silent, never emailed, resolves without an email.
	m := quiet(0)
	m.Services = []Service{{Name: "web", State: "exited", ExitCode: ptr(int64(1)), AlertsEnabled: true, DownReports: 2}}
	r.tick(0, m)
	if got := sender.subjects; !slices.Equal(got, []string{"a: disk almost full, resolved"}) {
		t.Errorf("emails %q", got)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func (r *rig) count(q string) int {
	r.t.Helper()
	var n int
	if err := r.db.Read.QueryRow(q).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) exec(q string) {
	r.t.Helper()
	if _, err := r.db.Write.Exec(q); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) wantIndexes(names ...string) {
	r.t.Helper()
	for _, n := range names {
		if r.count(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = '`+n+`'`) != 1 {
			r.t.Errorf("no index %s", n)
		}
	}
}
