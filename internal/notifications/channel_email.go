package notifications

import (
	"context"
	"errors"
	"net/mail"

	"github.com/g4f4r0/tender/internal/notifications/channels"
)

// Hub names the hub in what it sends: its host name in emails, and its URL in events.
type Hub struct {
	Name string
	URL  string
}

// errEmailOff is why an email channel can't send.
var errEmailOff = errors.New("Email is off on this hub. Set TENDER_SMTP_HOST and TENDER_SMTP_FROM in /etc/tender/hub.env and restart the hub.")

// emailAdapter sends an event as an email through the hub's SMTP settings, to one address.
type emailAdapter struct {
	sender Sender // nil when email is off
	hub    Hub
}

func (emailAdapter) Fields() []channels.Field {
	return []channels.Field{{Name: "to", Required: true}}
}

// Check needs to to be one bare address, such as ops@example.com.
func (emailAdapter) Check(cfg channels.Config) error {
	addr, err := mail.ParseAddress(cfg["to"])
	if err != nil || addr.Address != cfg["to"] {
		return errors.New("to must be one bare address such as ops@example.com.")
	}
	return nil
}

func (e emailAdapter) Send(ctx context.Context, cfg channels.Config, ev channels.Event) error {
	if e.sender == nil {
		return errEmailOff
	}
	msg, err := emailFor(ev, e.hub.Name)
	if err != nil {
		return err
	}
	return e.sender.Send(ctx, []string{cfg["to"]}, msg)
}

// emailFor renders the email of an event.
func emailFor(ev channels.Event, hub string) (Message, error) {
	if ev.Event == channels.EventTest {
		return TestMessage(hub)
	}
	a := ev.Alert
	text := typeText[a.Type]
	title := text.title
	if a.Service != nil {
		title = *a.Service + " " + title
	}
	if title == "" {
		title = a.Type
	}
	data := emailData{
		Title: title, Content: a.Message, Severity: a.Severity,
		Since: formatTime(a.CreatedAt), Tool: text.tool, Hub: hub, Color: "#b00020",
	}
	if a.Machine != nil {
		data.Machine = *a.Machine
	}
	if a.ResolvedAt != nil {
		data.ResolvedAt = formatTime(*a.ResolvedAt)
		data.Duration = formatDuration(a.ResolvedAt.Sub(a.CreatedAt))
	}
	if a.Severity == SeverityWarning {
		data.Color = "#8a5a00"
	}
	return render(ev.Event, data)
}
