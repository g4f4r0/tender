package main

import (
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/logs"
	"github.com/g4f4r0/tender/internal/notifications"
)

func TestLogFindings(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 20, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	sent := at(-time.Minute)
	sendErr := "the sink answered 503 Service Unavailable"
	sink := func(machine, name string, st logs.Status) logs.Health {
		return logs.Health{Machine: machine, Sink: name, Status: st}
	}
	cases := []struct {
		name string
		st   logs.Status
		want bool
	}{
		{"working", logs.Status{LastSentAt: sent}, false},
		{"failing for 14 minutes", logs.Status{FailingSince: at(-14 * time.Minute), LastSentAt: sent}, false},
		{"failing for 15 minutes", logs.Status{FailingSince: at(-15 * time.Minute), LastError: &sendErr}, true},
		{"dropped, not sent since", logs.Status{LastSentAt: sent, LastDroppedAt: at(-30 * time.Second)}, true},
		{"dropped, never sent", logs.Status{LastDroppedAt: at(0)}, true},
		{"dropped, then sent", logs.Status{LastSentAt: sent, LastDroppedAt: at(-2 * time.Minute)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := logFindings([]logs.Health{sink("apps", "otel", c.st)}, now)
			if (len(got["apps"]) == 1) != c.want || len(got) > 1 {
				t.Errorf("findings = %+v, want a finding %v", got, c.want)
			}
		})
	}

	// One finding for the machine names every failing sink and why, and leaves out the rest.
	got := logFindings([]logs.Health{
		sink("apps", "otel", logs.Status{FailingSince: at(-20 * time.Minute), LastError: &sendErr, LastDroppedAt: at(0)}),
		sink("apps", "hook", logs.Status{LastDroppedAt: at(0)}),
		sink("apps", "fine", logs.Status{LastSentAt: sent}),
		sink("db", "fine", logs.Status{LastSentAt: sent}),
	}, now)
	if len(got) != 1 || len(got["apps"]) != 1 {
		t.Fatalf("findings %+v", got)
	}
	f := got["apps"][0]
	if f.Event != eventLogsShippingFailing || f.Severity != notifications.SeverityWarning || f.Service != "" {
		t.Errorf("finding %+v", f)
	}
	for _, want := range []string{"Log shipping from apps to otel has failed since 2026-11-15 12:00 UTC", "(20 minutes): the sink answered 503", "; to hook dropped logs at"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message %q lacks %q", f.Message, want)
		}
	}
	if strings.Contains(f.Message, "fine") {
		t.Errorf("message names a working sink: %q", f.Message)
	}
}
