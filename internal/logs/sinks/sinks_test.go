package sinks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/server"
)

func TestNames(t *testing.T) {
	if got := strings.Join(Names(), ","); got != "otlp,webhook" {
		t.Errorf("Names() = %s", got)
	}
}

func TestCheck(t *testing.T) {
	for _, c := range []struct {
		name, typ string
		config    Config
		says      string // "" means valid
	}{
		{"otlp", "otlp", Config{"endpoint": "https://otel.example.com/v1/logs", "headers": map[string]any{"Authorization": "Bearer x"}}, ""},
		{"otlp without headers", "otlp", Config{"endpoint": "http://10.0.0.5:4318/v1/logs"}, ""},
		{"otlp no endpoint", "otlp", Config{}, "config.endpoint is required"},
		{"otlp bad scheme", "otlp", Config{"endpoint": "ftp://x/v1/logs"}, "http or https URL"},
		{"otlp login in url", "otlp", Config{"endpoint": "https://u:p@x/v1/logs"}, "can't hold a login"},
		{"otlp bad header", "otlp", Config{"endpoint": "https://x/v1/logs", "headers": map[string]any{"Bad Name": "x"}}, "not an HTTP header name"},
		{"otlp header injection", "otlp", Config{"endpoint": "https://x/v1/logs", "headers": map[string]any{"X-A": "a\r\nX-B: b"}}, "line break"},
		{"otlp sets content-type", "otlp", Config{"endpoint": "https://x/v1/logs", "headers": map[string]any{"Content-Type": "text/plain"}}, "Tender sets it"},
		{"otlp header not text", "otlp", Config{"endpoint": "https://x/v1/logs", "headers": map[string]any{"X-A": 1}}, "must be string"},
		{"otlp unknown field", "otlp", Config{"endpoint": "https://x/v1/logs", "protocol": "grpc"}, "It takes endpoint, headers"},
		{"webhook", "webhook", Config{"url": "https://logs.example.com/in", "secret": "0123456789abcdef"}, ""},
		{"webhook short secret", "webhook", Config{"url": "https://logs.example.com/in", "secret": "short"}, "at least 16"},
		{"webhook no secret", "webhook", Config{"url": "https://logs.example.com/in"}, "at least 16"},
		{"webhook no url", "webhook", Config{"secret": "0123456789abcdef"}, "config.url is required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tp, _ := Get(c.typ)
			_, err := tp.Check(c.config)
			if c.says == "" {
				if err != nil {
					t.Fatalf("Check: %v", err)
				}
				return
			}
			var e *server.Error
			if !errors.As(err, &e) || e.Status != 422 || !strings.Contains(e.Detail, c.says) || !strings.HasSuffix(e.Detail, ".") {
				t.Errorf("Check = %v, want a 422 sentence containing %q", err, c.says)
			}
		})
	}
}

func TestSecrets(t *testing.T) {
	o, _ := Get("otlp")
	w, _ := Get("webhook")
	if !IsSecret(o, "headers") || IsSecret(o, "endpoint") || !IsSecret(w, "secret") || IsSecret(w, "url") {
		t.Error("wrong secrets")
	}
	if o.Generated() != "" || w.Generated() != "secret" {
		t.Error("only the webhook's secret is made by the hub")
	}
}

var records = []Record{
	{Time: time.Date(2026, 11, 15, 12, 0, 0, 123456789, time.UTC), Source: SourceJournal, Unit: "nginx.service", Priority: "warning", Message: "slow upstream"},
	{Time: time.Date(2026, 11, 15, 12, 0, 1, 0, time.UTC), Source: SourceService, Service: "web", Priority: "err", Message: "boom"},
	{Time: time.Date(2026, 11, 15, 12, 0, 2, 0, time.UTC), Source: SourceJournal, Unit: "nginx.service", Priority: "info", Message: "ok"},
}

// receiver is a fake sink: it keeps each request and answers with status.
type receiver struct {
	status int
	got    []*http.Request
	bodies [][]byte
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.got = append(r.got, req)
	r.bodies = append(r.bodies, b)
	w.WriteHeader(r.status)
	_, _ = w.Write([]byte("echo: " + string(b)))
}

func newSender(t *testing.T, typ string, c Config) Sender {
	t.Helper()
	tp, _ := Get(typ)
	clean, err := tp.Check(c)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tp.New(clean, "apps")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The OTLP body is OTLP/HTTP JSON: one resource per unit or service with host.name and
// service.name, and each record's time in nanoseconds as a string, severity and body.
func TestOTLPSend(t *testing.T) {
	rec := &receiver{status: 200}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	s := newSender(t, "otlp", Config{"endpoint": srv.URL + "/v1/logs", "headers": map[string]any{"Authorization": "Bearer tok"}})
	if err := s.Send(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	r := rec.got[0]
	if r.URL.Path != "/v1/logs" || r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request %s %v", r.URL.Path, r.Header)
	}
	var body struct {
		ResourceLogs []struct {
			Resource struct {
				Attributes []struct {
					Key   string
					Value struct{ StringValue string }
				}
			}
			ScopeLogs []struct {
				LogRecords []struct {
					TimeUnixNano   string
					SeverityNumber int
					SeverityText   string
					Body           struct{ StringValue string }
				}
			}
		}
	}
	if err := json.Unmarshal(rec.bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	if len(body.ResourceLogs) != 2 {
		t.Fatalf("%d resources, want one for nginx.service and one for web: %s", len(body.ResourceLogs), rec.bodies[0])
	}
	attrs := map[string]string{}
	for _, a := range body.ResourceLogs[0].Resource.Attributes {
		attrs[a.Key] = a.Value.StringValue
	}
	if attrs["host.name"] != "apps" || attrs["service.name"] != "nginx.service" {
		t.Errorf("attributes %v", attrs)
	}
	lr := body.ResourceLogs[0].ScopeLogs[0].LogRecords
	if len(lr) != 2 || lr[0].TimeUnixNano != strconv.FormatInt(records[0].Time.UnixNano(), 10) || lr[0].SeverityNumber != 13 || lr[0].SeverityText != "warning" || lr[0].Body.StringValue != "slow upstream" {
		t.Errorf("records %+v", lr)
	}
	web := body.ResourceLogs[1].ScopeLogs[0].LogRecords[0]
	if web.SeverityNumber != 17 || web.Body.StringValue != "boom" {
		t.Errorf("web %+v", web)
	}
}

// The webhook body is the log contract, signed with HMAC-SHA256 over "<t>.<body>".
func TestWebhookSend(t *testing.T) {
	rec := &receiver{status: 204}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	secret := "0123456789abcdef0123"
	s := newSender(t, "webhook", Config{"url": srv.URL + "/in", "secret": secret})
	if err := s.Send(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	body := rec.bodies[0]
	sig := rec.got[0].Header.Get("Tender-Signature")
	ts, mac, ok := strings.Cut(strings.TrimPrefix(sig, "t="), ",v1=")
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(ts + "." + string(body)))
	if !ok || mac != hex.EncodeToString(h.Sum(nil)) {
		t.Errorf("signature %q doesn't verify", sig)
	}
	var b Batch
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 || b.Machine != "apps" || len(b.Records) != 3 || b.Records[1].Service != "web" || b.Records[0].Unit != "nginx.service" {
		t.Errorf("batch %+v", b)
	}
	if !strings.Contains(string(body), `"time":"2026-11-15T12:00:00.123456789Z","source":"journal","unit":"nginx.service","priority":"warning","message":"slow upstream"`) ||
		strings.Contains(string(body), `"service":""`) {
		t.Errorf("body %s", body)
	}
	if strings.Contains(string(body), secret) {
		t.Error("the secret is in the body")
	}
}

// A 4xx that can't change is permanent; 429, 5xx and no answer are worth another try. No error
// holds the sink's answer, which can echo the logs, nor the URL, which can hold a key.
func TestSendErrors(t *testing.T) {
	for _, c := range []struct {
		status    int
		permanent bool
	}{{400, true}, {413, true}, {422, true}, {401, false}, {404, false}, {429, false}, {500, false}, {503, false}} {
		rec := &receiver{status: c.status}
		srv := httptest.NewServer(rec)
		s := newSender(t, "webhook", Config{"url": srv.URL + "/in?key=urlsecret", "secret": "0123456789abcdef"})
		err := s.Send(context.Background(), records)
		srv.Close()
		var e *Error
		if !errors.As(err, &e) || e.Status != c.status || e.Permanent != c.permanent {
			t.Errorf("%d: %v (%+v), want permanent %v", c.status, err, e, c.permanent)
			continue
		}
		if strings.Contains(err.Error(), "slow upstream") || strings.Contains(err.Error(), "urlsecret") {
			t.Errorf("%d: the error holds the logs or the URL: %v", c.status, err)
		}
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	s := newSender(t, "otlp", Config{"endpoint": url + "/v1/logs?key=urlsecret"})
	err := s.Send(context.Background(), records)
	var e *Error
	if !errors.As(err, &e) || e.Permanent || e.Status != 0 || strings.Contains(err.Error(), "urlsecret") || !strings.Contains(err.Error(), "didn't answer") {
		t.Errorf("dead sink: %v", err)
	}
}

// A sink that doesn't answer in time fails the send, which is tried again.
func TestSendTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	s := newSender(t, "otlp", Config{"endpoint": srv.URL + "/v1/logs"})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := s.Send(ctx, records)
	var e *Error
	if !errors.As(err, &e) || e.Permanent {
		t.Errorf("timeout: %v", err)
	}
}

// A sink that redirects fails the send: a 301 must not turn the POST into a GET that looks
// delivered, and a 307 must not carry the logs and the headers to another host.
func TestSendFollowsNoRedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	for _, status := range []int{301, 302, 307, 308} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/in", status)
		}))
		s := newSender(t, "otlp", Config{"endpoint": srv.URL + "/v1/logs", "headers": map[string]any{"Authorization": "Bearer k"}})
		err := s.Send(context.Background(), records)
		srv.Close()
		var e *Error
		if !errors.As(err, &e) || e.Status != status || e.Permanent {
			t.Errorf("%d: %v, want a failed send that is tried again", status, err)
		}
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the redirect target got %d requests", n)
	}
}
