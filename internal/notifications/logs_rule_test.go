package notifications

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// shipping is machine a, reporting at t0, with these log sinks.
func shipping(sinks ...LogSink) Machine {
	m := quiet(0)
	m.LogSinks = sinks
	return m
}

func TestEvaluateLogShipping(t *testing.T) {
	sent := t0.Add(-time.Minute)
	cases := []struct {
		name string
		s    LogSink
		want []string
	}{
		{"working", LogSink{Name: "otel", LastSentAt: &sent}, []string{}},
		{"failing for 14 minutes", LogSink{Name: "otel", FailingSince: ptr(t0.Add(-14 * time.Minute)), LastSentAt: &sent}, []string{}},
		{"failing for 15 minutes", LogSink{Name: "otel", FailingSince: ptr(t0.Add(-15 * time.Minute)), LastError: "the sink answered 503 Service Unavailable"},
			[]string{"log_shipping_failing:warning"}},
		{"dropped, not sent since", LogSink{Name: "otel", LastSentAt: &sent, LastDroppedAt: ptr(t0.Add(-30 * time.Second))}, []string{"log_shipping_failing:warning"}},
		{"dropped, never sent", LogSink{Name: "otel", LastDroppedAt: ptr(t0)}, []string{"log_shipping_failing:warning"}},
		{"dropped, then sent", LogSink{Name: "otel", LastSentAt: &sent, LastDroppedAt: ptr(t0.Add(-2 * time.Minute))}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summary(evaluate(shipping(c.s), t0)); !slices.Equal(got, c.want) {
				t.Errorf("findings = %v, want %v", got, c.want)
			}
		})
	}

	// one alert for the machine names every failing sink and why.
	fs := evaluate(shipping(
		LogSink{Name: "otel", FailingSince: ptr(t0.Add(-20 * time.Minute)), LastError: "the sink answered 503 Service Unavailable", LastDroppedAt: ptr(t0)},
		LogSink{Name: "hook", LastDroppedAt: ptr(t0)},
		LogSink{Name: "fine", LastSentAt: &sent},
	), t0)
	if len(fs) != 1 {
		t.Fatalf("findings %v", summary(fs))
	}
	for _, want := range []string{"Log shipping from a to otel has failed since", "(20 minutes): the sink answered 503", "; to hook dropped logs at"} {
		if !strings.Contains(fs[0].content, want) {
			t.Errorf("content %q lacks %q", fs[0].content, want)
		}
	}
	if strings.Contains(fs[0].content, "fine") {
		t.Errorf("content names a working sink: %q", fs[0].content)
	}

	// a silent machine alerts as silent; its log alert, from before, stays open.
	m := silent()
	m.LogSinks = []LogSink{{Name: "otel", LastDroppedAt: ptr(t0)}}
	if got := summary(evaluate(m, t0)); !slices.Equal(got, []string{"machine_silent:critical"}) {
		t.Errorf("silent machine findings = %v", got)
	}
	if !m.keeps(Notification{Type: TypeLogShippingFailing}, true, t0) || shipping().keeps(Notification{Type: TypeLogShippingFailing}, false, t0) {
		t.Error("the log alert stays open only while the machine is silent")
	}
}

// The alert opens with an email and resolves once a send works.
func TestLogShippingAlert(t *testing.T) {
	r := newRig(t)
	r.tick(0, shipping(LogSink{Name: "otel", LastDroppedAt: ptr(t0)}))
	r.want([]string{"log_shipping_failing warning sent"}, "a: log shipping failing")
	r.tick(time.Minute, func() Machine {
		m := shipping(LogSink{Name: "otel", LastDroppedAt: ptr(t0), LastSentAt: ptr(t0.Add(time.Minute))})
		m.LastSeen = ptr(t0.Add(time.Minute))
		return m
	}())
	r.want([]string{"log_shipping_failing warning resolved"}, "a: log shipping failing", "a: log shipping failing, resolved")
}
