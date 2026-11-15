package notifications

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/notifications/channels"
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

	// dockerSilentAfter is how long a daemon that still sends usage may go without a services
	// report, once it has sent one, before its Docker counts as unreachable.
	dockerSilentAfter = 5 * time.Minute
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

	Services []Service // the Docker containers its daemon reports, each with its service

	// Findings are the problems other domains find on the machine now, such as log shipping
	// that fails. The hub's wiring fills them; each event must be registered. A problem resolves
	// once its finding is gone.
	Findings []Finding

	// What the daemon's latest report said about the machine's Docker engine: running,
	// unreachable, or empty before any report, and how many reports in a row said unreachable.
	Docker                   string
	DockerReason             string // why it is unreachable: socket_missing, permission_denied, refused, timeout or error
	DockerUnreachableReports int
	DockerReportedAt         *time.Time // when the daemon's latest services report arrived; nil before the first
}

// The Docker states a daemon reports.
const (
	dockerUnreachable = "unreachable"

	exitedState = "exited"
	deadState   = "dead"
)

// keeps reports whether an open problem of m stays open though its finding doesn't hold now.
//
// A silent machine's Docker and service problems and the problems other domains find, and service
// problems while its Docker is unreachable, are about a state nobody can see. A service problem
// also stays while the latest report still finds the service so, because after an outage its
// count of reports in a row starts again: it resolves once a report doesn't. The problems of a
// service whose notifications are off, or that is gone, never stay.
func (m Machine) keeps(n Notification, silent bool, now time.Time) bool {
	switch {
	case !builtin(n.Event): // another domain's problem, whose service, if any, isn't a Docker one
		return silent
	case n.Service != nil:
		container := ""
		if n.Container != nil {
			container = *n.Container
		}
		i := slices.IndexFunc(m.Services, func(s Service) bool { return s.Name == *n.Service && s.Container == container })
		if i < 0 || !m.Services[i].AlertsEnabled {
			return false
		}
		if silent || m.Docker == dockerUnreachable || m.dockerSilent(now) {
			return true
		}
		base := channels.Base(n.Event)
		return slices.ContainsFunc(evaluateService(m.Name, m.Services[i], 1), func(f finding) bool { return f.event == base })
	case channels.Base(n.Event) == EventDockerUnreachable:
		return silent
	}
	return false
}

// dockerSilent says the daemon still sends usage but has sent no services report for
// dockerSilentAfter, after it sent one: it lost Docker without saying so.
func (m Machine) dockerSilent(now time.Time) bool {
	return m.DockerReportedAt != nil && m.LastSeen != nil &&
		now.Sub(*m.DockerReportedAt) >= dockerSilentAfter && now.Sub(*m.LastSeen) < dockerSilentAfter
}

// Service is what the rules need to know about one Docker container of a service. The hub's
// wiring fills it from the services store.
type Service struct {
	Name            string // the service's name
	Container       string // the container's name in the service
	State           string // Docker's: created, running, paused, restarting, exited, removing or dead
	ExitCode        *int64
	RestartCount    int64
	RestartPolicy   string // no, always, unless-stopped or on-failure
	AlertsEnabled   bool   // the service's switch
	StoppedByTender bool   // a Tender stop left it stopped

	MemoryBytes      *int64 // its memory use in the latest report, while it runs
	MemoryLimitBytes *int64 // its memory limit, or nil without one

	// How many reports in a row found it down (exited or dead), restarting (in the restarting
	// state, or with a restart count that grew), unhealthy, and above 90% of its memory limit.
	DownReports       int
	RestartingReports int
	UnhealthyReports  int
	MemoryHighReports int
}

// Run is one backup run.
type Run struct {
	Status string // pending, in_progress, completed or failed
	Error  string
	Time   time.Time
}

// Finding is a problem another domain finds on a machine now: a registered event, its severity
// and message, and the service it is about, if any, with the container when it is about one.
type Finding struct {
	Event     string
	Severity  string
	Message   string
	Service   string
	Container string
}

// finding is a condition that holds now. service is set for a problem of a service, and container
// for a problem of one of its containers.
type finding struct {
	event, severity, content string
	service, container       string
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
		out = append(out, finding{EventMachineSilent, SeverityCritical, content, "", ""})
	}

	if m.HasBackupSettings {
		if r := m.LastBackup; r != nil && r.Status == runFailed {
			content := fmt.Sprintf("The last backup on %s failed at %s", m.Name, formatTime(r.Time))
			if r.Error != "" {
				content += ": " + r.Error
			} else {
				content += "."
			}
			out = append(out, finding{EventBackupFailed, SeverityCritical, content, "", ""})
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
			out = append(out, finding{EventBackupStale, severity, content, "", ""})
		}
	}

	if m.LastSeen != nil && m.DiskUsedPercent >= diskWarningPercent {
		severity := SeverityWarning
		if m.DiskUsedPercent >= diskCriticalPercent {
			severity = SeverityCritical
		}
		percent := strconv.FormatFloat(m.DiskUsedPercent, 'f', 1, 64)
		out = append(out, finding{EventDiskFull, severity, fmt.Sprintf("The disk on %s is %s%% full.", m.Name, strings.TrimSuffix(percent, ".0")), "", ""})
	}

	// A silent machine already alerts. Its services, and the problems other domains find from
	// its reports, are as old as its last report, so they don't.
	if len(out) > 0 && out[0].event == EventMachineSilent {
		return out
	}
	for _, f := range m.Findings {
		if t, ok := eventType(f.Event); ok && t.Name == f.Event && slices.Contains(t.Severities, f.Severity) {
			out = append(out, finding{f.Event, f.Severity, f.Message, f.Service, f.Container})
		}
	}
	switch {
	case m.DockerUnreachableReports >= serviceReports:
		out = append(out, finding{EventDockerUnreachable, SeverityCritical,
			fmt.Sprintf("Docker on %s can't be reached: %s. Its services' states are unknown until it is back.", m.Name, dockerWhy(m.DockerReason)), "", ""})
	case m.dockerSilent(now):
		out = append(out, finding{EventDockerUnreachable, SeverityCritical,
			fmt.Sprintf("Docker on %s hasn't been reported since %s, though the machine still reports. Its services' states are unknown until it is back.",
				m.Name, formatTime(*m.DockerReportedAt)), "", ""})
	}
	if m.Docker == dockerUnreachable || m.dockerSilent(now) { // its services' states are unknown
		return out
	}
	for _, s := range m.Services {
		if s.AlertsEnabled {
			out = append(out, evaluateService(m.Name, s, serviceReports)...)
		}
	}
	return out
}

// dockerWhy words the daemon's reason that Docker can't be reached.
func dockerWhy(reason string) string {
	switch reason {
	case "socket_missing":
		return "its socket is gone, so Docker was stopped or removed"
	case "permission_denied":
		return "Tender may not use its socket. If Docker was installed after Tender, run: sudo tender install refresh"
	case "refused":
		return "its socket refuses connections, so Docker is stopped"
	case "timeout":
		return "it didn't answer within 5 seconds, so it is stuck or overloaded"
	}
	return "the daemon couldn't say why"
}

// shouldRun reports whether a stopped service was meant to keep running: it failed, or Docker
// would restart it. One that exited 0 with restart policy no or on-failure finished on purpose.
func shouldRun(s Service) bool {
	failed := s.ExitCode == nil || *s.ExitCode != 0
	return failed || s.RestartPolicy == "always" || s.RestartPolicy == "unless-stopped"
}

// Where names a container of a service on a machine, as messages do: web in notes on apps, or
// migrate on apps for a lone container, which is its own service.
func Where(machine, service, container string) string {
	if container == "" || container == service {
		return service + " on " + machine
	}
	return container + " in " + service + " on " + machine
}

// evaluateService returns the conditions that hold for one container of the machine called
// machine in at least reports reports in a row.
func evaluateService(machine string, s Service, reports int) []finding {
	var out []finding
	where := Where(machine, s.Name, s.Container)
	add := func(event, severity, content string) {
		out = append(out, finding{event, severity, content, s.Name, s.Container})
	}
	if s.DownReports >= reports && !s.StoppedByTender && (s.State == exitedState || s.State == deadState) && shouldRun(s) {
		content := where + " stopped."
		if s.State == deadState {
			content = where + " is dead: Docker could not stop or remove it cleanly."
		} else if s.ExitCode != nil {
			content = fmt.Sprintf("%s stopped with exit code %d.", where, *s.ExitCode)
		}
		add(EventServiceDown, SeverityCritical, content)
	}
	if s.RestartingReports >= reports {
		add(EventServiceRestarting, SeverityCritical, fmt.Sprintf("%s keeps restarting. Docker has restarted it %s.", where, plural(s.RestartCount, "time")))
	}
	if s.UnhealthyReports >= reports {
		add(EventServiceUnhealthy, SeverityWarning, where+" is unhealthy: its health check fails.")
	}
	// Only a container with a memory limit: the services domain has no machine total to compare with.
	if s.MemoryHighReports >= reports && s.MemoryBytes != nil && s.MemoryLimitBytes != nil {
		add(EventServiceMemoryHigh, SeverityWarning, fmt.Sprintf("%s uses %s, %d%% of its %s memory limit.", where,
			formatBytes(*s.MemoryBytes), int(100*float64(*s.MemoryBytes)/float64(*s.MemoryLimitBytes)), formatBytes(*s.MemoryLimitBytes)))
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

// formatBytes is a size in binary units, such as 512 MiB or 1.5 GiB.
func formatBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v, i = v/1024, i+1
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + " " + units[i]
}

// formatTime is 2026-11-14 03:31 UTC.
func formatTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" }

// FormatTime shows t as the hub's messages do, such as "2026-11-15 12:04 UTC", for the problems
// other domains find.
func FormatTime(t time.Time) string { return formatTime(t) }

// FormatDuration shows d as the hub's messages do, such as "3 hours 12 minutes".
func FormatDuration(d time.Duration) string { return formatDuration(d) }

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
