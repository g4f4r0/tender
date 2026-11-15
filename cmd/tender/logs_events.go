package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/logs"
	"github.com/g4f4r0/tender/internal/notifications"
)

// eventLogsShippingFailing is a machine whose daemon can't send its logs to a sink.
const eventLogsShippingFailing = "logs.shipping_failing"

// logFailingAfter is how long sending to a sink must fail before it is a problem.
const logFailingAfter = 15 * time.Minute

// logEvents are the log shipping domain's events.
var logEvents = []notifications.EventType{{
	Name: eventLogsShippingFailing, Severities: []string{notifications.SeverityWarning},
	Title: "log shipping failing", Tool: "list_log_sinks",
	Fires:    "Sending a machine's logs to an enabled sink has failed for 15 minutes, or the daemon dropped records since its last send that worked.",
	Resolves: "Every sink in it takes a send again.",
}}

// shippingFails says whether sending to one sink is a problem at now: it has failed for
// logFailingAfter, or records were dropped since the last send that worked.
func shippingFails(h logs.Health, now time.Time) bool {
	return (h.FailingSince != nil && now.Sub(*h.FailingSince) >= logFailingAfter) || dropped(h)
}

func dropped(h logs.Health) bool {
	return h.LastDroppedAt != nil && (h.LastSentAt == nil || h.LastDroppedAt.After(*h.LastSentAt))
}

// logFindings returns the logs.shipping_failing finding of each machine, one for all its failing
// sinks. Its message names each sink and why, but never a log line.
func logFindings(health []logs.Health, now time.Time) map[string][]notifications.Finding {
	parts := map[string][]string{}
	var order []string
	for _, h := range health {
		if !shippingFails(h, now) {
			continue
		}
		part := "to " + h.Sink
		if h.FailingSince != nil {
			part += fmt.Sprintf(" has failed since %s (%s)", notifications.FormatTime(*h.FailingSince), notifications.FormatDuration(now.Sub(*h.FailingSince)))
			if h.LastError != nil && *h.LastError != "" {
				part += ": " + *h.LastError
			}
		}
		if dropped(h) {
			if h.FailingSince != nil {
				part += ","
			}
			part += fmt.Sprintf(" dropped logs at %s, because its buffer was full or the sink refused them", notifications.FormatTime(*h.LastDroppedAt))
		}
		if parts[h.Machine] == nil {
			order = append(order, h.Machine)
		}
		parts[h.Machine] = append(parts[h.Machine], part)
	}
	out := map[string][]notifications.Finding{}
	for _, m := range order {
		out[m] = []notifications.Finding{{Event: eventLogsShippingFailing, Severity: notifications.SeverityWarning,
			Message: fmt.Sprintf("Log shipping from %s %s. It resolves after a send to the sink works.", m, strings.Join(parts[m], "; "))}}
	}
	return out
}
