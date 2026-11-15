package notifications

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/notifications/channels"
	"github.com/g4f4r0/tender/internal/server"
)

// endpoint is a fake webhook or Slack receiver that answers status and keeps the events it got.
type endpoint struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	got    []string // "event alert-type", or the Slack text's first line
	bodies [][]byte
}

func newEndpoint(t *testing.T, status int) *endpoint {
	e := &endpoint{status: status}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		defer e.mu.Unlock()
		w.WriteHeader(e.status)
		if e.status/100 != 2 {
			_, _ = io.WriteString(w, "down for maintenance")
			return
		}
		var ev channels.Event
		var slack struct{ Text string }
		if json.Unmarshal(body, &ev) == nil && ev.Version == 1 {
			e.got = append(e.got, ev.Event+" "+ev.Alert.Type)
		} else if json.Unmarshal(body, &slack) == nil {
			first, _, _ := strings.Cut(slack.Text, "\n")
			e.got = append(e.got, first)
		}
		e.bodies = append(e.bodies, body)
	}))
	t.Cleanup(e.Close)
	return e
}

func (e *endpoint) set(status int) { e.mu.Lock(); e.status = status; e.mu.Unlock() }

func (e *endpoint) events() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.got...)
}

// add adds a channel through the store, as the routes do.
func (r *rig) add(name, typ string, cfg map[string]string, minSeverity string) Channel {
	r.t.Helper()
	c, err := r.engine.store.AddChannel(r.t.Context(), ChannelInput{Name: name, Type: typ, Config: cfg, MinSeverity: minSeverity})
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

// deliveries lists every delivery as "channel event status attempts", in order.
func (r *rig) deliveries() []string {
	r.t.Helper()
	rows, err := r.db.Read.Query(`SELECT c.name, d.event, d.status, d.attempts FROM notification_deliveries d
		JOIN notification_channels c ON c.id = d.channel_id ORDER BY d.id`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var name, event, status, attempts string
		if err := rows.Scan(&name, &event, &status, &attempts); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, name+" "+event+" "+status+" "+attempts)
	}
	return out
}

func (r *rig) wantDeliveries(want ...string) {
	r.t.Helper()
	if got := r.deliveries(); !slices.Equal(got, want) {
		r.t.Errorf("deliveries:\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// adaptersDir makes a folder of exec adapters for the test.
func adaptersDir(t *testing.T) string {
	dir := t.TempDir()
	old := AdaptersDir
	AdaptersDir = dir
	t.Cleanup(func() { AdaptersDir = old })
	return dir
}

// Every type gets the alert; one failing channel neither delays nor repeats the others.
func TestEveryChannelOnItsOwn(t *testing.T) {
	dir := adaptersDir(t)
	r := newRig(t)
	out := filepath.Join(t.TempDir(), "exec.log")
	if err := os.WriteFile(filepath.Join(dir, "log-it"), []byte("#!/bin/sh\ncat >>"+out+"\necho >>"+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hook, chat := newEndpoint(t, 503), newEndpoint(t, 200)
	r.add("hook", TypeWebhook, map[string]string{"url": hook.URL}, "")
	r.add("chat", TypeSlack, map[string]string{"webhook_url": chat.URL + "/services/SLACK-KEY"}, "")
	r.add("run", TypeExec, map[string]string{"command": "log-it"}, "")

	r.tick(0, silent())
	r.want([]string{"machine_silent critical sent"}, "a: not reporting")
	r.wantDeliveries("one opened delivered 1", "hook opened pending 1", "chat opened delivered 1", "run opened delivered 1")
	if got := chat.events(); !slices.Equal(got, []string{"*Critical: machine_silent on a* (opened)"}) {
		t.Errorf("slack got %q", got)
	}
	logged, _ := os.ReadFile(out)
	var ev channels.Event
	if err := json.Unmarshal(logged, &ev); err != nil || ev.Event != "opened" || ev.Alert.Type != "machine_silent" || ev.Hub != "https://hub1.example.com" ||
		ev.Alert.Machine == nil || *ev.Alert.Machine != "a" {
		t.Errorf("exec got %s: %v", logged, err)
	}

	// 30 seconds on, the webhook isn't due (1 minute), and nothing goes again to the others.
	r.tick(30*time.Second, silent())
	r.wantDeliveries("one opened delivered 1", "hook opened pending 1", "chat opened delivered 1", "run opened delivered 1")
	// It is back: the next try, a minute after the first, delivers it, once.
	hook.set(200)
	r.tick(time.Minute, silent())
	r.tick(2*time.Minute, silent())
	r.wantDeliveries("one opened delivered 1", "hook opened delivered 2", "chat opened delivered 1", "run opened delivered 1")
	if got := hook.events(); !slices.Equal(got, []string{"opened machine_silent"}) {
		t.Errorf("webhook got %q", got)
	}

	// The alert resolves: every channel gets the resolved event.
	r.tick(3*time.Minute, quiet(3*time.Minute))
	r.wantDeliveries("one opened delivered 1", "hook opened delivered 2", "chat opened delivered 1", "run opened delivered 1",
		"one resolved delivered 1", "hook resolved delivered 1", "chat resolved delivered 1", "run resolved delivered 1")
	var last channels.Event
	hook.mu.Lock()
	_ = json.Unmarshal(hook.bodies[len(hook.bodies)-1], &last)
	hook.mu.Unlock()
	if last.Alert.ResolvedAt == nil || !last.Alert.ResolvedAt.Equal(t0.Add(3*time.Minute)) {
		t.Errorf("resolved_at %v", last.Alert.ResolvedAt)
	}

	// No secret reached the log.
	if strings.Contains(r.logs.String(), "SLACK-KEY") || strings.Contains(r.logs.String(), "whsec_") {
		t.Errorf("a secret is in the log:\n%s", r.logs)
	}
}

// A 4xx other than 429 fails at once with the answer; a 5xx is retried with the backoff until 24 hours.
func TestDeliveryFailures(t *testing.T) {
	r := newRig(t)
	gone, busy := newEndpoint(t, 410), newEndpoint(t, 500)
	r.add("gone", TypeWebhook, map[string]string{"url": gone.URL}, "")
	r.add("busy", TypeWebhook, map[string]string{"url": busy.URL}, "")
	r.tick(0, silent())
	for m := 1; m <= 60; m++ {
		r.tick(time.Duration(m)*time.Minute, silent())
	}
	// busy: tries at 0, 1, 3, 8, 18 and 48 minutes.
	r.wantDeliveries("one opened delivered 1", "gone opened failed 1", "busy opened pending 6")
	var why string
	if err := r.db.Read.QueryRow(`SELECT error FROM notification_deliveries WHERE id = 2`).Scan(&why); err != nil ||
		why != "the webhook answered 410: down for maintenance" {
		t.Errorf("error %q, %v", why, err)
	}
	// Every 30 minutes until 24 hours have passed, then failed for good.
	r.tick(24*time.Hour+time.Minute, silent())
	r.wantDeliveries("one opened delivered 1", "gone opened failed 1", "busy opened failed 7")
}

// An alert's events reach a channel in order: a newer one waits while an older one is pending.
// One that resolves before a channel got it drops what that channel still owed.
func TestOrderAndResolveBeforeDelivered(t *testing.T) {
	r := newRig(t)
	hook := newEndpoint(t, 500)
	r.add("hook", TypeWebhook, map[string]string{"url": hook.URL}, "")
	r.tick(0, staleAfter(0, 26))
	r.tick(time.Hour, staleAfter(time.Hour, 50)) // escalates: one got opened, hook still owes it
	r.wantDeliveries("one opened delivered 1", "hook opened pending 2", "one escalated delivered 1", "hook escalated pending 0")
	hook.set(200)
	r.tick(time.Hour+2*time.Minute, staleAfter(time.Hour+2*time.Minute, 50))
	if got := hook.events(); !slices.Equal(got, []string{"opened backup_stale", "escalated backup_stale"}) {
		t.Errorf("hook got %q, want opened then escalated", got)
	}

	// A second alert that hook can't get before it resolves: hook gets neither event.
	r2 := newRig(t)
	hook2 := newEndpoint(t, 500)
	r2.add("hook", TypeWebhook, map[string]string{"url": hook2.URL}, "")
	r2.tick(0, silent())
	hook2.set(200)
	r2.tick(30*time.Second, quiet(30*time.Second))
	r2.tick(5*time.Minute, quiet(5*time.Minute))
	r2.wantDeliveries("one opened delivered 1", "hook opened failed 1", "one resolved delivered 1")
	if got := hook2.events(); len(got) != 0 {
		t.Errorf("hook got %q", got)
	}
}

// A channel gets alerts from its min_severity up, and an escalation into its range.
func TestMinSeverity(t *testing.T) {
	r := newRig(t)
	pager := newEndpoint(t, 200)
	r.add("pager", TypeWebhook, map[string]string{"url": pager.URL}, SeverityCritical)
	r.tick(0, staleAfter(0, 26)) // warning
	r.tick(24*time.Hour, staleAfter(24*time.Hour, 50))
	r.tick(25*time.Hour, staleAfter(25*time.Hour, 1))
	if got := pager.events(); !slices.Equal(got, []string{"escalated backup_stale", "resolved backup_stale"}) {
		t.Errorf("pager got %q", got)
	}
}

// A channel turned off gets nothing more, and what it owed is dropped.
func TestChannelOff(t *testing.T) {
	r := newRig(t)
	hook := newEndpoint(t, 500)
	r.add("hook", TypeWebhook, map[string]string{"url": hook.URL}, "")
	r.tick(0, silent())
	off := false
	if _, err := r.engine.store.PatchChannel(t.Context(), "hook", ChannelPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	hook.set(200)
	r.tick(5*time.Minute, quiet(5*time.Minute))
	r.wantDeliveries("one opened delivered 1", "hook opened failed 1", "one resolved delivered 1")
	if len(hook.events()) != 0 {
		t.Errorf("a channel that is off got %q", hook.events())
	}
}

// channel_failing opens when a channel's deliveries have failed for an hour, goes to every other
// channel, never the failing one, and resolves once one gets through.
func TestChannelFailing(t *testing.T) {
	r := newRig(t)
	r.engine.FailingAfter = time.Hour
	hook, chat := newEndpoint(t, 503), newEndpoint(t, 200)
	r.add("hook", TypeWebhook, map[string]string{"url": hook.URL}, "")
	r.add("chat", TypeSlack, map[string]string{"webhook_url": chat.URL}, SeverityCritical)
	r.tick(0, silent())
	r.tick(59*time.Minute, silent())
	r.want([]string{"machine_silent critical sent"}, "a: not reporting")
	r.tick(61*time.Minute, silent())
	r.want([]string{"machine_silent critical sent", "channel_failing warning sent"}, "a: not reporting", "Tender: a notification channel is failing")
	var content string
	var channel *string
	if err := r.db.Read.QueryRow(`SELECT content, channel FROM notifications WHERE type = 'channel_failing'`).Scan(&content, &channel); err != nil {
		t.Fatal(err)
	}
	if channel == nil || *channel != "hook" || !strings.Contains(content, "The webhook channel hook has failed to deliver alerts since 2026-11-14 03:30 UTC") ||
		!strings.Contains(content, "answered 503") {
		t.Errorf("channel %v, content %q", channel, content)
	}
	// It reached chat, though chat takes critical only, and not hook.
	if got := chat.events(); !slices.Equal(got, []string{"*Critical: machine_silent on a* (opened)", "*Warning: channel_failing* (opened)"}) {
		t.Errorf("chat got %q", got)
	}
	if strings.Contains(strings.Join(hook.events(), " "), "channel_failing") {
		t.Error("the failing channel got its own alert")
	}
	ns, err := r.engine.store.List(t.Context(), 0, "", "", mustPage(t))
	if err != nil || ns[0].Type != TypeChannelFailing || ns[0].Machine != nil || len(ns[0].Deliveries) != 2 {
		t.Errorf("list: %+v %v", ns, err)
	}

	// The webhook is back: its next try delivers, and channel_failing resolves.
	// Its tries are at 0, 1, 3, 8, 18, 48 and 78 minutes, then 108; the rules see the success at their next run.
	hook.set(200)
	r.tick(108*time.Minute, silent())
	r.want([]string{"machine_silent critical sent", "channel_failing warning sent"}, "a: not reporting", "Tender: a notification channel is failing")
	r.tick(109*time.Minute, silent())
	r.want([]string{"machine_silent critical sent", "channel_failing warning resolved"}, "a: not reporting",
		"Tender: a notification channel is failing", "Tender: a notification channel is failing, resolved")
}

func mustPage(t *testing.T) server.Page {
	t.Helper()
	p, err := server.NewPage(0, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
