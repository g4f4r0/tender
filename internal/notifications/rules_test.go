package notifications

import (
	"slices"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 14, 3, 30, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// summary is "type:severity" of each finding, in order.
func summary(fs []finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.typ+":"+f.severity)
	}
	return out
}

func TestEvaluate(t *testing.T) {
	seen := func(ago time.Duration) *time.Time { return ptr(t0.Add(-ago)) }
	withBackups := func(m Machine) Machine {
		m.HasBackupSettings, m.BackupSettingsCreatedAt = true, t0.Add(-100*time.Hour)
		return m
	}
	cases := []struct {
		name string
		m    Machine
		want []string
	}{
		{"never reported, added 9 minutes ago", Machine{CreatedAt: t0.Add(-9 * time.Minute)}, []string{}},
		{"never reported, added 10 minutes ago", Machine{CreatedAt: t0.Add(-10 * time.Minute)}, []string{"machine_silent:critical"}},
		{"reported 9 minutes ago", Machine{CreatedAt: t0.Add(-time.Hour), LastSeen: seen(9 * time.Minute)}, []string{}},
		{"reported 10 minutes ago", Machine{CreatedAt: t0.Add(-time.Hour), LastSeen: seen(10 * time.Minute)}, []string{"machine_silent:critical"}},

		{"disk 89.9", Machine{LastSeen: seen(0), DiskUsedPercent: 89.9}, []string{}},
		{"disk 90", Machine{LastSeen: seen(0), DiskUsedPercent: 90}, []string{"disk_full:warning"}},
		{"disk 94.99", Machine{LastSeen: seen(0), DiskUsedPercent: 94.99}, []string{"disk_full:warning"}},
		{"disk 95", Machine{LastSeen: seen(0), DiskUsedPercent: 95}, []string{"disk_full:critical"}},
		{"disk without a reading", Machine{CreatedAt: t0, DiskUsedPercent: 99}, []string{}},

		{"failed run", withBackups(Machine{LastSeen: seen(0), LastBackup: &Run{Status: "failed", Error: "boom", Time: t0}, LastCompletedBackup: t0}), []string{"backup_failed:critical"}},
		{"failed run without settings", Machine{LastSeen: seen(0), LastBackup: &Run{Status: "failed", Time: t0}}, []string{}},
		{"completed run", withBackups(Machine{LastSeen: seen(0), LastBackup: &Run{Status: "completed", Time: t0}, LastCompletedBackup: t0}), []string{}},
		{"run in progress", withBackups(Machine{LastSeen: seen(0), LastBackup: &Run{Status: "in_progress", Time: t0}, LastCompletedBackup: t0}), []string{}},

		{"completed 25h59 ago", withBackups(Machine{LastSeen: seen(0), LastCompletedBackup: t0.Add(-26*time.Hour + time.Minute)}), []string{}},
		{"completed 26h ago", withBackups(Machine{LastSeen: seen(0), LastCompletedBackup: t0.Add(-26 * time.Hour)}), []string{"backup_stale:warning"}},
		{"completed 49h59 ago", withBackups(Machine{LastSeen: seen(0), LastCompletedBackup: t0.Add(-50*time.Hour + time.Minute)}), []string{"backup_stale:warning"}},
		{"completed 50h ago", withBackups(Machine{LastSeen: seen(0), LastCompletedBackup: t0.Add(-50 * time.Hour)}), []string{"backup_stale:critical"}},
		{"never completed, settings 100h old", withBackups(Machine{LastSeen: seen(0)}), []string{"backup_stale:critical"}},
		{"never completed, settings 1h old", Machine{LastSeen: seen(0), HasBackupSettings: true, BackupSettingsCreatedAt: t0.Add(-time.Hour)}, []string{}},
		{"stale without settings", Machine{LastSeen: seen(0), LastCompletedBackup: t0.Add(-60 * time.Hour)}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summary(evaluate(c.m, t0)); !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestEvaluateContent(t *testing.T) {
	m := Machine{
		Name: "code", CreatedAt: t0.Add(-100 * time.Hour), LastSeen: ptr(t0.Add(-(3*time.Hour + 12*time.Minute))), DiskUsedPercent: 94.57,
		HasBackupSettings: true, BackupSettingsCreatedAt: t0.Add(-100 * time.Hour),
		LastBackup:          &Run{Status: "failed", Error: "repository is locked", Time: time.Date(2026, 11, 14, 2, 5, 0, 0, time.UTC)},
		LastCompletedBackup: t0.Add(-(27*time.Hour + 30*time.Minute)),
	}
	got := map[string]string{}
	for _, f := range evaluate(m, t0) {
		got[f.typ] = f.content
	}
	want := map[string]string{
		TypeMachineSilent: "code hasn't reported to the hub since 2026-11-14 00:18 UTC (3 hours 12 minutes ago).",
		TypeBackupFailed:  "The last backup on code failed at 2026-11-14 02:05 UTC: repository is locked",
		TypeBackupStale:   "code has had no completed backup since 2026-11-13 00:00 UTC (1 day 3 hours ago).",
		TypeDiskFull:      "The disk on code is 94.6% full.",
	}
	for typ, w := range want {
		if got[typ] != w {
			t.Errorf("%s: %q, want %q", typ, got[typ], w)
		}
	}

	never := Machine{Name: "code", CreatedAt: t0.Add(-time.Hour), HasBackupSettings: true, BackupSettingsCreatedAt: t0.Add(-30 * time.Hour), DiskUsedPercent: 90}
	for _, f := range evaluate(never, t0) {
		if f.typ == TypeBackupStale && f.content != "code has never completed a backup." {
			t.Errorf("never completed: %q", f.content)
		}
		if f.typ == TypeMachineSilent && f.content != "code has never reported to the hub." {
			t.Errorf("never reported: %q", f.content)
		}
	}
	if len(evaluate(never, t0)) != 2 {
		t.Errorf("findings = %v, want machine_silent and backup_stale", evaluate(never, t0))
	}
	if f := evaluate(Machine{Name: "code", LastSeen: ptr(t0), DiskUsedPercent: 90, CreatedAt: t0}, t0); len(f) != 1 || !strings.HasSuffix(f[0].content, "is 90% full.") {
		t.Errorf("whole percent: %v", f)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                              "0 seconds",
		time.Second:                    "1 second",
		45 * time.Second:               "45 seconds",
		time.Minute:                    "1 minute",
		5*time.Minute + 30*time.Second: "5 minutes 30 seconds",
		3*time.Hour + 12*time.Minute:   "3 hours 12 minutes",
		3 * time.Hour:                  "3 hours",
		3*time.Hour + 20*time.Second:   "3 hours",
		26 * time.Hour:                 "1 day 2 hours",
		48 * time.Hour:                 "2 days",
		50*time.Hour + 59*time.Minute:  "2 days 2 hours",
		-time.Minute:                   "0 seconds",
		24*time.Hour + 30*time.Minute:  "1 day",
		9*time.Minute + 59*time.Second + 600*time.Millisecond: "10 minutes",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
	if got := formatTime(time.Date(2026, 11, 14, 5, 31, 0, 0, time.FixedZone("x", 7200))); got != "2026-11-14 03:31 UTC" {
		t.Errorf("formatTime = %q", got)
	}
}
