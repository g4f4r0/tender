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

	TypeServiceDown       = "service_down"
	TypeServiceRestarting = "service_restarting"
	TypeServiceUnhealthy  = "service_unhealthy"
	TypeDockerUnreachable = "docker_unreachable"
)

// The thresholds of the rules.
const (
	silentAfter         = 10 * time.Minute
	staleWarningAfter   = 26 * time.Hour
	staleCriticalAfter  = 50 * time.Hour
	diskWarningPercent  = 90
	diskCriticalPercent = 95

	runFailed = "failed" // the status of a failed backup run

	// serviceReports is how many reports in a row must find a service down, restarting or
	// unhealthy before it alerts, so a quick restart or a deploy doesn't.
	serviceReports = 2
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
	LastBackup              *Run      // the newest finished (completed or failed) backup run; nil before the first
	LastCompletedBackup     time.Time // when the newest completed backup started; zero before the first

	Services []Service // the Docker services its daemon reports

	// What the daemon's latest report said about the machine's Docker engine: running,
	// unreachable, or empty before any report, and how many reports in a row said unreachable.
	Docker                   string
	DockerUnreachableReports int
}

// The Docker states a daemon reports.
const (
	dockerUnreachable = "unreachable"

	exitedState = "exited"
	deadState   = "dead"
)

// frozen reports whether an open alert about m stays as it is, neither resolving nor changing:
// a silent machine's Docker and service alerts, and service alerts while its Docker is
// unreachable, are about a state nobody can see.
func (m Machine) frozen(n Notification, silent bool) bool {
	switch {
	case n.Service != nil:
		return silent || m.Docker == dockerUnreachable
	case n.Type == TypeDockerUnreachable:
		return silent
	}
	return false
}

// Service is what the rules need to know about one Docker service. The hub's wiring fills it
// from the services store.
type Service struct {
	Name            string
	State           string // Docker's: created, running, paused, restarting, exited, removing or dead
	ExitCode        *int64
	RestartCount    int64
	RestartPolicy   string // no, always, unless-stopped or on-failure
	AlertsEnabled   bool
	StoppedByTender bool // a Tender stop left it stopped

	// How many reports in a row found it down (exited or dead), restarting (in the restarting
	// state, or with a restart count that grew) and unhealthy.
	DownReports       int
	RestartingReports int
	UnhealthyReports  int
}

// Run is one backup run.
type Run struct {
	Status string // pending, in_progress, completed or failed
	Error  string
	Time   time.Time
}

// finding is a condition that holds now. service is set for an alert about a Docker service.
type finding struct {
	typ, severity, content string
	service                string
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
		out = append(out, finding{TypeMachineSilent, SeverityCritical, content, ""})
	}

	if m.HasBackupSettings {
		if r := m.LastBackup; r != nil && r.Status == runFailed {
			content := fmt.Sprintf("The last backup on %s failed at %s", m.Name, formatTime(r.Time))
			if r.Error != "" {
				content += ": " + r.Error
			} else {
				content += "."
			}
			out = append(out, finding{TypeBackupFailed, SeverityCritical, content, ""})
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
			out = append(out, finding{TypeBackupStale, severity, content, ""})
		}
	}

	if m.LastSeen != nil && m.DiskUsedPercent >= diskWarningPercent {
		severity := SeverityWarning
		if m.DiskUsedPercent >= diskCriticalPercent {
			severity = SeverityCritical
		}
		percent := strconv.FormatFloat(m.DiskUsedPercent, 'f', 1, 64)
		out = append(out, finding{TypeDiskFull, severity, fmt.Sprintf("The disk on %s is %s%% full.", m.Name, strings.TrimSuffix(percent, ".0")), ""})
	}

	// A silent machine already alerts; its services, whose state is as old as its last report, don't.
	if len(out) > 0 && out[0].typ == TypeMachineSilent {
		return out
	}
	if m.DockerUnreachableReports >= serviceReports {
		out = append(out, finding{TypeDockerUnreachable, SeverityCritical,
			fmt.Sprintf("Docker on %s is not answering: its socket refuses connections or doesn't answer. Its services' states are unknown until it is back.", m.Name), ""})
	}
	if m.Docker == dockerUnreachable { // its services' states are unknown
		return out
	}
	for _, s := range m.Services {
		if s.AlertsEnabled {
			out = append(out, evaluateService(m.Name, s)...)
		}
	}
	return out
}

// shouldRun reports whether a stopped service was meant to keep running: it failed, or Docker
// would restart it. One that exited 0 with restart policy no or on-failure finished on purpose.
func shouldRun(s Service) bool {
	failed := s.ExitCode == nil || *s.ExitCode != 0
	return failed || s.RestartPolicy == "always" || s.RestartPolicy == "unless-stopped"
}

// evaluateService returns the conditions that hold for one service of the machine called machine.
func evaluateService(machine string, s Service) []finding {
	var out []finding
	if s.DownReports >= serviceReports && !s.StoppedByTender && (s.State == exitedState || s.State == deadState) && shouldRun(s) {
		content := fmt.Sprintf("The service %s on %s stopped.", s.Name, machine)
		if s.State == deadState {
			content = fmt.Sprintf("The service %s on %s is dead: Docker could not stop or remove it cleanly.", s.Name, machine)
		} else if s.ExitCode != nil {
			content = fmt.Sprintf("The service %s on %s stopped with exit code %d.", s.Name, machine, *s.ExitCode)
		}
		out = append(out, finding{TypeServiceDown, SeverityCritical, content, s.Name})
	}
	if s.RestartingReports >= serviceReports {
		content := fmt.Sprintf("The service %s on %s keeps restarting. Docker has restarted it %s.", s.Name, machine, plural(s.RestartCount, "time"))
		out = append(out, finding{TypeServiceRestarting, SeverityCritical, content, s.Name})
	}
	if s.UnhealthyReports >= serviceReports {
		content := fmt.Sprintf("The service %s on %s is unhealthy: its health check fails.", s.Name, machine)
		out = append(out, finding{TypeServiceUnhealthy, SeverityWarning, content, s.Name})
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
