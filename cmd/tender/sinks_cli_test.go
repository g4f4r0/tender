package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/logs"
)

// Sinks can be added, listed, shown, changed and removed from the command line and over MCP,
// against the real hub, and their secrets never show.
func TestSinksThroughTheCLIAndMCP(t *testing.T) {
	d := seeded(t)
	hub := httptest.NewServer(newHub(d, testKey, nil, quiet))
	t.Cleanup(hub.Close)
	dir := t.TempDir()
	headers := filepath.Join(dir, "headers")
	if err := os.WriteFile(headers, []byte("# the collector's key\nAuthorization: Bearer marker-otlp-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	none := &answers{}
	out, err := cli(t, hub.URL, none, "sinks", "list")
	if err != nil || !strings.HasPrefix(out, "No log sinks yet.") {
		t.Fatalf("empty list: %v %q", err, out)
	}
	out, err = cli(t, hub.URL, none, "sinks", "add", "otel", "--type", "otlp", "--endpoint", "https://otel.example.com/v1/logs",
		"--headers-file", headers, "--units", "nginx.service,ssh.service", "--min-priority", "warning", "--no-services", "--machines", "g4f4r0-code")
	if err != nil || !strings.Contains(out, "Added log sink otel.") || strings.Contains(out, "secret") {
		t.Fatalf("add otel: %v\n%s", err, out)
	}
	out, err = cli(t, hub.URL, none, "sinks", "add", "hook", "--type", "webhook", "--url", "https://logs.example.com/in")
	if err != nil || !strings.Contains(out, "The signing secret, made by the hub and shown only now:") {
		t.Fatalf("add hook: %v\n%s", err, out)
	}
	madeSecret := strings.TrimSpace(strings.Split(out, "\n")[3])
	if len(madeSecret) != 64 {
		t.Fatalf("made secret %q", madeSecret)
	}

	st := logs.NewStore(d, testKey)
	k, _ := st.Sink(context.Background(), "otel")
	at := time.Now().Add(-20 * time.Minute)
	msg := "the sink answered 503 Service Unavailable"
	if err := st.SetStatus(context.Background(), 1, logs.StatusReport{Data: []logs.StatusInput{{SinkID: k.ID, RecordsSent: 42, RecordsDropped: 3, FailingSince: &at, LastError: &msg}}}); err != nil {
		t.Fatal(err)
	}
	out, err = cli(t, hub.URL, none, "sinks", "list")
	flat := strings.Join(strings.Fields(out), " ")
	for _, want := range []string{"NAME TYPE SENDS TO MACHINES STATUS", "hook webhook https://logs.example.com/in all no report yet",
		"otel otlp https://otel.example.com/v1/logs g4f4r0-code failing on g4f4r0-code"} {
		if err != nil || !strings.Contains(flat, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	out, err = cli(t, hub.URL, none, "sinks", "show", "otel")
	flat = strings.Join(strings.Fields(out), " ")
	for _, want := range []string{"SOURCES journal (warning and up, nginx.service,ssh.service)", "MACHINES g4f4r0-code",
		"g4f4r0-code 42 3 never failing since 20 minutes ago: the sink answered 503"} {
		if err != nil || !strings.Contains(flat, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	out, err = cli(t, hub.URL, none, "sinks", "update", "otel", "--disable", "--services", "web", "--machines", "all")
	if err != nil || !strings.Contains(out, "Updated log sink otel.") {
		t.Fatalf("update: %v %s", err, out)
	}
	k, _ = st.Sink(context.Background(), "otel")
	if k.Enabled || len(k.Machines) != 0 || k.Sources.Services == nil || k.Sources.Services.Names[0] != "web" || k.Sources.Journal == nil || k.Config["headers_set"] != true {
		t.Errorf("after update %+v", k)
	}
	for _, c := range [][]string{
		{"sinks", "add", "x"},
		{"sinks", "add", "x", "--type", "otlp", "--no-journal", "--units", "a.service"},
		{"sinks", "update", "otel"},
		{"sinks", "update", "otel", "--enable", "--disable"},
		{"sinks", "show", "zzz"},
	} {
		if _, err := cli(t, hub.URL, none, c...); err == nil {
			t.Errorf("%v: no error", c)
		}
	}

	// MCP sees the same sinks and never their secrets.
	cs := connect(t, d)
	var list struct{ Data []logs.Sink }
	structured(t, callTool(t, cs, "list_log_sinks", map[string]any{}), &list)
	if len(list.Data) != 2 || list.Data[1].Name != "otel" || len(list.Data[1].Status) != 1 || list.Data[1].Status[0].RecordsSent != 42 {
		t.Errorf("list_log_sinks %+v", list.Data)
	}
	res := callTool(t, cs, "update_log_sink", map[string]any{"sink": "hook", "config": map[string]any{"secret": "***", "url": "https://other.example.com/in"}, "machines": []string{"g4f4r0-apps"}})
	var hook logs.Sink
	structured(t, res, &hook)
	if hook.Config["url"] != "https://other.example.com/in" || hook.Machines[0] != "g4f4r0-apps" || hook.Config["secret"] != "***" {
		t.Errorf("update_log_sink %+v", hook)
	}
	daemon, _ := st.DaemonSinks(context.Background(), "g4f4r0-apps")
	if len(daemon) != 1 || daemon[0].Config["secret"] != madeSecret {
		t.Errorf("the made secret changed: %+v", daemon)
	}
	res = callTool(t, cs, "add_log_sink", map[string]any{"name": "otel2", "type": "otlp", "config": map[string]any{"endpoint": "https://o.example.com/v1/logs"},
		"sources": map[string]any{"journal": map[string]any{"units": []string{}, "min_priority": "err"}}})
	var added logs.SinkCreated
	structured(t, res, &added)
	if added.Sources.Services != nil || added.Sources.Journal.MinPriority != "err" {
		t.Errorf("add_log_sink %+v", added)
	}
	if res := callTool(t, cs, "update_log_sink", map[string]any{"sink": "otel2"}); !res.IsError || !strings.Contains(errorText(res), "Give config, enabled, sources or machines") {
		t.Errorf("an empty update: %s", errorText(res))
	}
	if res := callTool(t, cs, "remove_log_sink", map[string]any{"sink": "otel2"}); res.IsError {
		t.Error(errorText(res))
	}
	if res := callTool(t, cs, "remove_log_sink", map[string]any{"sink": "otel2"}); !res.IsError || !strings.Contains(errorText(res), `Log sink "otel2" doesn't exist.`) {
		t.Errorf("remove twice: %s", errorText(res))
	}
	out, err = cli(t, hub.URL, none, "sinks", "remove", "hook")
	if err != nil || !strings.Contains(out, "Removed log sink hook.") {
		t.Errorf("remove: %v %s", err, out)
	}
	for _, f := range []string{"marker-otlp-key", madeSecret} {
		for _, p := range []string{"/v1/logs/sinks", "/v1/logs/sinks/otel"} {
			if b := get(t, hub.URL+p); strings.Contains(b, f) {
				t.Errorf("%s shows a secret", p)
			}
		}
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
