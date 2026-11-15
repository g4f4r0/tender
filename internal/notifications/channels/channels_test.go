package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func event() Event {
	machine, service := "apps", "web"
	return Event{Version: Version, Event: EventOpened, Hub: "https://hub.example.com", Alert: Alert{
		ID: 12, Type: "service_down", Severity: "critical", Machine: &machine, Service: &service,
		Message: "The service web on apps stopped with exit code 1.", CreatedAt: time.Date(2026, 11, 15, 3, 31, 0, 0, time.UTC),
	}}
}

// receiver is a fake endpoint that answers status with body and keeps what it got.
type receiver struct {
	*httptest.Server
	status int
	answer string
	body   []byte
	header http.Header
}

func newReceiver(t *testing.T, status int, answer string) *receiver {
	r := &receiver{status: status, answer: answer}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.body, _ = io.ReadAll(req.Body)
		r.header = req.Header.Clone()
		w.WriteHeader(r.status)
		_, _ = io.WriteString(w, r.answer)
	}))
	t.Cleanup(r.Close)
	return r
}

const secret = "whsec_test-secret-of-enough-length"

func TestWebhookSignsTheEventContract(t *testing.T) {
	r := newReceiver(t, 204, "")
	at := time.Unix(1794751710, 0)
	w := Webhook{Now: func() time.Time { return at }}
	if err := w.Send(context.Background(), Config{"url": r.URL + "/hook?x=1", "secret": secret}, event()); err != nil {
		t.Fatal(err)
	}
	// The body is the contract, field for field.
	var got map[string]any
	if err := json.Unmarshal(r.body, &got); err != nil {
		t.Fatal(err)
	}
	alert := got["alert"].(map[string]any)
	if got["version"] != 1.0 || got["event"] != "opened" || got["hub"] != "https://hub.example.com" || len(got) != 4 {
		t.Errorf("event: %s", r.body)
	}
	for _, k := range []string{"id", "type", "severity", "machine", "service", "message", "created_at", "resolved_at"} {
		if _, ok := alert[k]; !ok {
			t.Errorf("alert has no %s: %s", k, r.body)
		}
	}
	if len(alert) != 8 || alert["resolved_at"] != nil || alert["created_at"] != "2026-11-15T03:31:00Z" {
		t.Errorf("alert: %v", alert)
	}
	// The signature verifies as the docs say: HMAC-SHA256 of "<t>.<body>".
	sig := r.header.Get("Tender-Signature")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("1794751710." + string(r.body)))
	if want := "t=1794751710,v1=" + hex.EncodeToString(mac.Sum(nil)); sig != want {
		t.Errorf("signature %q, want %q", sig, want)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}
}

func TestWebhookStatuses(t *testing.T) {
	for _, c := range []struct {
		status    int
		permanent bool
	}{{200, false}, {500, false}, {503, false}, {429, false}, {400, true}, {404, true}, {410, true}, {302, false}} {
		long := strings.Repeat("x", 3000)
		r := newReceiver(t, c.status, "nope "+long)
		err := (Webhook{}).Send(context.Background(), Config{"url": r.URL, "secret": secret}, event())
		switch {
		case c.status/100 == 2:
			if err != nil {
				t.Errorf("%d: %v, want delivered", c.status, err)
			}
			continue
		case err == nil:
			t.Errorf("%d: delivered", c.status)
			continue
		}
		if IsPermanent(err) != c.permanent {
			t.Errorf("%d: permanent %v, want %v", c.status, IsPermanent(err), c.permanent)
		}
		if !strings.Contains(err.Error(), "answered "+itoa(c.status)+": nope") || len(err.Error()) > 1100 {
			t.Errorf("%d: error %d bytes: %.80s", c.status, len(err.Error()), err)
		}
		if strings.Contains(err.Error(), r.URL) {
			t.Errorf("%d: the error shows the URL: %v", c.status, err)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestWebhookTimeoutAndUnreachable(t *testing.T) {
	old := SendTimeout
	SendTimeout = 200 * time.Millisecond
	defer func() { SendTimeout = old }()
	stop := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-stop }))
	defer slow.Close()
	defer close(stop)
	start := time.Now()
	err := (Webhook{}).Send(context.Background(), Config{"url": slow.URL, "secret": secret}, event())
	if err == nil || IsPermanent(err) || !strings.Contains(err.Error(), "no answer within") || time.Since(start) > 2*time.Second {
		t.Errorf("slow: %v after %s", err, time.Since(start))
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	err = (Slack{}).Send(context.Background(), Config{"webhook_url": gone.URL + "/services/T000/B000/KEY-IN-PATH"}, event())
	if err == nil || IsPermanent(err) || strings.Contains(err.Error(), "KEY-IN-PATH") {
		t.Errorf("unreachable: %v, want a retry that doesn't show the URL", err)
	}
}

func TestWebhookCheckAndMake(t *testing.T) {
	cfg := Config{"url": "https://example.com/hook"}
	made, err := Webhook{}.Make(cfg)
	if err != nil || len(made) != 1 || !strings.HasPrefix(cfg["secret"], "whsec_") || len(cfg["secret"]) < 40 {
		t.Fatalf("make: %v %v %q", made, err, cfg["secret"])
	}
	if err := (Webhook{}).Check(cfg); err != nil {
		t.Error(err)
	}
	if _, err := (Webhook{}).Make(Config{"url": "https://example.com", "secret": "mine"}); err == nil {
		t.Error("a secret in the create was taken")
	}
	bad := []Config{{"url": "ftp://x", "secret": secret}, {"url": "https://", "secret": secret}, {"url": "https://x", "secret": "short"}}
	for _, bad := range bad {
		if (Webhook{}).Check(bad) == nil {
			t.Errorf("%v passed", bad)
		}
	}
}

func TestSlackMessage(t *testing.T) {
	r := newReceiver(t, 200, "ok")
	ev := event()
	ev.Alert.Message = "a <b> & c"
	if err := (Slack{}).Send(context.Background(), Config{"webhook_url": r.URL}, ev); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(r.body, &got); err != nil {
		t.Fatal(err)
	}
	want := "*Critical: service_down on apps* (opened)\na &lt;b&gt; &amp; c\nAlert 12, open since 2026-11-15 03:31 UTC. Tender hub: https://hub.example.com"
	if got["text"] != want || len(got) != 1 {
		t.Errorf("text %q\nwant %q", got["text"], want)
	}
	r.status, r.answer = 403, "invalid_token"
	err := (Slack{}).Send(context.Background(), Config{"webhook_url": r.URL}, ev)
	if !IsPermanent(err) || !strings.Contains(err.Error(), "invalid_token") {
		t.Errorf("403: %v", err)
	}
	ev.Event = EventTest
	if !strings.Contains(SlackText(ev), "This is a test") {
		t.Error(SlackText(ev))
	}
}

// adapter writes a program into dir.
func adapter(t *testing.T, dir, name, script string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, name), mode); err != nil {
		t.Fatal(err)
	}
}

func TestExecPathRules(t *testing.T) {
	dir := t.TempDir()
	e := Exec{Dir: dir, AnyOwner: true}
	adapter(t, dir, "good", "cat >/dev/null\n", 0o755)
	adapter(t, dir, "notexec", "", 0o644)
	adapter(t, dir, "writable", "", 0o777)
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.Check(Config{"command": "good"}); err != nil {
		t.Errorf("good: %v", err)
	}
	for name, want := range map[string]string{
		"":            "must be the name of a program",
		"../good":     "no slash",
		"a/b":         "no slash",
		"..":          "no slash",
		"a..b":        "no slash",
		"/bin/sh":     "no slash",
		"-rf":         "must be the name",
		"missing":     "there is no",
		"notexec":     "may not run",
		"writable":    "can be changed by others",
		"folder":      "is not a file",
		"good --flag": "must be the name",
	} {
		err := e.Check(Config{"command": name})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", name, err, want)
		}
	}
	// Without AnyOwner, a program must be root's. The test doesn't run as root.
	if os.Getuid() != 0 {
		err := Exec{Dir: dir}.Check(Config{"command": "good"})
		if err == nil || !strings.Contains(err.Error(), "doesn't belong to root") {
			t.Errorf("not root's: %v", err)
		}
	}
	if (Exec{}).dir() != "/etc/tender/adapters" {
		t.Error("the default folder moved")
	}
}

func TestExecRuns(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "got")
	e := Exec{Dir: dir, AnyOwner: true}
	t.Setenv("TENDER_ENCRYPTION_KEY", "must-not-leak")
	adapter(t, dir, "save", "cat >"+out+"\nenv >>"+out+"\n", 0o755)
	if err := e.Send(context.Background(), Config{"command": "save"}, event()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	first, _, _ := strings.Cut(string(got), "\n")
	var ev Event
	if err := json.Unmarshal([]byte(first), &ev); err != nil || ev.Alert.ID != 12 || ev.Version != 1 {
		t.Errorf("stdin: %v %q", err, first)
	}
	if strings.Contains(string(got), "must-not-leak") {
		t.Error("the hub's environment reached the adapter")
	}

	adapter(t, dir, "fails", "echo to stdout\necho 'no route to pager' >&2\nexit 3\n", 0o755)
	err := e.Send(context.Background(), Config{"command": "fails"}, event())
	if err == nil || IsPermanent(err) || err.Error() != "fails exited with status 3: no route to pager" {
		t.Errorf("fails: %v", err)
	}

	// The output kept is capped; a flood doesn't hold the hub up.
	adapter(t, dir, "flood", "head -c 5000000 /dev/zero | tr '\\0' x\nexit 1\n", 0o755)
	err = e.Send(context.Background(), Config{"command": "flood"}, event())
	if err == nil || len(err.Error()) > 1100 {
		t.Errorf("flood: %d bytes", len(err.Error()))
	}
	var c capped
	_, _ = c.Write(make([]byte, 100<<10))
	_, _ = c.Write([]byte("more"))
	if c.Len() != 64<<10 {
		t.Errorf("capped at %d", c.Len())
	}

	old := SendTimeout
	SendTimeout = 300 * time.Millisecond
	defer func() { SendTimeout = old }()
	adapter(t, dir, "hangs", "sleep 30 &\nsleep 30\n", 0o755)
	start := time.Now()
	err = e.Send(context.Background(), Config{"command": "hangs"}, event())
	if err == nil || !strings.Contains(err.Error(), "didn't finish within") || time.Since(start) > 3*time.Second {
		t.Errorf("hangs: %v after %s", err, time.Since(start))
	}
}
