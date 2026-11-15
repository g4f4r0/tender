package notifications

import (
	"fmt"
	"strings"
	"time"
)

// TypeLogShippingFailing is a machine whose daemon can't send its logs to a sink.
const TypeLogShippingFailing = "log_shipping_failing"

// logFailingAfter is how long sending to a sink must fail before it alerts.
const logFailingAfter = 15 * time.Minute

// LogSink is what the rules need to know about one machine's sending to one enabled sink. The
// hub's wiring fills it from the logs store.
type LogSink struct {
	Name          string
	FailingSince  *time.Time // when sending started to fail; nil while it works
	LastError     string
	LastSentAt    *time.Time
	LastDroppedAt *time.Time // when the daemon last dropped records for the sink
}

// failing reports whether s alerts at now: sending has failed for logFailingAfter, or records
// were dropped since the last send that worked.
func (s LogSink) failing(now time.Time) bool {
	if s.FailingSince != nil && now.Sub(*s.FailingSince) >= logFailingAfter {
		return true
	}
	return s.LastDroppedAt != nil && (s.LastSentAt == nil || s.LastDroppedAt.After(*s.LastSentAt))
}

// evaluateLogs returns the log_shipping_failing finding for m, one for all its failing sinks.
// Its content names each sink and why, but never a log line.
func evaluateLogs(m Machine, now time.Time) []finding {
	var parts []string
	for _, s := range m.LogSinks {
		if !s.failing(now) {
			continue
		}
		part := "to " + s.Name
		if s.FailingSince != nil {
			part += fmt.Sprintf(" has failed since %s (%s)", formatTime(*s.FailingSince), formatDuration(now.Sub(*s.FailingSince)))
			if s.LastError != "" {
				part += ": " + s.LastError
			}
		}
		if s.LastDroppedAt != nil && (s.LastSentAt == nil || s.LastDroppedAt.After(*s.LastSentAt)) {
			if s.FailingSince != nil {
				part += ","
			}
			part += fmt.Sprintf(" dropped logs at %s, because its buffer was full or the sink refused them", formatTime(*s.LastDroppedAt))
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return nil
	}
	content := fmt.Sprintf("Log shipping from %s %s. It resolves after a send to the sink works.", m.Name, strings.Join(parts, "; "))
	return []finding{{TypeLogShippingFailing, SeverityWarning, content, ""}}
}
