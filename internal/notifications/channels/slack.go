package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Slack posts a message to a Slack incoming webhook. Its URL holds the key, so it is a secret.
type Slack struct{}

// Fields: webhook_url, a secret.
func (Slack) Fields() []Field { return []Field{{Name: "webhook_url", Secret: true, Required: true}} }

// Check needs a webhook_url on https://hooks.slack.com/, so the key in it only ever goes to Slack.
func (Slack) Check(cfg Config) error {
	if allowLocal.Load() {
		return checkURL(cfg["webhook_url"], "webhook_url")
	}
	u, err := url.Parse(cfg["webhook_url"])
	if err != nil || u.Scheme != "https" || u.Host != "hooks.slack.com" || u.User != nil {
		return errors.New("webhook_url must be a Slack incoming webhook URL, which starts with https://hooks.slack.com/. Copy it from the app's Incoming Webhooks page.")
	}
	return nil
}

// Send posts ev as a Slack message.
func (s Slack) Send(ctx context.Context, cfg Config, ev Event) error {
	if err := s.Check(cfg); err != nil {
		return &Permanent{err}
	}
	body, err := json.Marshal(map[string]string{"text": SlackText(ev)})
	if err != nil {
		return err
	}
	return post(ctx, cfg["webhook_url"], "Slack webhook", body, nil)
}

// SlackText is the message for ev in Slack's mrkdwn, such as:
//
//	*Critical: service.down on apps*
//	web in notes on apps stopped with exit code 1.
//	Notification 12, 2026-11-15 03:31 UTC. Tender hub: https://hub.example.com
func SlackText(ev Event) string {
	n := ev.Notification
	if ev.Event == EventTest {
		return "This is a test from the Tender hub at " + slackEscape(ev.Hub) + ". If you can read it, notifications reach this channel."
	}
	head := n.Severity + ": " + n.Event
	if n.Severity != "" {
		head = strings.ToUpper(head[:1]) + head[1:]
	}
	if n.Machine != nil {
		head += " on " + *n.Machine
	}
	var b strings.Builder
	b.WriteString("*" + slackEscape(head) + "*\n")
	b.WriteString(slackEscape(n.Message) + "\n")
	b.WriteString("Notification " + strconv.FormatInt(n.ID, 10) + ", " + formatTime(n.CreatedAt) + ". Tender hub: " + slackEscape(ev.Hub))
	return b.String()
}

// slackEscape escapes the three characters Slack's mrkdwn gives a meaning.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func formatTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" }
