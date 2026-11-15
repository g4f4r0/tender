package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestReadLine(t *testing.T) {
	for in, want := range map[string]string{"token\n": "token", "token\r\nmore\n": "token", "token": "token", "": ""} {
		if got, err := readLine(strings.NewReader(in)); err != nil || got != want {
			t.Errorf("readLine(%q) = %q, %v", in, got, err)
		}
	}
}

func TestInstallUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil, {"bogus"}, {"daemon"}, {"daemon", "--hub", "https://h.example.net"}, {"hub", "extra"}, {"refresh", "extra"},
		{"credential", "r2.password"}, // removed: the hub keeps the destination's secrets now
		{"hub", "--smtp-port", "70000"}, {"hub", "--smtp-password-file", "x"},
	} {
		if err := runInstall(args, strings.NewReader(""), session{out: &strings.Builder{}}); err == nil {
			t.Errorf("runInstall(%v) accepted", args)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(strings.NewReader("token\n")) {
		t.Error("a reader is not a terminal")
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = null.Close() }()
	if !isTerminal(null) {
		t.Error("/dev/null should count as no input")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if isTerminal(r) {
		t.Error("a pipe is not a terminal")
	}
}

// The hub check of "install daemon" passes with the machine's own token and names the fix for a
// wrong token, a wrong machine and an address nobody answers on, against the real hub.
func TestCheckHub(t *testing.T) {
	hub := httptest.NewServer(newHub(schema(t), testKey, nil, quiet))
	t.Cleanup(hub.Close)
	var machine struct{ Token string }
	status, body := restCall(t, hub.URL, "POST", "/v1/machines", "", map[string]any{"name": "code"})
	if status != 201 || json.Unmarshal([]byte(body), &machine) != nil || machine.Token == "" {
		t.Fatalf("create machine: %d %s", status, body)
	}
	if status, body := restCall(t, hub.URL, "POST", "/v1/machines", "", map[string]any{"name": "apps"}); status != 201 {
		t.Fatalf("create second machine: %d %s", status, body)
	}
	ctx := context.Background()
	if err := checkHub(ctx, hub.URL, "code", machine.Token); err != nil {
		t.Fatalf("own token: %v", err)
	}
	status, body = restCall(t, hub.URL, "GET", "/v1/machines/code", "", nil)
	if status != 200 || !strings.Contains(body, `"last_seen":"`) {
		t.Errorf("the check's report did not reach the hub: %d %s", status, body)
	}

	dead := httptest.NewServer(nil)
	deadURL := dead.URL
	dead.Close()
	for _, c := range []struct{ name, hub, machine, token, want string }{
		{"wrong token", hub.URL, "code", "not-a-token", `does not accept this token, so nothing was changed. Make a new one with "tender machines rotate-token code"`},
		{"another machine", hub.URL, "apps", machine.Token, `has no machine "apps" for this token`},
		{"unknown machine", hub.URL, "ci", machine.Token, `has no machine "ci" for this token`},
		{"nobody answers", deadURL, "code", machine.Token, "cannot reach the hub at " + deadURL + ", so nothing was changed"},
	} {
		err := checkHub(ctx, c.hub, c.machine, c.token)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), machine.Token) {
			t.Errorf("%s: the error shows the token", c.name)
		}
	}
}
