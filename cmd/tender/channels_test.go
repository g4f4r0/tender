package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/notifications/channels"
)

// The tests' fake receivers listen on loopback, and stand in for Slack, which only tests may do.
// Every command sends testToken, the API token schema puts in each test database.
func TestMain(m *testing.M) {
	channels.AllowLocalForTests(true)
	if err := os.Setenv(envToken, testToken); err != nil {
		panic(err)
	}
	// No test reads the API token file of whoever runs them.
	config, err := os.MkdirTemp("", "tender-config-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", config); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(config)
	os.Exit(code)
}

// The channels commands against a real hub, with a fake Slack and a fake webhook receiver.
func TestChannelsCommands(t *testing.T) {
	box := &mailbox{}
	hub := httptest.NewServer(newHub(schema(t), testKey, box, quiet))
	defer hub.Close()
	slackStatus := 200
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(slackStatus)
	}))
	defer fake.Close()
	a := &answers{}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runChannels(append(args, "--hub", hub.URL), session{out: &out, prompt: a.ask, now: time.Now})
		return out.String(), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("tender channels %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}

	if out := must("list"); !strings.HasPrefix(out, "No channels yet, so notifications are only recorded.") {
		t.Errorf("empty list = %q", out)
	}
	if out := must("add", "ops", "--type", "email", "--to", "ops@example.com"); out !=
		"Added channel ops. It gets the notifications of * from warning up. Check that it arrives with: tender channels test ops\n" {
		t.Errorf("add email = %q", out)
	}
	// A Slack URL is asked for with typing hidden, or read from a file; never taken on the command line.
	slackURL := fake.URL + "/services/T0/B0/SLACK-SECRET-KEY"
	a.left = []string{slackURL}
	must("add", "chat", "--type", "slack", "--min-severity", "critical")
	if len(a.labels) != 1 || a.labels[0] != "Slack webhook URL" {
		t.Errorf("asked %q", a.labels)
	}
	out := must("add", "hook", "--type", "webhook", "--url", fake.URL+"/in", "--events", "service.*, backup.failed")
	if !strings.Contains(out, "It gets the notifications of service.*,backup.failed from warning up.") {
		t.Errorf("add webhook = %q", out)
	}
	if _, err := run("add", "x", "--type", "email", "--to", "x@example.com", "--events", ","); err == nil || !strings.Contains(err.Error(), "at least one pattern") {
		t.Errorf("empty --events: %v", err)
	}
	if _, err := run("add", "x", "--type", "email", "--to", "x@example.com", "--events", "nope"); err == nil || !strings.Contains(err.Error(), "events") {
		t.Errorf("bad --events: %v", err)
	}
	if !strings.Contains(out, "Its signing secret, made by the hub and shown only now:\n  whsec_") {
		t.Errorf("add webhook = %q", out)
	}
	if _, err := run("add", "x", "--type", "slack"); err == nil || !strings.Contains(err.Error(), "--webhook-url-file") {
		t.Errorf("slack without a terminal: %v", err)
	}
	if _, err := run("add", "x"); err == nil || !strings.Contains(err.Error(), "needs --type") {
		t.Errorf("no type: %v", err)
	}
	if _, err := run("add", "x", "--type", "exec", "--command", "../sh"); err == nil || !strings.Contains(err.Error(), "no slash") {
		t.Errorf("bad exec: %v", err)
	}

	list := must("list")
	for _, want := range []string{"NAME   TYPE      SENDS TO", "EVENTS", "ops    email     ops@example.com", "chat   slack     Slack webhook (secret)", "critical",
		"hook   webhook   " + fake.URL + "/in", "service.*,backup.failed"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}
	show := must("show", "chat")
	if !strings.Contains(show, "webhook_url    set (secret, never shown)") || strings.Contains(show, "SLACK-SECRET") {
		t.Errorf("show:\n%s", show)
	}

	if out := must("update", "ops", "--min-severity", "critical", "--off"); out != "Updated channel ops: off, the notifications of * from critical up.\n" {
		t.Errorf("update = %q", out)
	}
	if out := must("update", "hook", "--events", "*"); !strings.Contains(out, "the notifications of * from warning up") {
		t.Errorf("update events = %q", out)
	}
	newURL := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(newURL, []byte(fake.URL+"/services/NEW-KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	must("update", "chat", "--webhook-url-file", newURL)
	if _, err := run("update", "ops"); err == nil || !strings.Contains(err.Error(), "needs something to change") {
		t.Errorf("update with nothing: %v", err)
	}
	if _, err := run("update", "ops", "--on", "--off"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("--on --off: %v", err)
	}

	// ops is off, so the test goes to chat and hook.
	out = must("test")
	if !strings.Contains(out, "chat      delivered") || !strings.Contains(out, "hook      delivered") || strings.Contains(out, "ops") || len(box.to) != 0 {
		t.Errorf("test:\n%s", out)
	}
	slackStatus = 404
	out, err := run("test", "chat")
	if !errors.Is(err, errTestFailed) || !strings.Contains(out, "failed: HTTP 404 Not Found") || strings.Contains(out, "NEW-KEY") {
		t.Errorf("failing test: %v\n%s", err, out)
	}
	if out := must("test", "ops"); !strings.Contains(out, "ops       delivered") || len(box.to) != 1 {
		t.Errorf("test ops:\n%s", out)
	}

	if out := must("remove", "chat"); out != "Removed channel chat. It gets no more notifications.\n" {
		t.Errorf("remove = %q", out)
	}
	if _, err := run("remove", "chat"); err == nil || !strings.Contains(err.Error(), `Channel "chat" doesn't exist.`) {
		t.Errorf("remove again: %v", err)
	}
}
