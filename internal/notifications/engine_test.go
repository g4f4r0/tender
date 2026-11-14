package notifications

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

// fakeSender records the emails it is given, and fails while fail is set.
type fakeSender struct {
	subjects []string
	texts    []string
	calls    int
	fail     bool
}

func (f *fakeSender) Send(_ context.Context, m Message) error {
	f.calls++
	if f.fail {
		return errors.New("smtp down")
	}
	f.subjects = append(f.subjects, m.Subject)
	f.texts = append(f.texts, m.Text)
	return nil
}

type rig struct {
	t      *testing.T
	db     *db.DB
	engine *Engine
	sender *fakeSender
}

// newRig has machine a (id 1) and an engine that emails through a fake sender.
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
	e := NewEngine(NewStore(d), sender, "hub1", slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &rig{t: t, db: d, engine: e, sender: sender}
}

// tick runs the rules at t0 plus after.
func (r *rig) tick(after time.Duration, ms ...Machine) {
	r.t.Helper()
	if err := r.engine.Tick(context.Background(), t0.Add(after), ms); err != nil {
		r.t.Fatal(err)
	}
}

// rows lists every notification as "type severity status".
func (r *rig) rows() []string {
	r.t.Helper()
	rows, err := r.db.Read.Query(`SELECT type, severity, status FROM notifications ORDER BY id`)
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
	r.want([]string{"machine_silent critical sent"}, "a: not reporting")
	if !strings.Contains(r.sender.texts[0], "a has never reported to the hub.") || !strings.Contains(r.sender.texts[0], "on hub1.") {
		t.Errorf("opened text:\n%s", r.sender.texts[0])
	}

	// Still silent a minute later: no second row, no second email.
	r.tick(time.Minute, silent())
	r.want([]string{"machine_silent critical sent"}, "a: not reporting")

	// A reading arrives: the resolved email goes out, then the row resolves.
	r.tick(5*time.Minute, quiet(5*time.Minute))
	r.want([]string{"machine_silent critical resolved"}, "a: not reporting", "a: not reporting, resolved")
	if !strings.Contains(r.sender.texts[1], "resolved as of 2026-11-14 03:35 UTC. It was open for 5 minutes.") {
		t.Errorf("resolved text:\n%s", r.sender.texts[1])
	}

	// Silent again: a new notification, not the old row reopened.
	r.tick(time.Hour, silent())
	r.want([]string{"machine_silent critical resolved", "machine_silent critical sent"},
		"a: not reporting", "a: not reporting, resolved", "a: not reporting")
}

func TestOneOpenNotificationPerTypeAndMachine(t *testing.T) {
	r := newRig(t)
	r.sender.fail = true
	for i := range 5 {
		r.tick(time.Duration(i)*time.Minute, silent())
	}
	r.want([]string{"machine_silent critical pending"})
}

func TestResolvesWithoutEmailWhenNeverSent(t *testing.T) {
	r := newRig(t)
	r.sender.fail = true
	r.tick(0, silent())
	r.sender.fail = false
	r.tick(time.Minute, quiet(time.Minute)) // clears before the pending email went out
	r.want([]string{"machine_silent critical resolved"})
}

func TestFailedSendIsRetriedWithBackoff(t *testing.T) {
	r := newRig(t)
	r.sender.fail = true
	// The first failure retries on the next tick, then the waits are 2, 4 and 8 minutes, then 10.
	var attempts []int
	for i := range 40 {
		before := r.sender.calls
		r.tick(time.Duration(i)*time.Minute, silent())
		if r.sender.calls > before {
			attempts = append(attempts, i)
		}
	}
	if want := []int{0, 1, 3, 7, 15, 25, 35}; !slices.Equal(attempts, want) {
		t.Errorf("attempts at minutes %v, want %v", attempts, want)
	}
	r.want([]string{"machine_silent critical pending"})

	// The next attempt that works sends the opened email and the row becomes sent.
	r.sender.fail = false
	r.tick(45*time.Minute, silent())
	r.want([]string{"machine_silent critical sent"}, "a: not reporting")
}

func TestFailedResolvedEmailIsRetried(t *testing.T) {
	r := newRig(t)
	r.tick(0, silent())
	r.sender.fail = true
	r.tick(time.Minute, quiet(time.Minute))
	r.want([]string{"machine_silent critical sent"}, "a: not reporting") // still open, retried below
	r.sender.fail = false
	r.tick(2*time.Minute, quiet(2*time.Minute))
	r.want([]string{"machine_silent critical resolved"}, "a: not reporting", "a: not reporting, resolved")
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
	r.want([]string{"backup_stale warning sent"}, "a: no recent backup")

	// 50 hours without a backup: severity and content change and an escalated email goes out.
	r.tick(24*time.Hour, staleAfter(24*time.Hour, 50))
	r.want([]string{"backup_stale critical sent"}, "a: no recent backup", "a: no recent backup, now critical")
	if !strings.Contains(r.sender.texts[1], "This was a warning and is now critical. It has been open since 2026-11-14 03:30 UTC.") ||
		!strings.Contains(r.sender.texts[1], "(2 days 2 hours ago)") {
		t.Errorf("escalated text:\n%s", r.sender.texts[1])
	}

	// The severity falls (a backup completed 30 hours ago, which can't happen, but the rule must cope): no email.
	r.tick(25*time.Hour, staleAfter(25*time.Hour, 30))
	r.want([]string{"backup_stale warning sent"}, "a: no recent backup", "a: no recent backup, now critical")

	// Resolves.
	r.tick(26*time.Hour, staleAfter(26*time.Hour, 1))
	r.want([]string{"backup_stale warning resolved"}, "a: no recent backup", "a: no recent backup, now critical", "a: no recent backup, resolved")
}

func TestEscalationBeforeEmailStaysOpened(t *testing.T) {
	r := newRig(t)
	r.sender.fail = true
	r.tick(0, staleAfter(0, 26))
	r.tick(24*time.Hour, staleAfter(24*time.Hour, 50)) // escalates while nothing was ever sent
	r.want([]string{"backup_stale critical pending"})
	r.sender.fail = false
	r.tick(24*time.Hour+3*time.Minute, staleAfter(24*time.Hour+3*time.Minute, 50)) // after the 2 minute wait
	r.want([]string{"backup_stale critical sent"}, "a: no recent backup")
}

func TestDropBeforeEscalatedEmailSettlesAsSent(t *testing.T) {
	r := newRig(t)
	r.tick(0, staleAfter(0, 26))
	r.sender.fail = true
	r.tick(24*time.Hour, staleAfter(24*time.Hour, 50)) // escalated email fails: pending
	r.want([]string{"backup_stale critical pending"}, "a: no recent backup")
	r.sender.fail = false
	r.tick(25*time.Hour, staleAfter(25*time.Hour, 30)) // drops to warning: nothing is owed
	r.want([]string{"backup_stale warning sent"}, "a: no recent backup")
}

func TestBackupFailedAndDisk(t *testing.T) {
	r := newRig(t)
	m := quiet(0)
	m.HasBackupSettings, m.BackupSettingsCreatedAt, m.LastCompletedBackup = true, t0, t0
	m.LastBackup = &Run{Status: "failed", Error: "boom", Time: t0}
	m.DiskUsedPercent = 91
	r.tick(0, m)
	r.want([]string{"backup_failed critical sent", "disk_full warning sent"}, "a: backup failed", "a: disk almost full")

	m.LastBackup = &Run{Status: "in_progress", Time: t0}
	m.DiskUsedPercent = 50
	r.tick(time.Minute, m)
	r.want([]string{"backup_failed critical resolved", "disk_full warning resolved"},
		"a: backup failed", "a: disk almost full", "a: backup failed, resolved", "a: disk almost full, resolved")
}

func TestEmailOff(t *testing.T) {
	r := newRig(t)
	r.engine.sender = nil
	r.tick(0, silent())
	r.tick(time.Minute, silent())
	r.want([]string{"machine_silent critical pending"})
	r.tick(2*time.Minute, quiet(2*time.Minute))
	r.want([]string{"machine_silent critical resolved"})
}

func TestIgnoresOtherRows(t *testing.T) {
	r := newRig(t)
	_, err := r.db.Write.Exec(`INSERT INTO notifications (machine_id, type, severity, status, content, created_at, updated_at) VALUES
		(NULL, 'hub_restarted', 'info', 'sent', 'x', 1, 1), (1, 'other', 'info', 'sent', 'x', 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	r.tick(0, quiet(0))
	r.want([]string{"hub_restarted info sent", "other info sent"})
}

func TestBackoff(t *testing.T) {
	var got []string
	for n := 1; n <= 7; n++ {
		got = append(got, fmt.Sprint(backoff(n)))
	}
	if want := []string{"0s", "2m0s", "4m0s", "8m0s", "10m0s", "10m0s", "10m0s"}; !slices.Equal(got, want) {
		t.Errorf("backoff = %v, want %v", got, want)
	}
}
