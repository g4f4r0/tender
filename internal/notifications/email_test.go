package notifications

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/notifications/channels"
)

// received is what the fake SMTP server was given.
type received struct {
	auth  string // the AUTH line after "AUTH PLAIN "
	from  string
	rcpts []string
	data  string
}

// fakeSMTP serves TLS from the first byte with a self-signed certificate for 127.0.0.1. It accepts
// AUTH PLAIN with the password "right" and sends each accepted message on got.
type fakeSMTP struct {
	port int
	pool *x509.CertPool
	got  chan received
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake smtp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	s := &fakeSMTP{port: l.Addr().(*net.TCPAddr).Port, pool: pool, got: make(chan received, 10)}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *fakeSMTP) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	tp := textproto.NewConn(c)
	say := func(line string) { _ = tp.PrintfLine("%s", line) }
	say("220 fake ESMTP")
	var r received
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		switch verb, rest, _ := strings.Cut(line, " "); strings.ToUpper(verb) {
		case "EHLO":
			say("250-fake")
			say("250 AUTH PLAIN")
		case "AUTH":
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(rest, "PLAIN "))
			if !strings.HasPrefix(rest, "PLAIN ") || !strings.HasSuffix(string(raw), "\x00right") {
				say("535 bad credentials")
				continue
			}
			r.auth = strings.TrimPrefix(rest, "PLAIN ")
			say("235 ok")
		case "MAIL":
			r.from = rest
			say("250 ok")
		case "RCPT":
			r.rcpts = append(r.rcpts, rest)
			say("250 ok")
		case "DATA":
			say("354 go on")
			b, err := io.ReadAll(tp.DotReader())
			if err != nil {
				return
			}
			r.data = string(b)
			say("250 queued")
			s.got <- r
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 no")
		}
	}
}

func (s *fakeSMTP) mailer(password string) *Mailer {
	m := NewMailer(config.SMTP{
		Host: "127.0.0.1", Port: s.port, Username: "api_token", Password: password,
		From: "tender <noreply@example.net>",
	})
	m.tls = &tls.Config{RootCAs: s.pool}
	return m
}

// testTo are the recipients of the mail the tests send.
var testTo = []string{"one@example.com", "two@example.com"}

var placeholder = regexp.MustCompile(`\{[a-z_]+\}|\{\{|<no value>`)

func TestSendEmails(t *testing.T) {
	s := newFakeSMTP(t)
	m := s.mailer("right")
	data := emailData{
		Machine: "code", Title: "backup failed", Severity: "critical", Color: "#b00020", Tool: "list_backup_history", Hub: "hub1",
		Content: "The last backup on code failed at 2026-11-14 02:05 UTC: <b>exit status 1</b> & more\nsecond line",
		Since:   "2026-11-14 02:05 UTC",
	}
	for _, c := range []struct{ kind, subject, text string }{
		{channels.PhaseOpened, "code: backup failed", "You'll get one more email when this is resolved.\n\nSent by the Tender hub on hub1.\n"},
		{channels.PhaseEscalated, "code: backup failed, now critical", "This got worse and is now critical, as of 2026-11-14 02:05 UTC."},
		{channels.PhaseResolved, "code: backup failed, resolved", "This is resolved as of 2026-11-14 02:05 UTC."},
	} {
		t.Run(c.kind, func(t *testing.T) {
			msg, err := render(c.kind, data)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Send(context.Background(), testTo, msg); err != nil {
				t.Fatal(err)
			}
			got := <-s.got
			subject, text, html := parseEmail(t, got)
			if subject != c.subject {
				t.Errorf("subject %q, want %q", subject, c.subject)
			}
			if !strings.Contains(text, c.text) {
				t.Errorf("text part lacks %q:\n%s", c.text, text)
			}
			if c.kind != channels.PhaseResolved && !strings.Contains(text, "exit status 1</b> & more\nsecond line") {
				t.Errorf("text part changed the content:\n%s", text)
			}
			for _, part := range []string{subject, text, html} {
				if placeholder.MatchString(part) {
					t.Errorf("placeholder left in %q", part)
				}
			}
			if c.kind != channels.PhaseResolved && !strings.Contains(html, "&lt;b&gt;exit status 1&lt;/b&gt; &amp; more") {
				t.Errorf("html part didn't escape the content:\n%s", html)
			}
			for _, bad := range []string{"http://", "https://", "<img", "<link", "<script"} {
				if strings.Contains(html, bad) {
					t.Errorf("html part contains %q", bad)
				}
			}
			if got.from != "FROM:<noreply@example.net>" || len(got.rcpts) != 2 || got.rcpts[1] != "TO:<two@example.com>" {
				t.Errorf("envelope %q to %q", got.from, got.rcpts)
			}
		})
	}
}

func TestSendTestEmailAndHeaders(t *testing.T) {
	s := newFakeSMTP(t)
	msg, err := TestMessage("hub1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.mailer("right").Send(context.Background(), testTo, msg); err != nil {
		t.Fatal(err)
	}
	got := <-s.got
	subject, text, html := parseEmail(t, got)
	if subject != "Tender test email" || text != "This is a test from the Tender hub on hub1. If you can read it, notification emails reach you.\n" ||
		!strings.Contains(html, "If you can read it, notification emails reach you.") {
		t.Errorf("got %q, %q, %q", subject, text, html)
	}
	if user := mustDecode(t, got.auth); user != "\x00api_token\x00right" {
		t.Errorf("auth = %q", user)
	}

	m, err := mail.ReadMessage(strings.NewReader(got.data))
	if err != nil {
		t.Fatal(err)
	}
	if d, err := m.Header.Date(); err != nil || time.Since(d) > time.Minute || time.Since(d) < -time.Minute {
		t.Errorf("Date = %q, %v", m.Header.Get("Date"), err)
	}
	if from, err := m.Header.AddressList("From"); err != nil || len(from) != 1 || from[0].Name != "tender" || from[0].Address != "noreply@example.net" {
		t.Errorf("From = %v, %v", from, err)
	}
	if to, err := m.Header.AddressList("To"); err != nil || len(to) != 2 || to[0].Address != "one@example.com" {
		t.Errorf("To = %v, %v", to, err)
	}
	if id := m.Header.Get("Message-ID"); !regexp.MustCompile(`^<[0-9a-f]{32}@example\.net>$`).MatchString(id) {
		t.Errorf("Message-ID = %q", id)
	}
	if m.Header.Get("MIME-Version") != "1.0" {
		t.Errorf("MIME-Version = %q", m.Header.Get("MIME-Version"))
	}
}

func TestSubjectIsEncodedWhenNotASCII(t *testing.T) {
	s := newFakeSMTP(t)
	if err := s.mailer("right").Send(context.Background(), testTo, Message{Subject: "café: not reporting", Text: "naïve café\n", HTML: "<p>naïve café</p>\n"}); err != nil {
		t.Fatal(err)
	}
	got := <-s.got
	m, err := mail.ReadMessage(strings.NewReader(got.data))
	if err != nil {
		t.Fatal(err)
	}
	if raw := m.Header.Get("Subject"); !strings.HasPrefix(raw, "=?utf-8?q?") {
		t.Errorf("raw subject %q, want an RFC 2047 word", raw)
	}
	subject, text, html := parseEmail(t, got)
	if subject != "café: not reporting" || text != "naïve café\n" || html != "<p>naïve café</p>\n" {
		t.Errorf("got %q, %q, %q", subject, text, html)
	}
}

func TestSendFailures(t *testing.T) {
	s := newFakeSMTP(t)
	msg := Message{Subject: "s", Text: "t", HTML: "h"}

	err := s.mailer("wrong-password-123").Send(context.Background(), testTo, msg)
	if err == nil || !strings.Contains(err.Error(), "smtp login") {
		t.Errorf("wrong password: %v", err)
	} else if strings.Contains(err.Error(), "wrong-password-123") {
		t.Errorf("error leaks the password: %v", err)
	}

	untrusting := s.mailer("right")
	untrusting.tls = nil // the system roots don't know the test certificate
	if err := untrusting.Send(context.Background(), testTo, msg); err == nil {
		t.Error("an untrusted certificate was accepted")
	}

	closed := s.mailer("right")
	closed.cfg.Port = 1
	if err := closed.Send(context.Background(), testTo, msg); err == nil || !strings.Contains(err.Error(), "connect to 127.0.0.1:"+strconv.Itoa(1)) {
		t.Errorf("closed port: %v", err)
	}
	select {
	case r := <-s.got:
		t.Errorf("a failed send delivered %+v", r)
	default:
	}
}

func mustDecode(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// parseEmail checks the structure of a received message and returns its decoded subject, text and HTML parts.
func parseEmail(t *testing.T, r received) (subject, text, html string) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(r.data))
	if err != nil {
		t.Fatal(err)
	}
	subject, err = new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("Content-Type = %q, %v", m.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var types []string
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if p.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
			t.Errorf("part encoding = %q", p.Header.Get("Content-Transfer-Encoding"))
		}
		b, err := io.ReadAll(quotedprintable.NewReader(bufio.NewReader(p)))
		if err != nil {
			t.Fatal(err)
		}
		ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		types = append(types, ct)
		// The wire has CRLF line ends; compare with LF.
		body := strings.ReplaceAll(string(b), "\r\n", "\n")
		switch ct {
		case "text/plain":
			text = body
		case "text/html":
			html = body
		}
	}
	if len(types) != 2 || types[0] != "text/plain" || types[1] != "text/html" {
		t.Fatalf("parts = %v, want text/plain then text/html", types)
	}
	return subject, text, html
}

func TestSendWithoutLogin(t *testing.T) {
	s := newFakeSMTP(t)
	m := s.mailer("")
	m.cfg.Username = ""
	msg, err := TestMessage("hub1")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Send(context.Background(), []string{"solo@example.com"}, msg); err != nil {
		t.Fatal(err)
	}
	got := <-s.got
	if got.auth != "" {
		t.Errorf("sent AUTH %q although the username is empty", got.auth)
	}
	if len(got.rcpts) != 1 || got.rcpts[0] != "TO:<solo@example.com>" {
		t.Errorf("recipients = %q, want only solo@example.com", got.rcpts)
	}
}
