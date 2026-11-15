package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/actions"
)

// cli runs a hub command against a real hub, answering prompts from a.
func cli(t *testing.T, hub string, a *answers, args ...string) (string, error) {
	t.Helper()
	t.Setenv("TENDER_HUB", hub)
	var out bytes.Buffer
	err := hubCommands[args[0]](args[1:], session{out: &out, prompt: a.ask, now: time.Now})
	return out.String(), err
}

// Every type can be added, listed, changed and tested from the command line, against the real hub.
func TestDestinationTypesThroughTheCLI(t *testing.T) {
	d := seeded(t)
	hub := httptest.NewServer(newHub(d, testKey, nil, quiet))
	t.Cleanup(hub.Close)
	as := newActions(d, testKey)
	var mu sync.Mutex
	var targets []string
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { // the daemon of g4f4r0-code: it is handed each test with its destination
		defer close(done)
		for ctx.Err() == nil {
			list, err := as.Wait(ctx, 1, time.Second, nil)
			if err != nil {
				return
			}
			for _, h := range list {
				mu.Lock()
				targets = append(targets, string(h.Target))
				mu.Unlock()
				if as.Claim(ctx, 1, h.ID) == nil {
					out := "Reached the repository.\n"
					_ = as.Finish(ctx, 1, h.ID, actions.Patch{Status: actions.StatusCompleted, Output: &out})
				}
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })

	pwFile := filepath.Join(t.TempDir(), "rest-password")
	if err := os.WriteFile(pwFile, []byte("marker-rest-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	none := &answers{}
	for _, c := range [][]string{
		{"destinations", "add", "nas", "--type", "sftp", "--host", "nas.example.com", "--port", "2222", "--user", "tender", "--path", "/srv/restic"},
		{"destinations", "add", "disk", "--type", "local", "--path", "/mnt/backup"},
		{"destinations", "add", "rs", "--type", "rest", "--url", "https://rest.example.com:8000", "--username", "apps", "--rest-password-file", pwFile},
		{"destinations", "add", "gd", "--type", "rclone", "--remote", "gdrive:tender"},
	} {
		if out, err := cli(t, hub.URL, none, c...); err != nil || !strings.Contains(out, "Added destination") || !strings.Contains(out, "shown only now") {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
	out, err := cli(t, hub.URL, none, "destinations", "list")
	for _, want := range []string{"nas    sftp   sftp://tender@nas.example.com:2222//srv/restic", "disk   local  local:/mnt/backup",
		"rs     rest   rest:https://rest.example.com:8000", "gd     rclone rclone:gdrive:tender"} {
		if err != nil || !strings.Contains(strings.Join(strings.Fields(out), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "marker-rest-password") {
		t.Error("the list shows the REST password")
	}

	out, err = cli(t, hub.URL, none, "destinations", "update", "nas", "--port", "22")
	if err != nil || out != "Updated destination nas: sftp://tender@nas.example.com:22//srv/restic.\n" {
		t.Errorf("update: %v %q", err, out)
	}
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"destinations", "update", "nas", "--bucket", "x"}, "--bucket is not for a sftp destination"},
		{[]string{"destinations", "update", "nas"}, "needs an option to change"},
		{[]string{"destinations", "add", "x", "--type", "ftp"}, "--type must be one of local, rclone, rest, s3, sftp"},
		{[]string{"destinations", "add", "x", "--type", "local", "--endpoint", "https://e.example"}, "--endpoint is not for a local destination"},
		{[]string{"destinations", "add", "x", "--type", "sftp", "--host", "-oProxyCommand=x", "--user", "u", "--path", "p"}, "config.host must be"},
		{[]string{"destinations", "add", "x", "--type", "rest", "--url", "https://r.example.com", "--username", "u"}, "no terminal to ask for the REST server password"},
		{[]string{"destinations", "test", "nas"}, "needs a destination name and a machine name"},
		{[]string{"destinations", "test", "nope", "g4f4r0-code"}, `Destination "nope" doesn't exist`},
		{[]string{"destinations", "test", "nas", "nope"}, `Machine "nope" doesn't exist`},
	} {
		if _, err := cli(t, hub.URL, none, c.args...); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%v: %v, want %q", c.args, err, c.says)
		}
	}

	out, err = cli(t, hub.URL, none, "destinations", "test", "rs", "g4f4r0-code")
	if err != nil || out != "Reached the repository.\n" {
		t.Errorf("test: %v %q", err, out)
	}
	mu.Lock()
	got := strings.Join(targets, "\n")
	mu.Unlock()
	if !strings.Contains(got, `"name":"rs"`) || !strings.Contains(got, "marker-rest-password") {
		t.Errorf("the daemon was handed %s, want rs with its password", got)
	}
	out, _ = cli(t, hub.URL, none, "actions", "list", "g4f4r0-code")
	if !strings.Contains(out, "destination rs") || strings.Contains(out, "marker") {
		t.Errorf("actions list:\n%s", out)
	}

	// MCP does the same.
	cs := connect(t, d)
	var a actions.Action
	structured(t, callTool(t, cs, "test_destination", map[string]any{"destination": "disk", "machine": "g4f4r0-code"}), &a)
	if a.Status != actions.StatusCompleted || a.Destination == nil || *a.Destination != "disk" || a.Output == nil {
		t.Errorf("test_destination = %+v", a)
	}
	fails(t, cs, "test_destination", map[string]any{"destination": "nope", "machine": "g4f4r0-code"}, "list_destinations")
	fails(t, cs, "test_destination", map[string]any{"destination": "disk", "machine": "nope"}, "list_machines")
}
