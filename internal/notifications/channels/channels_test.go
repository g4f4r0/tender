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
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func event() Event {
	machine, service := "apps", "web"
	return Event{Version: Version, Event: "service.down", Hub: "https://hub.example.com", Notification: Notification{
		ID: 12, Event: "service.down", Severity: "critical", Machine: &machine, Service: &service,
		Message: "The service web on apps stopped with exit code 1.", CreatedAt: time.Date(2026, 11, 15, 3, 31, 0, 0, time.UTC),
	}}
}

// The tests' fake endpoints listen on loopback, which only tests may reach.
func TestMain(m *testing.M) {
	AllowLocalForTests(true)
	os.Exit(m.Run())
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
	n := got["notification"].(map[string]any)
	if got["version"] != 1.0 || got["event"] != "service.down" || got["hub"] != "https://hub.example.com" || len(got) != 4 {
		t.Errorf("event: %s", r.body)
	}
	for _, k := range []string{"id", "event", "severity", "machine", "service", "channel", "message", "created_at"} {
		if _, ok := n[k]; !ok {
			t.Errorf("notification has no %s: %s", k, r.body)
		}
	}
	if len(n) != 8 || n["channel"] != nil || n["event"] != "service.down" || n["created_at"] != "2026-11-15T03:31:00Z" {
		t.Errorf("notification: %v", n)
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
		// The error is the status alone: the answer's body is never kept or shown.
		if want := "HTTP " + itoa(c.status) + " " + http.StatusText(c.status); err.Error() != want {
			t.Errorf("%d: error %.80q, want %q", c.status, err, want)
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
	ev.Notification.Message = "a <b> & c"
	if err := (Slack{}).Send(context.Background(), Config{"webhook_url": r.URL}, ev); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(r.body, &got); err != nil {
		t.Fatal(err)
	}
	want := "*Critical: service.down on apps*\na &lt;b&gt; &amp; c\nNotification 12, 2026-11-15 03:31 UTC. Tender hub: https://hub.example.com"
	if got["text"] != want || len(got) != 1 {
		t.Errorf("text %q\nwant %q", got["text"], want)
	}
	r.status, r.answer = 403, "invalid_token"
	err := (Slack{}).Send(context.Background(), Config{"webhook_url": r.URL}, ev)
	if !IsPermanent(err) || err.Error() != "HTTP 403 Forbidden" {
		t.Errorf("403: %v", err)
	}
	ev.Event = EventTest
	if !strings.Contains(SlackText(ev), "This is a test") {
		t.Error(SlackText(ev))
	}
}

// adaptersDir is an adapters folder that only its owner can change.
func adaptersDir(t *testing.T) string {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
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
	dir := adaptersDir(t)
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
	dir := adaptersDir(t)
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
	if err := json.Unmarshal([]byte(first), &ev); err != nil || ev.Notification.ID != 12 || ev.Version != 1 {
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

// local turns the tests' loopback allowance off for one test.
func local(t *testing.T) {
	AllowLocalForTests(false)
	t.Cleanup(func() { AllowLocalForTests(true) })
}

func TestAddressRules(t *testing.T) {
	local(t)
	for addr, want := range map[string][2]bool{ // {over https, over plain http}
		"127.0.0.1":          {false, false},
		"::1":                {false, false},
		"::ffff:127.0.0.1":   {false, false},
		"169.254.169.254":    {false, false},
		"fe80::1":            {false, false},
		"224.0.0.1":          {false, false},
		"ff02::1":            {false, false},
		"0.0.0.0":            {false, false},
		"::":                 {false, false},
		"10.1.2.3":           {true, true},
		"172.16.0.9":         {true, true},
		"192.168.1.20":       {true, true},
		"fd12::5":            {true, true},
		"100.101.102.103":    {true, true},
		"fd7a:115c:a1e0::53": {true, true},
		"93.184.216.34":      {true, false},
		"2606:4700::1111":    {true, false},
	} {
		ip := netip.MustParseAddr(addr)
		if got := refused(ip, false) == ""; got != want[0] {
			t.Errorf("%s over https: allowed %v, want %v", addr, got, want[0])
		}
		if got := refused(ip, true) == ""; got != want[1] {
			t.Errorf("%s over http: allowed %v, want %v", addr, got, want[1])
		}
	}
}

func TestWebhookRefusesLocalAddresses(t *testing.T) {
	local(t)
	r := newReceiver(t, 204, "")
	_, port, _ := strings.Cut(strings.TrimPrefix(r.URL, "http://"), ":")
	// The check is on the address a name resolves to when the hub dials, so a name that leads to
	// loopback is refused as surely as the address itself.
	for _, target := range []string{
		r.URL, "http://localhost:" + port, "https://localhost:" + port, "http://[::ffff:127.0.0.1]:" + port,
		"https://169.254.169.254/latest/meta-data", "http://93.184.216.34/hook",
	} {
		err := (Webhook{}).Send(context.Background(), Config{"url": target, "secret": secret}, event())
		if err == nil || !IsPermanent(err) || !strings.Contains(err.Error(), "doesn't send to") {
			t.Errorf("%s: %v, want a refusal", target, err)
		}
	}
	if r.body != nil {
		t.Errorf("the receiver got %s", r.body)
	}
	for _, bad := range []string{"http://127.0.0.1/x", "https://[::1]/x", "http://169.254.169.254/", "http://93.184.216.34/hook"} {
		if err := (Webhook{}).Check(Config{"url": bad, "secret": secret}); err == nil {
			t.Errorf("%s passed the check", bad)
		}
	}
	for _, good := range []string{"http://100.101.102.103:9000/hook", "http://192.168.1.20/hook", "https://example.com/hook"} {
		if err := (Webhook{}).Check(Config{"url": good, "secret": secret}); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	for _, c := range []*http.Client{httpsClient, plainClient} {
		if c.Transport.(*http.Transport).Proxy != nil {
			t.Error("a proxy could see the events")
		}
	}
}

func TestSlackNeedsSlacksHost(t *testing.T) {
	local(t)
	r := newReceiver(t, 200, "ok")
	if err := (Slack{}).Check(Config{"webhook_url": "https://hooks.slack.com/services/T000/B000/XXXX"}); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"http://hooks.slack.com/services/T/B/X", "https://hooks.slack.com.example.com/services/T/B/X", "https://example.com/services/T/B/X", r.URL} {
		if err := (Slack{}).Check(Config{"webhook_url": bad}); err == nil || !strings.Contains(err.Error(), "https://hooks.slack.com/") {
			t.Errorf("%s: %v", bad, err)
		}
	}
	err := (Slack{}).Send(context.Background(), Config{"webhook_url": r.URL}, event())
	if !IsPermanent(err) || r.body != nil {
		t.Errorf("send to %s: %v, got %s", r.URL, err, r.body)
	}
}

func TestExecFolderRules(t *testing.T) {
	dir := adaptersDir(t)
	adapter(t, dir, "good", "cat >/dev/null\n", 0o755)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	// AnyOwner relaxes only who owns the folder: one that others can write to is still refused.
	err := Exec{Dir: dir, AnyOwner: true}.Check(Config{"command": "good"})
	if err == nil || !strings.Contains(err.Error(), "can be changed by others") || !strings.Contains(err.Error(), "chmod go-w "+dir) {
		t.Errorf("open folder: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (Exec{Dir: dir, AnyOwner: true}).Check(Config{"command": "good"}); err != nil {
		t.Error(err)
	}
}

func TestExecDeliveredWithAChildLeftRunning(t *testing.T) {
	dir := adaptersDir(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	// The child keeps standard output open after the adapter exits 0.
	adapter(t, dir, "forks", "cat >/dev/null\nsleep 30 &\necho $! >"+pidFile+"\nexit 0\n", 0o755)
	err := Exec{Dir: dir, AnyOwner: true}.Send(context.Background(), Config{"command": "forks"}, event())
	if err != nil {
		t.Errorf("exit 0: %v, want delivered", err)
	}
	raw, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid <= 0 {
		t.Fatalf("pid %q", raw)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("the adapter's child still runs after the delivery")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
