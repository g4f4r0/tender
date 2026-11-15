package notifications

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/notifications/channels"
	"github.com/g4f4r0/tender/internal/server"
)

// fakeSender records the emails it is given, and fails while fail is set.
type fakeSender struct {
	subjects []string
	texts    []string
	to       [][]string
	calls    int
	fail     bool
	hook     func() // runs at the start of each send, while set
}

func (f *fakeSender) Send(_ context.Context, to []string, m Message) error {
	f.calls++
	if f.hook != nil {
		f.hook()
	}
	if f.fail {
		return errors.New("smtp down")
	}
	f.to = append(f.to, to)
	f.subjects = append(f.subjects, m.Subject)
	f.texts = append(f.texts, m.Text)
	return nil
}

// The tests' fake endpoints listen on loopback, which only tests may reach.
func TestMain(m *testing.M) {
	channels.AllowLocalForTests(true)
	os.Exit(m.Run())
}

type rig struct {
	t      *testing.T
	db     *db.DB
	engine *Engine
	sender *fakeSender
	logs   *bytes.Buffer
}

// newRig has machine a (id 1), the email channel one to one@example.com, and an engine that
// emails through a fake sender.
func newRig(t *testing.T) *rig {
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
		INSERT INTO machines VALUES (1, 'a', x'00', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background(), Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	addEmailChannel(t, d, "one", "one@example.com")
	logs := &bytes.Buffer{}
	e := NewEngine(NewStore(d, testKey), sender, testHub, slog.New(slog.NewTextHandler(logs, nil)))
	e.FailingAfter = 1000 * time.Hour // these tests are about one channel; delivery_test.go covers channel.failing
	return &rig{t: t, db: d, engine: e, sender: sender, logs: logs}
}

var (
	testKey = make([]byte, 32)
	testHub = Hub{Name: "hub1", URL: "https://hub1.example.com"}
)

// addEmailChannel adds an email channel that gets every notification.
func addEmailChannel(t *testing.T, d *db.DB, name, to string) {
	t.Helper()
	_, err := d.Write.Exec(`INSERT INTO notification_channels (name, type, config, enabled, min_severity, created_at, updated_at)
		VALUES (?, 'email', json_object('to', ?), 1, 'info', 1, 1)`, name, to)
	if err != nil {
		t.Fatal(err)
	}
}

// tick runs the rules at t0 plus after.
func (r *rig) tick(after time.Duration, ms ...Machine) {
	r.t.Helper()
	if err := r.engine.Tick(context.Background(), t0.Add(after), ms); err != nil {
		r.t.Fatal(err)
	}
}

// rows lists every notification as "event severity status".
func (r *rig) rows() []string {
	r.t.Helper()
	rows, err := r.db.Read.Query(`SELECT event, severity, status FROM notifications ORDER BY id`)
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

func (r *rig) want(rows []string, subjects ...string) {
	r.t.Helper()
	if got := r.rows(); !slices.Equal(got, rows) {
		r.t.Errorf("rows = %q, want %q", got, rows)
	}
	if subjects == nil {
		subjects = []string{}
	}
	if got := append([]string{}, r.sender.subjects...); !slices.Equal(got, subjects) {
		r.t.Errorf("emails = %q, want %q", got, subjects)
	}
}

// silent is machine a, added at t0 minus 1 hour, which never reports; quiet is the same machine reporting at t0 plus after.
func silent() Machine { return Machine{ID: 1, Name: "a", CreatedAt: t0.Add(-time.Hour)} }

func quiet(after time.Duration) Machine {
	m := silent()
	m.LastSeen = ptr(t0.Add(after))
	return m
}

func TestOpenSendResolve(t *testing.T) {
	r := newRig(t)
	r.tick(0, silent())
	r.want([]string{"machine.silent critical sent"}, "a: not reporting")
	if want := []string{"one@example.com"}; !slices.Equal(r.sender.to[0], want) {
		t.Errorf("the opened email went to %q, want the channel's address %q", r.sender.to[0], want)
	}
	if !strings.Contains(r.sender.texts[0], "a has never reported to the hub.") || !strings.Contains(r.sender.texts[0], "on hub1.") {
		t.Errorf("opened text:\n%s", r.sender.texts[0])
	}

	// Still silent a minute later: no second row, no second email.
	r.tick(time.Minute, silent())
	r.want([]string{"machine.silent critical sent"}, "a: not reporting")

	// A reading arrives: the problem resolves with a notification of its own, emailed.
	r.tick(5*time.Minute, quiet(5*time.Minute))
	r.want([]string{"machine.silent critical sent", "machine.silent.resolved critical sent"}, "a: not reporting", "a: not reporting, resolved")
	if !strings.Contains(r.sender.texts[1], "resolved as of 2026-11-14 03:35 UTC.") {
		t.Errorf("resolved text:\n%s", r.sender.texts[1])
	}

	// Silent again: a new problem, nothing reopened.
	r.tick(time.Hour, silent())
	r.want([]string{"machine.silent critical sent", "machine.silent.resolved critical sent", "machine.silent critical sent"},
		"a: not reporting", "a: not reporting, resolved", "a: not reporting")
}

func TestOneOpenNotificationPerTypeAndMachine(t *testing.T) {
	r := newRig(t)
	r.sender.fail = true
	for i := range 5 {
		r.tick(time.Duration(i)*time.Minute, silent())
	}
	r.want([]string{"machine.silent critical pending"})
}

func TestResolvesWithoutEmailWhenNeverSent(t *testing.T) {
	r := newRig(t)
	r.sender.fail = true
	r.tick(0, silent())
	r.sender.fail = false
	r.tick(time.Minute, quiet(time.Minute)) // clears before the pending email went out
	r.want([]string{"machine.silent critical failed", "machine.silent.resolved critical failed"})
}

func TestFailedSendIsRetriedWithBackoff(t *testing.T) {
	r := newRig(t)
	r.sender.fail = true
	// The waits after each failure are 1, 2, 5, 10 and 30 minutes.
	var attempts []int
	for i := range 40 {
		before := r.sender.calls
		r.tick(time.Duration(i)*time.Minute, silent())
		if r.sender.calls > before {
			attempts = append(attempts, i)
		}
	}
	if want := []int{0, 1, 3, 8, 18}; !slices.Equal(attempts, want) {
		t.Errorf("attempts at minutes %v, want %v", attempts, want)
	}
	r.want([]string{"machine.silent critical pending"})

	// Not due yet at 45 minutes; the next attempt, at 48, works: the row becomes sent.
	r.sender.fail = false
	r.tick(45*time.Minute, silent())
	r.want([]string{"machine.silent critical pending"})
	r.tick(48*time.Minute, silent())
	r.want([]string{"machine.silent critical sent"}, "a: not reporting")
}

func TestFailedResolvedEmailIsRetried(t *testing.T) {
	r := newRig(t)
	r.tick(0, silent())
	r.sender.fail = true
	r.tick(time.Minute, quiet(time.Minute))
	r.want([]string{"machine.silent critical sent", "machine.silent.resolved critical pending"}, "a: not reporting") // retried below
	r.sender.fail = false
	r.tick(2*time.Minute, quiet(2*time.Minute))
	r.want([]string{"machine.silent critical sent", "machine.silent.resolved critical sent"}, "a: not reporting", "a: not reporting, resolved")
}

// staleAfter is machine a with backup settings, whose last completed backup was hours before t0 plus after.
func staleAfter(after, hours time.Duration) Machine {
	m := quiet(after)
	m.HasBackupSettings, m.BackupSettingsCreatedAt = true, t0.Add(-1000*time.Hour)
	m.LastCompletedBackup = t0.Add(after - hours*time.Hour)
	return m
}

func TestEscalationAndDrop(t *testing.T) {
	r := newRig(t)
	r.tick(0, staleAfter(0, 26))
	r.want([]string{"backup.stale warning sent"}, "a: no recent backup")

	// 50 hours without a backup: an escalated notification goes out.
	r.tick(24*time.Hour, staleAfter(24*time.Hour, 50))
	esc := []string{"backup.stale warning sent", "backup.stale.escalated critical sent"}
	r.want(esc, "a: no recent backup", "a: no recent backup, now critical")
	if !strings.Contains(r.sender.texts[1], "This got worse and is now critical, as of 2026-11-15 03:30 UTC.") ||
		!strings.Contains(r.sender.texts[1], "(2 days 2 hours ago)") {
		t.Errorf("escalated text:\n%s", r.sender.texts[1])
	}

	// The severity falls (a backup completed 30 hours ago, which can't happen, but the rule must cope): nothing.
	r.tick(25*time.Hour, staleAfter(25*time.Hour, 30))
	r.want(esc, "a: no recent backup", "a: no recent backup, now critical")

	// It rises again, and falls and rises once more: one escalation per problem, so none.
	r.tick(25*time.Hour+time.Minute, staleAfter(25*time.Hour+time.Minute, 50))
	r.tick(25*time.Hour+2*time.Minute, staleAfter(25*time.Hour+2*time.Minute, 30))
	r.tick(25*time.Hour+3*time.Minute, staleAfter(25*time.Hour+3*time.Minute, 50))
	r.want(esc, "a: no recent backup", "a: no recent backup, now critical")

	// Resolves, at the severity it last went out with.
	r.tick(26*time.Hour, staleAfter(26*time.Hour, 1))
	r.want(append(esc, "backup.stale.resolved critical sent"), "a: no recent backup", "a: no recent backup, now critical", "a: no recent backup, resolved")
}

func TestEscalationBeforeDelivery(t *testing.T) {
	r := newRig(t)
	r.sender.fail = true
	r.tick(0, staleAfter(0, 26))
	r.tick(2*time.Hour, staleAfter(2*time.Hour, 50)) // escalates while nothing was ever delivered
	r.want([]string{"backup.stale warning pending", "backup.stale.escalated critical pending"})
	r.sender.fail = false
	r.tick(2*time.Hour+3*time.Minute, staleAfter(2*time.Hour+3*time.Minute, 50)) // after the 2 minute wait: both, in order
	r.want([]string{"backup.stale warning sent", "backup.stale.escalated critical sent"}, "a: no recent backup", "a: no recent backup, now critical")
}

func TestBackupFailedAndDisk(t *testing.T) {
	r := newRig(t)
	m := quiet(0)
	m.HasBackupSettings, m.BackupSettingsCreatedAt, m.LastCompletedBackup = true, t0, t0
	m.LastBackup = &Run{Status: "failed", Error: "boom", Time: t0}
	m.DiskUsedPercent = 91
	r.tick(0, m)
	r.want([]string{"backup.failed critical sent", "disk.full warning sent"}, "a: backup failed", "a: disk almost full")

	m.LastBackup = &Run{Status: "in_progress", Time: t0}
	m.DiskUsedPercent = 50
	r.tick(time.Minute, m)
	r.want([]string{"backup.failed critical sent", "disk.full warning sent", "backup.failed.resolved critical sent", "disk.full.resolved warning sent"},
		"a: backup failed", "a: disk almost full", "a: backup failed, resolved", "a: disk almost full, resolved")
}

func TestEmailOff(t *testing.T) {
	r := newRig(t)
	r.engine.sender = nil
	r.tick(0, silent())
	r.tick(time.Minute, silent())
	r.want([]string{"machine.silent critical pending"})
	r.tick(2*time.Minute, quiet(2*time.Minute))
	r.want([]string{"machine.silent critical failed", "machine.silent.resolved critical failed"})
}

const nobody = "nobody gets notifications"

func TestEmailOffSaysSoOnce(t *testing.T) {
	r := newRig(t)
	r.engine.sender = nil
	for i := range 3 {
		r.tick(time.Duration(i)*time.Minute, silent())
	}
	if n := strings.Count(r.logs.String(), nobody); n != 1 {
		t.Errorf("the hub said nobody gets email %d times, want once:\n%s", n, r.logs)
	}
}

func TestNoChannels(t *testing.T) {
	r := newRig(t)
	if _, err := r.db.Write.Exec(`DELETE FROM notification_channels`); err != nil {
		t.Fatal(err)
	}
	// Alerts are recorded and stay pending; nothing is sent; the log says so once.
	for i := range 3 {
		r.tick(time.Duration(i)*time.Minute, silent())
	}
	r.want([]string{"machine.silent critical pending"})
	if r.sender.calls != 0 {
		t.Errorf("sent %d emails to nobody", r.sender.calls)
	}
	if n := strings.Count(r.logs.String(), nobody); n != 1 {
		t.Errorf("the hub said nobody gets email %d times, want once:\n%s", n, r.logs)
	}

	// The first channel gets the email that was owed.
	addEmailChannel(t, r.db, "late", "late@example.com")
	r.tick(3*time.Minute, silent())
	r.want([]string{"machine.silent critical sent"}, "a: not reporting")
	if want := []string{"late@example.com"}; !slices.Equal(r.sender.to[0], want) {
		t.Errorf("sent to %q, want %q", r.sender.to[0], want)
	}

	// With the last channel gone, the problem resolves without an email, and the log speaks again.
	if _, err := r.db.Write.Exec(`DELETE FROM notification_channels`); err != nil {
		t.Fatal(err)
	}
	r.tick(4*time.Minute, quiet(4*time.Minute))
	r.want([]string{"machine.silent critical sent", "machine.silent.resolved critical failed"}, "a: not reporting")
	if n := strings.Count(r.logs.String(), nobody); n != 2 {
		t.Errorf("the hub said nobody gets email %d times, want 2", n)
	}
}

func TestIgnoresOtherRows(t *testing.T) {
	r := newRig(t)
	_, err := r.db.Write.Exec(`INSERT INTO notifications (machine_id, event, severity, status, message, sent_at, created_at, updated_at) VALUES
		(NULL, 'hub.restarted', 'info', 'sent', 'x', 1, 1, 1), (1, 'other.thing', 'info', 'pending', 'x', NULL, 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	r.tick(0, quiet(0))
	r.want([]string{"hub.restarted info sent", "other.thing info pending"})
}

func TestBackoff(t *testing.T) {
	var got []string
	for n := 0; n <= 7; n++ {
		got = append(got, fmt.Sprint(backoff(n)))
	}
	if want := []string{"0s", "1m0s", "2m0s", "5m0s", "10m0s", "30m0s", "30m0s", "30m0s"}; !slices.Equal(got, want) {
		t.Errorf("backoff = %v, want %v", got, want)
	}
}
