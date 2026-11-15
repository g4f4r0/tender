package notifications_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

// mailbox records what it is asked to send, and fails when fail is set.
type mailbox struct {
	to   [][]string
	fail bool
}

func (m *mailbox) Send(_ context.Context, to []string, _ notifications.Message) error {
	if m.fail {
		return errors.New("smtp down")
	}
	m.to = append(m.to, to)
	return nil
}

// patch sends a merge-patch.
func patch(t *testing.T, srv *httptest.Server, path, body string, out any) *http.Response {
	t.Helper()
	return send(t, srv, "PATCH", path, body, out)
}

func TestChannelRoutes(t *testing.T) {
	srv, d := newHubDB(t, nil)

	var list server.List[notifications.Channel]
	if res := send(t, srv, "GET", "/v1/notifications/channels", "", &list); res.StatusCode != 200 || list.Data == nil || len(list.Data) != 0 {
		t.Fatalf("empty list: %d %+v, want 200 and []", res.StatusCode, list)
	}

	var c notifications.Channel
	res := send(t, srv, "POST", "/v1/notifications/channels", `{"name":"ops","type":"email","config":{"to":"ops@example.com"}}`, &c)
	if res.StatusCode != 201 || c.ID == 0 || c.Type != "email" || !c.Enabled || c.MinSeverity != "warning" || c.Config["to"] != "ops@example.com" ||
		res.Header.Get("Location") != "/v1/notifications/channels/ops" {
		t.Fatalf("add email: %d %+v %q", res.StatusCode, c, res.Header.Get("Location"))
	}

	// A Slack URL is its key: stored sealed, shown as *** with webhook_url_set.
	const slackURL = "https://hooks.slack.com/services/T000/B000/SLACK-KEY-1"
	res = send(t, srv, "POST", "/v1/notifications/channels", `{"name":"chat","type":"slack","config":{"webhook_url":"`+slackURL+`"},"min_severity":"critical"}`, &c)
	if res.StatusCode != 201 || c.Config["webhook_url"] != "***" || c.Config["webhook_url_set"] != true || c.MinSeverity != "critical" {
		t.Fatalf("add slack: %d %+v", res.StatusCode, c)
	}

	// A webhook's secret is made by the hub and shown once.
	var hook notifications.Channel
	res = send(t, srv, "POST", "/v1/notifications/channels", `{"name":"hook","type":"webhook","config":{"url":"https://example.com/in"},"enabled":false}`, &hook)
	made, _ := hook.Config["secret"].(string)
	if res.StatusCode != 201 || !strings.HasPrefix(made, "whsec_") || hook.Config["secret_set"] != true || hook.Enabled {
		t.Fatalf("add webhook: %d %+v", res.StatusCode, hook)
	}
	var again notifications.Channel
	send(t, srv, "GET", "/v1/notifications/channels/hook", "", &again)
	if again.Config["secret"] != "***" || again.Config["url"] != "https://example.com/in" {
		t.Errorf("GET shows %+v, want the secret hidden", again.Config)
	}
	send(t, srv, "GET", "/v1/notifications/channels", "", &list)
	if len(list.Data) != 3 || list.Data[0].Name != "ops" || list.Data[2].Name != "hook" {
		t.Errorf("list: %+v", list.Data)
	}
	for _, ch := range list.Data {
		for k, v := range ch.Config {
			if s, ok := v.(string); ok && (strings.Contains(s, "SLACK-KEY") || strings.HasPrefix(s, "whsec_")) {
				t.Errorf("the list shows the secret %s of %s", k, ch.Name)
			}
		}
	}
	// The database holds the secrets sealed only.
	var raw string
	if err := d.Read.QueryRow(`SELECT group_concat(config, ' ') FROM notification_channels`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "SLACK-KEY") || strings.Contains(raw, made) {
		t.Errorf("a secret is in the database in the clear: %s", raw)
	}

	var p problem
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"duplicate", `{"name":"ops","type":"email","config":{"to":"x@example.com"}}`, 409},
		{"bad name", `{"name":"Ops Team","type":"email","config":{"to":"x@example.com"}}`, 422},
		{"unknown type", `{"name":"x","type":"pager","config":{}}`, 422},
		{"missing config", `{"name":"x","type":"email"}`, 422},
		{"unknown config key", `{"name":"x","type":"email","config":{"to":"x@example.com","cc":"y@example.com"}}`, 422},
		{"bad address", `{"name":"x","type":"email","config":{"to":"Ops <x@example.com>"}}`, 422},
		{"header injection", `{"name":"x","type":"email","config":{"to":"a@example.com\r\nBcc: x@example.com"}}`, 422},
		{"bad url", `{"name":"x","type":"slack","config":{"webhook_url":"hooks.slack.com"}}`, 422},
		{"secret in the create", `{"name":"x","type":"webhook","config":{"url":"https://example.com","secret":"mine-and-long-enough-12345"}}`, 422},
		{"exec path", `{"name":"x","type":"exec","config":{"command":"../bin/sh"}}`, 422},
		{"exec missing", `{"name":"x","type":"exec","config":{"command":"nope"}}`, 422},
		{"bad severity", `{"name":"x","type":"email","config":{"to":"x@example.com"},"min_severity":"loud"}`, 422},
		{"config not strings", `{"name":"x","type":"email","config":{"to":5}}`, 422},
		{"unknown field", `{"name":"x","type":"email","config":{"to":"x@example.com"},"nope":1}`, 400},
	} {
		if res := send(t, srv, "POST", "/v1/notifications/channels", c.body, &p); res.StatusCode != c.want || p.Detail == "" {
			t.Errorf("%s: %d %q, want %d with a sentence", c.name, res.StatusCode, p.Detail, c.want)
		}
	}

	// PATCH merges: a secret is replaced, never read; *** is refused; others stay.
	res = patch(t, srv, "/v1/notifications/channels/chat", `{"config":{"webhook_url":"https://hooks.slack.com/services/T/B/SLACK-KEY-2"},"min_severity":"info"}`, &c)
	if res.StatusCode != 200 || c.Config["webhook_url"] != "***" || c.MinSeverity != "info" || !c.Enabled {
		t.Errorf("patch slack: %d %+v", res.StatusCode, c)
	}
	if res := patch(t, srv, "/v1/notifications/channels/chat", `{"config":{"webhook_url":"***"}}`, &p); res.StatusCode != 422 || !strings.Contains(p.Detail, "secret") {
		t.Errorf("patch ***: %d %q", res.StatusCode, p.Detail)
	}
	if res := patch(t, srv, "/v1/notifications/channels/ops", `{"config":{"to":null}}`, &p); res.StatusCode != 422 {
		t.Errorf("remove a required key: %d %q", res.StatusCode, p.Detail)
	}
	if res := patch(t, srv, "/v1/notifications/channels/hook", `{"config":{"secret":"short"}}`, &p); res.StatusCode != 422 {
		t.Errorf("short secret: %d %q", res.StatusCode, p.Detail)
	}
	res = patch(t, srv, "/v1/notifications/channels/hook", `{"config":{"secret":"a-new-secret-of-enough-length"},"enabled":true}`, &c)
	if res.StatusCode != 200 || c.Config["secret"] != "***" || !c.Enabled || c.Config["url"] != "https://example.com/in" {
		t.Errorf("patch webhook: %d %+v", res.StatusCode, c)
	}
	for _, path := range []string{"/v1/notifications/channels/nope"} {
		if res := patch(t, srv, path, `{"enabled":false}`, &p); res.StatusCode != 404 {
			t.Errorf("patch %s: %d", path, res.StatusCode)
		}
		if res := send(t, srv, "GET", path, "", &p); res.StatusCode != 404 || p.Detail == "" {
			t.Errorf("GET %s: %d", path, res.StatusCode)
		}
	}
	if res := send(t, srv, "DELETE", "/v1/notifications/channels/chat", "", nil); res.StatusCode != 204 {
		t.Errorf("delete: %d", res.StatusCode)
	}
	if res := send(t, srv, "DELETE", "/v1/notifications/channels/chat", "", &p); res.StatusCode != 404 || p.Detail == "" {
		t.Errorf("delete again: %d", res.StatusCode)
	}
}

func TestTestRoute(t *testing.T) {
	// No channel: a 422 that says to add one.
	box := &mailbox{}
	srv := newHubSending(t, box)
	var p problem
	if res := send(t, srv, "POST", "/v1/notifications/test", "", &p); res.StatusCode != 422 || !strings.Contains(p.Detail, "Add one") {
		t.Errorf("no channels: %d %q", res.StatusCode, p.Detail)
	}

	var got []byte
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		if r.Header.Get("Tender-Signature") == "" {
			w.WriteHeader(400)
		}
	}))
	defer hook.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }))
	defer dead.Close()
	send(t, srv, "POST", "/v1/notifications/channels", `{"name":"a","type":"email","config":{"to":"a@example.com"}}`, nil)
	send(t, srv, "POST", "/v1/notifications/channels", `{"name":"b","type":"email","config":{"to":"b@example.com"},"enabled":false}`, nil)
	send(t, srv, "POST", "/v1/notifications/channels", `{"name":"hook","type":"webhook","config":{"url":"`+hook.URL+`"}}`, nil)
	send(t, srv, "POST", "/v1/notifications/channels", `{"name":"dead","type":"slack","config":{"webhook_url":"`+dead.URL+`/KEY-IN-URL"}}`, nil)

	// Every enabled channel, once each; b is off.
	var res server.List[notifications.Delivery]
	if r := send(t, srv, "POST", "/v1/notifications/test", "", &res); r.StatusCode != 200 || len(res.Data) != 3 {
		t.Fatalf("test: %d %+v", r.StatusCode, res)
	}
	status := map[string]string{}
	for _, d := range res.Data {
		status[d.Channel] = d.Status
		if d.Event != "test" || d.NotificationID != nil || d.Attempts != 1 {
			t.Errorf("delivery %+v", d)
		}
		if d.Channel == "dead" && (d.Error == nil || !strings.Contains(*d.Error, "answered 404") || strings.Contains(*d.Error, "KEY-IN-URL")) {
			t.Errorf("dead: %v", d.Error)
		}
	}
	if status["a"] != "delivered" || status["hook"] != "delivered" || status["dead"] != "failed" {
		t.Errorf("statuses %v", status)
	}
	if len(box.to) != 1 || !slices.Equal(box.to[0], []string{"a@example.com"}) || !strings.Contains(string(got), `"event":"test"`) {
		t.Errorf("sent %v, webhook got %s", box.to, got)
	}

	// One channel by name, even when it is off.
	if r := send(t, srv, "POST", "/v1/notifications/test", `{"channel":"b"}`, &res); r.StatusCode != 200 || len(res.Data) != 1 || res.Data[0].Status != "delivered" {
		t.Errorf("test b: %d %+v", r.StatusCode, res)
	}
	if r := send(t, srv, "POST", "/v1/notifications/test", `{"channel":"nope"}`, &p); r.StatusCode != 404 {
		t.Errorf("test nope: %d", r.StatusCode)
	}

	// Email off: an email channel's test fails and says what to set.
	off := newHubSending(t, nil)
	send(t, off, "POST", "/v1/notifications/channels", `{"name":"a","type":"email","config":{"to":"a@example.com"}}`, nil)
	if r := send(t, off, "POST", "/v1/notifications/test", "", &res); r.StatusCode != 200 || res.Data[0].Status != "failed" ||
		!strings.Contains(*res.Data[0].Error, "TENDER_SMTP_HOST") {
		t.Errorf("email off: %d %+v", r.StatusCode, res)
	}
}
