package notifications

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/g4f4r0/tender/internal/config"
)

//go:embed templates/*.tmpl
var templateFiles embed.FS

var (
	textTemplates = texttemplate.Must(texttemplate.ParseFS(templateFiles, "templates/email.txt.tmpl"))
	htmlTemplates = htmltemplate.Must(htmltemplate.ParseFS(templateFiles, "templates/email.html.tmpl"))
)

// The kinds of email, which name their templates.
const (
	kindOpened    = "opened"
	kindEscalated = "escalated"
	kindResolved  = "resolved"
	kindTest      = "test"
)

// typeText is the title and the MCP tool to look closer with, per notification type.
var typeText = map[string]struct{ title, tool string }{
	TypeMachineSilent: {"not reporting", "list_machines"},
	TypeBackupFailed:  {"backup failed", "list_backup_history"},
	TypeBackupStale:   {"no recent backup", "get_backup_status"},
	TypeDiskFull:      {"disk almost full", "get_machine_usage"},

	// A service alert's title follows the service's name: "web-1 stopped".
	TypeServiceDown:       {"stopped", "list_services"},
	TypeServiceRestarting: {"keeps restarting", "list_services"},
	TypeServiceUnhealthy:  {"unhealthy", "list_services"},
	TypeServiceMemoryHigh: {"memory high", "list_services"},
	TypeDockerUnreachable: {"Docker unreachable", "list_services"},

	TypeLogShippingFailing: {"log shipping failing", "list_log_sinks"},
}

// Message is one email, before it gets its headers.
type Message struct {
	Subject string
	Text    string
	HTML    string
}

// emailData fills the templates. Times and durations arrive as text.
type emailData struct {
	Subject    string
	Machine    string
	Title      string
	Content    string
	Severity   string
	Color      string // of the severity word in the HTML part
	Since      string
	Duration   string
	ResolvedAt string
	Tool       string
	Hub        string
}

// render fills the templates of one kind of email.
func render(kind string, d emailData) (Message, error) {
	var subject, text, page bytes.Buffer
	if err := textTemplates.ExecuteTemplate(&subject, kind+".subject", d); err != nil {
		return Message{}, err
	}
	d.Subject = subject.String()
	if err := textTemplates.ExecuteTemplate(&text, kind, d); err != nil {
		return Message{}, err
	}
	if err := htmlTemplates.ExecuteTemplate(&page, kind, d); err != nil {
		return Message{}, err
	}
	return Message{Subject: d.Subject, Text: text.String(), HTML: page.String()}, nil
}

// TestMessage is the email POST /v1/notifications/test and the send_test_email tool send.
func TestMessage(hub string) (Message, error) { return render(kindTest, emailData{Hub: hub}) }

// Mailer sends email over SMTP with TLS from the first byte (port 465).
type Mailer struct {
	cfg config.SMTP
	tls *tls.Config // nil means the system's roots; tests trust their own certificate
}

// NewMailer returns a mailer for cfg. The password is never logged or returned in errors.
func NewMailer(cfg config.SMTP) *Mailer { return &Mailer{cfg: cfg} }

const smtpTimeout = 30 * time.Second

// Send delivers msg to every address in to, which are bare addresses. The whole exchange takes at
// most 30 seconds. It logs in only when the config has a username.
func (m *Mailer) Send(ctx context.Context, to []string, msg Message) (err error) {
	body, err := m.build(msg, to, time.Now())
	if err != nil {
		return err
	}
	from, _ := mail.ParseAddress(m.cfg.From) // checked when the config loaded

	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()
	dialer := tls.Dialer{Config: m.tls}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port)))
	if err != nil {
		return fmt.Errorf("connect to %s:%d: %w", m.cfg.Host, m.cfg.Port, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return errors.Join(err, conn.Close())
		}
	}
	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return errors.Join(fmt.Errorf("smtp greeting: %w", err), conn.Close())
	}
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()

	if m.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
			return fmt.Errorf("smtp login: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp sender: %w", err)
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return fmt.Errorf("smtp recipient %s: %w", addr, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return errors.Join(fmt.Errorf("smtp data: %w", err), w.Close())
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	return c.Quit()
}

// build returns the whole message: headers, then a multipart/alternative body with a text part
// and an HTML part, both quoted-printable.
func (m *Mailer) build(msg Message, recipients []string, now time.Time) ([]byte, error) {
	from, err := mail.ParseAddress(m.cfg.From)
	if err != nil {
		return nil, err
	}
	to := make([]string, len(recipients))
	for i, t := range recipients {
		a, err := mail.ParseAddress(t)
		if err != nil {
			return nil, err
		}
		to[i] = a.String()
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	domain := from.Address[strings.LastIndex(from.Address, "@")+1:]

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, p := range []struct{ contentType, content string }{
		{"text/plain; charset=utf-8", msg.Text},
		{"text/html; charset=utf-8", msg.HTML},
	} {
		w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {p.contentType}, "Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(p.content)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	for _, h := range [][2]string{
		{"Date", now.Format(time.RFC1123Z)},
		{"From", from.String()},
		{"To", strings.Join(to, ", ")},
		{"Subject", mime.QEncoding.Encode("utf-8", msg.Subject)},
		{"Message-ID", "<" + hex.EncodeToString(id) + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", "multipart/alternative; boundary=" + mw.Boundary()},
	} {
		out.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	out.WriteString("\r\n")
	out.Write(body.Bytes())
	return out.Bytes(), nil
}
