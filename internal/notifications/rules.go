package notifications

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Notification types.
const (
	TypeMachineSilent = "machine_silent"
	TypeBackupFailed  = "backup_failed"
	TypeBackupStale   = "backup_stale"
	TypeDiskFull      = "disk_full"
)

// The thresholds of the rules.
const (
	silentAfter         = 10 * time.Minute
	staleWarningAfter   = 26 * time.Hour
	staleCriticalAfter  = 50 * time.Hour
	diskWarningPercent  = 90
	diskCriticalPercent = 95

	runFailed = "failed" // the status of a failed backup run
)

// Machine is what the rules need to know about one machine. The hub's wiring fills it from the
// machines and backups stores, so this domain imports neither.
type Machine struct {
	ID        int64
	Name      string
	CreatedAt time.Time

	LastSeen        *time.Time // when the newest usage reading was measured; nil before the first
	DiskUsedPercent float64    // of that reading

	HasBackupSettings       bool
	BackupSettingsCreatedAt time.Time
	LastBackup              *Run      // the newest run of type backup; nil before the first
	LastCompletedBackup     time.Time // when the newest completed backup started; zero before the first
}

// Run is one backup run.
type Run struct {
	Status string // pending, in_progress, completed or failed
	Error  string
	Time   time.Time
}

// finding is a condition that holds now.
type finding struct {
	typ, severity, content string
}

// evaluate returns the conditions that hold for m at now.
func evaluate(m Machine, now time.Time) []finding {
	var out []finding

	lastSeen := m.CreatedAt // a machine that never reported counts from when it was added
	if m.LastSeen != nil {
		lastSeen = *m.LastSeen
	}
	if now.Sub(lastSeen) >= silentAfter {
		content := m.Name + " has never reported to the hub."
		if m.LastSeen != nil {
			content = fmt.Sprintf("%s hasn't reported to the hub since %s (%s ago).", m.Name, formatTime(lastSeen), formatDuration(now.Sub(lastSeen)))
		}
		out = append(out, finding{TypeMachineSilent, SeverityCritical, content})
	}

	if m.HasBackupSettings {
		if r := m.LastBackup; r != nil && r.Status == runFailed {
			content := fmt.Sprintf("The last backup on %s failed at %s", m.Name, formatTime(r.Time))
			if r.Error != "" {
				content += ": " + r.Error
			} else {
				content += "."
			}
			out = append(out, finding{TypeBackupFailed, SeverityCritical, content})
		}

		since := m.LastCompletedBackup // never completed: count from when the settings were made
		if since.IsZero() {
			since = m.BackupSettingsCreatedAt
		}
		if age := now.Sub(since); age >= staleWarningAfter {
			severity := SeverityWarning
			if age >= staleCriticalAfter {
				severity = SeverityCritical
			}
			content := m.Name + " has never completed a backup."
			if !m.LastCompletedBackup.IsZero() {
				content = fmt.Sprintf("%s has had no completed backup since %s (%s ago).", m.Name, formatTime(since), formatDuration(age))
			}
			out = append(out, finding{TypeBackupStale, severity, content})
		}
	}

	if m.LastSeen != nil && m.DiskUsedPercent >= diskWarningPercent {
		severity := SeverityWarning
		if m.DiskUsedPercent >= diskCriticalPercent {
			severity = SeverityCritical
		}
		percent := strconv.FormatFloat(m.DiskUsedPercent, 'f', 1, 64)
		out = append(out, finding{TypeDiskFull, severity, fmt.Sprintf("The disk on %s is %s%% full.", m.Name, strings.TrimSuffix(percent, ".0"))})
	}
	return out
}

func severityRank(s string) int {
	switch s {
	case SeverityCritical:
		return 2
	case SeverityWarning:
		return 1
	}
	return 0
}

// formatTime is 2026-11-14 03:31 UTC.
func formatTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" }

// formatDuration shows the largest two units, such as "3 hours 12 minutes". A zero second unit is left out.
func formatDuration(d time.Duration) string {
	secs := int64(d.Round(time.Second) / time.Second)
	if secs < 0 {
		secs = 0
	}
	parts := []struct {
		unit string
		n    int64
	}{{"day", secs / 86400}, {"hour", secs % 86400 / 3600}, {"minute", secs % 3600 / 60}, {"second", secs % 60}}
	for i, p := range parts {
		if p.n == 0 && i < len(parts)-1 {
			continue
		}
		out := plural(p.n, p.unit)
		if i+1 < len(parts) && parts[i+1].n > 0 {
			out += " " + plural(parts[i+1].n, parts[i+1].unit)
		}
		return out
	}
	return "0 seconds"
}

func plural(n int64, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
