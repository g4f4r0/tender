package notifications

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/g4f4r0/tender/internal/notifications/channels"
	"github.com/g4f4r0/tender/internal/server"
)

// The events of the hub's own rules. A problem starts as one of these, gets worse as
// <event>.escalated and clears as <event>.resolved.
const (
	EventMachineSilent = "machine.silent"
	EventBackupFailed  = "backup.failed"
	EventBackupStale   = "backup.stale"
	EventDiskFull      = "disk.full"

	EventServiceDown       = "service.down"
	EventServiceRestarting = "service.restarting"
	EventServiceUnhealthy  = "service.unhealthy"
	EventServiceMemoryHigh = "service.memory_high"
	EventDockerUnreachable = "docker.unreachable"

	// EventChannelFailing is about a channel, not a machine: its deliveries have failed for an hour.
	EventChannelFailing = "channel.failing"

	// EventTest is a test notification, a single message that never resolves.
	EventTest = channels.EventTest
)

// EventType is one event the hub can notify about. Each event name is registered once, with
// Register, so the docs, the API and the rules agree on what exists.
type EventType struct {
	Name       string   // <area>.<condition>, such as service.down: lowercase, digits and underscores, one dot
	Severities []string // the severities its notifications can have, least severe first
	Title      string   // what an email's subject says after the machine's name; for a service, after the service's name
	Tool       string   // the MCP tool to look closer with
	Fires      string   // when it fires, a sentence for the docs
	Resolves   string   // when it resolves, a sentence for the docs; empty for a single message that never does
}

var (
	registryMu sync.RWMutex
	registry   = map[string]EventType{}

	eventName = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
)

// Register adds event types. Other domains register theirs through the hub's wiring, before the
// hub starts. It panics on a name that is not <area>.<condition>, is taken, or names a phase, or
// on a severity that isn't info, warning or critical: those are programming errors.
func Register(ts ...EventType) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, t := range ts {
		_, phase, _ := strings.Cut(t.Name, ".")
		switch {
		case !eventName.MatchString(t.Name) || phase == channels.PhaseEscalated || phase == channels.PhaseResolved:
			panic(fmt.Sprintf("notifications: event name %q is not <area>.<condition>", t.Name))
		case registry[t.Name].Name != "":
			panic(fmt.Sprintf("notifications: event %s is registered twice", t.Name))
		case len(t.Severities) == 0 || t.Title == "" || t.Tool == "" || t.Fires == "":
			panic(fmt.Sprintf("notifications: event %s needs severities, a title, a tool and when it fires", t.Name))
		}
		for _, s := range t.Severities {
			if !validSeverityName(s) {
				panic(fmt.Sprintf("notifications: event %s has severity %q", t.Name, s))
			}
		}
		registry[t.Name] = t
	}
}

// Events returns every registered event type, by name.
func Events() []EventType {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]EventType, 0, len(registry))
	for _, t := range registry {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b EventType) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// eventType returns the registered type of an event name, or of its base name for an
// escalated or resolved one.
func eventType(event string) (EventType, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	t, ok := registry[channels.Base(event)]
	return t, ok
}

func validSeverityName(s string) bool {
	return s == SeverityInfo || s == SeverityWarning || s == SeverityCritical
}

func init() {
	warnCrit := []string{SeverityWarning, SeverityCritical}
	crit := []string{SeverityCritical}
	Register(
		EventType{Name: EventMachineSilent, Severities: crit, Title: "not reporting", Tool: "list_machines",
			Fires:    "The machine's daemon has sent nothing for 10 minutes, or never reported since the machine was added.",
			Resolves: "The daemon reports again."},
		EventType{Name: EventBackupFailed, Severities: crit, Title: "backup failed", Tool: "list_backup_history",
			Fires: "The machine's latest backup run failed.", Resolves: "A later backup run completes."},
		EventType{Name: EventBackupStale, Severities: warnCrit, Title: "no recent backup", Tool: "get_backup_status",
			Fires:    "The machine has backup settings and no completed backup for 26 hours (warning) or 50 hours (critical).",
			Resolves: "A backup completes."},
		EventType{Name: EventDiskFull, Severities: warnCrit, Title: "disk almost full", Tool: "get_machine_usage",
			Fires: "The machine's disk is 90% full (warning) or 95% full (critical).", Resolves: "The disk is below 90% full."},
		EventType{Name: EventServiceDown, Severities: crit, Title: "stopped", Tool: "list_services",
			Fires:    "Two reports in a row find a Docker service exited or dead that should run: it failed, or its restart policy is always or unless-stopped. A Tender stop doesn't count.",
			Resolves: "A report finds it running, or its notifications are turned off, or it is gone."},
		EventType{Name: EventServiceRestarting, Severities: crit, Title: "keeps restarting", Tool: "list_services",
			Fires:    "Two reports in a row find a Docker service restarting, or with a restart count that grew.",
			Resolves: "A report finds it settled, or its notifications are turned off, or it is gone."},
		EventType{Name: EventServiceUnhealthy, Severities: []string{SeverityWarning}, Title: "unhealthy", Tool: "list_services",
			Fires:    "Two reports in a row find a Docker service's health check failing.",
			Resolves: "A report finds it healthy, or its notifications are turned off, or it is gone."},
		EventType{Name: EventServiceMemoryHigh, Severities: []string{SeverityWarning}, Title: "memory high", Tool: "list_services",
			Fires:    "Two reports in a row find a Docker service with a memory limit using more than 90% of it.",
			Resolves: "A report finds it below, or its notifications are turned off, or it is gone."},
		EventType{Name: EventDockerUnreachable, Severities: crit, Title: "Docker unreachable", Tool: "list_services",
			Fires:    "Two reports in a row say Docker can't be reached, or the daemon still reports usage but no services for 5 minutes.",
			Resolves: "A report reaches Docker again."},
		EventType{Name: EventChannelFailing, Severities: []string{SeverityWarning}, Title: "a notification channel is failing", Tool: "list_channels",
			Fires:    "Every try to deliver to an enabled channel has failed for an hour. It goes to every other channel.",
			Resolves: "A delivery or a test to the channel gets through, or it is turned off or removed."},
		EventType{Name: EventTest, Severities: []string{SeverityInfo}, Title: "test", Tool: "send_test_notification",
			Fires: "Someone sends a test with tender channels test, POST /v1/notifications/test or send_test_notification."},
	)
}

// matches says whether a channel's events patterns take the event name event: "*" takes every
// event, "service.*" every service event, and "backup.failed" that one. An escalated or resolved
// event follows its base name.
func matches(patterns []string, event string) bool {
	base := channels.Base(event)
	for _, p := range patterns {
		if p == "*" || p == base || (strings.HasSuffix(p, ".*") && strings.HasPrefix(base, strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}

var eventPattern = regexp.MustCompile(`^(\*|[a-z][a-z0-9_]*\.\*|[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*)$`)

// checkEvents is a 422 unless patterns is a list of patterns that each take a registered event.
func checkEvents(patterns []string) error {
	if len(patterns) == 0 {
		return server.Invalid(`events needs at least one pattern, such as ["*"] for every event.`)
	}
	for _, p := range patterns {
		if !eventPattern.MatchString(p) {
			return server.Invalid(`events take "*", an area such as "service.*", or an event such as "backup.failed"; %q is none of them. `+
				"An escalated or resolved event follows its base name, so leave .escalated and .resolved out.", p)
		}
		if !slices.ContainsFunc(Events(), func(t EventType) bool { return matches([]string{p}, t.Name) }) {
			return server.Invalid("events: %q takes no event this hub knows. The events are listed in docs/notifications.md.", p)
		}
	}
	return nil
}
