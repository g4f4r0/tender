package channels

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Slack posts a message to a Slack incoming webhook. Its URL holds the key, so it is a secret.
type Slack struct{}

// Fields: webhook_url, a secret.
func (Slack) Fields() []Field { return []Field{{Name: "webhook_url", Secret: true, Required: true}} }

// Check needs an http or https webhook_url.
func (Slack) Check(cfg Config) error {
	return checkURL(cfg["webhook_url"], "webhook_url")
}

// Send posts ev as a Slack message.
func (Slack) Send(ctx context.Context, cfg Config, ev Event) error {
	body, err := json.Marshal(map[string]string{"text": SlackText(ev)})
	if err != nil {
		return err
	}
	return post(ctx, cfg["webhook_url"], "Slack webhook", body, nil)
}

// SlackText is the message for ev in Slack's mrkdwn, such as:
//
//	*Critical: service_down on apps* (opened)
//	The service web on apps stopped with exit code 1.
//	Alert 12, open since 2026-11-15 03:31 UTC. Tender hub: https://hub.example.com
func SlackText(ev Event) string {
	a := ev.Alert
	if ev.Event == EventTest {
		return "This is a test from the Tender hub at " + slackEscape(ev.Hub) + ". If you can read it, alerts reach this channel."
	}
	head := a.Severity + ": " + a.Type
	if a.Severity != "" {
		head = strings.ToUpper(head[:1]) + head[1:]
	}
	if a.Machine != nil {
		head += " on " + *a.Machine
	}
	var b strings.Builder
	b.WriteString("*" + slackEscape(head) + "* (" + ev.Event + ")\n")
	b.WriteString(slackEscape(a.Message) + "\n")
	when := "open since " + formatTime(a.CreatedAt)
	if ev.Event == EventResolved && a.ResolvedAt != nil {
		when = "resolved at " + formatTime(*a.ResolvedAt)
	}
	b.WriteString("Alert " + strconv.FormatInt(a.ID, 10) + ", " + when + ". Tender hub: " + slackEscape(ev.Hub))
	return b.String()
}

// slackEscape escapes the three characters Slack's mrkdwn gives a meaning.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func formatTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" }
