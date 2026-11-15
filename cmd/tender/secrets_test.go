package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// restCall makes one request to the hub, as a person unless token is set, and returns the status and body.
func restCall(t *testing.T, base, method, path, token string, body any) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, base+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

// Secrets go in and come out only where the design says: the machine token once when it is made,
// a generated password once when the destination is made, and a machine's own credentials to its own
// daemon. This test creates a destination with marker secrets, then reads everything the hub can
// say (every REST list and get, every error, every MCP tool and its errors, and the log) and
// looks for them.
func TestNoSecretAppearsAnywhere(t *testing.T) {
	d := schema(t)
	var logs bytes.Buffer
	hub := httptest.NewServer(newHub(d, testKey, nil, slog.New(slog.NewTextHandler(&logs, nil))))
	t.Cleanup(hub.Close)
	cs := connect(t, d) // the MCP tools, on the same database
	ctx := context.Background()

	const (
		keyID     = "MARKER-ACCESS-KEY-ID-7f3a"
		secretKey = "MARKER-SECRET-ACCESS-KEY-91cd"
		password  = "MARKER-REPOSITORY-PASSWORD-5b2e"
		newKeyID  = "MARKER-NEW-KEY-ID-0d44"
		newSecret = "MARKER-NEW-SECRET-KEY-e8a1"
		badPass   = "MARKER-REFUSED-PASSWORD-33f0"
	)
	secrets := []string{keyID, secretKey, password, newKeyID, newSecret, badPass}

	// Set up through REST: a machine, two destinations (one with a generated password), settings, a recipient.
	var machine struct{ Token string }
	status, body := restCall(t, hub.URL, "POST", "/v1/machines", "", map[string]any{"name": "g4f4r0-code"})
	if status != 201 || json.Unmarshal([]byte(body), &machine) != nil || machine.Token == "" {
		t.Fatalf("create machine: %d %s", status, body)
	}
	status, _ = restCall(t, hub.URL, "POST", "/v1/machines", "", map[string]any{"name": "g4f4r0-apps"})
	if status != 201 {
		t.Fatal("create second machine")
	}
	status, body = restCall(t, hub.URL, "POST", "/v1/backups/destinations", "", map[string]any{"name": "r2-code", "endpoint": "https://acct.r2.cloudflarestorage.com",
		"bucket": "code", "access_key_id": keyID, "secret_access_key": secretKey, "password": password})
	if status != 201 {
		t.Fatalf("create destination: %d %s", status, body)
	}
	var generated struct{ Password string }
	status, body = restCall(t, hub.URL, "POST", "/v1/backups/destinations", "", map[string]any{"name": "r2-apps", "endpoint": "https://acct.r2.cloudflarestorage.com",
		"bucket": "apps", "access_key_id": keyID, "secret_access_key": secretKey})
	if status != 201 || json.Unmarshal([]byte(body), &generated) != nil || generated.Password == "" {
		t.Fatalf("create destination without a password: %d %s", status, body)
	}
	secrets = append(secrets, generated.Password, machine.Token)
	if status, _ = restCall(t, hub.URL, "PUT", "/v1/machines/g4f4r0-code/backups/settings", "", map[string]any{"destination": "r2-code", "schedule": "30 3 * * *", "retention_days": 30}); status != 200 {
		t.Fatalf("set backups: %d", status)
	}
	if status, _ = restCall(t, hub.URL, "POST", "/v1/notifications/recipients", "", map[string]any{"email": "johan@example.com"}); status != 201 {
		t.Fatalf("add recipient: %d", status)
	}
	// Daemon traffic, so the token passes through the request log too.
	if status, _ = restCall(t, hub.URL, "POST", "/v1/machines/g4f4r0-code/usage", machine.Token,
		map[string]any{"cpu_load": 1, "memory_used_percent": 1, "disk_used_percent": 1, "uptime_seconds": 1, "created_at": "2026-11-14T03:30:00Z"}); status != 201 {
		t.Fatalf("daemon usage: %d", status)
	}
	// The daemon's own read of its settings is the one place credentials appear.
	status, body = restCall(t, hub.URL, "GET", "/v1/machines/g4f4r0-code/backups/settings", machine.Token, nil)
	for _, want := range []string{keyID, secretKey, password} {
		if status != 200 || !strings.Contains(body, want) {
			t.Fatalf("the daemon's settings lack %q: %d %s", want, status, body)
		}
	}

	type output struct{ label, text string }
	var outputs []output
	rest := func(method, path string, body any) {
		t.Helper()
		status, text := restCall(t, hub.URL, method, path, "", body)
		outputs = append(outputs, output{method + " " + path + " -> " + http.StatusText(status), text})
	}
	tool := func(name string, args map[string]any) {
		t.Helper()
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			outputs = append(outputs, output{"tool " + name + " protocol error", err.Error()})
			return
		}
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, output{"tool " + name, string(raw)})
	}

	// Every REST read, as a person.
	for _, path := range []string{
		"/v1/health", "/v1/machines", "/v1/machines/g4f4r0-code", "/v1/machines/g4f4r0-code/usage",
		"/v1/backups", "/v1/backups/destinations", "/v1/backups/destinations/r2-code", "/v1/backups/destinations/r2-apps",
		"/v1/machines/g4f4r0-code/backups", "/v1/machines/g4f4r0-code/backups/settings", "/v1/machines/g4f4r0-code/backups/snapshots",
		"/v1/machines/g4f4r0-code/backups/history", "/v1/notifications", "/v1/machines/g4f4r0-code/notifications",
		"/v1/notifications/recipients",
	} {
		rest("GET", path, nil)
	}
	// REST writes and refusals that carry secrets in their bodies.
	rest("POST", "/v1/backups/destinations", map[string]any{"name": "r2-code", "endpoint": "https://acct.r2.cloudflarestorage.com", "bucket": "code",
		"access_key_id": keyID, "secret_access_key": secretKey, "password": password}) // 409
	rest("POST", "/v1/backups/destinations", map[string]any{"name": "bad", "endpoint": "nope", "bucket": "b",
		"access_key_id": keyID, "secret_access_key": secretKey, "password": password}) // 422
	rest("POST", "/v1/backups/destinations", map[string]any{"name": "bad", "endpoint": "https://x.example.com", "bucket": "b",
		"access_key_id": keyID, "secret_access_key": secretKey, "password": password, "region": "auto"}) // 400
	rest("PATCH", "/v1/backups/destinations/r2-code", map[string]any{"password": badPass}) // 422
	rest("PATCH", "/v1/backups/destinations/r2-code", map[string]any{"access_key_id": newKeyID, "secret_access_key": newSecret})
	rest("PATCH", "/v1/backups/destinations/nope", map[string]any{"access_key_id": newKeyID, "secret_access_key": newSecret}) // 404
	rest("PATCH", "/v1/backups/destinations/r2-code", map[string]any{"endpoint": "nope", "access_key_id": newKeyID})          // 422
	rest("DELETE", "/v1/backups/destinations/r2-code", nil)                                                                   // 409, in use
	rest("GET", "/v1/backups/destinations", nil)                                                                              // after the patch
	rest("GET", "/v1/machines/g4f4r0-code/backups/settings", nil)

	// Every MCP tool, with the same secrets where a tool takes them.
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"list_machines", nil},
		{"get_machine_usage", map[string]any{"machine": "g4f4r0-code"}},
		{"get_backup_status", nil},
		{"get_backup_status", map[string]any{"machine": "g4f4r0-code"}},
		{"list_backup_snapshots", map[string]any{"machine": "g4f4r0-code"}},
		{"list_backup_history", map[string]any{"machine": "g4f4r0-code"}},
		{"list_notifications", nil},
		{"list_recipients", nil},
		{"list_destinations", nil},
		{"add_destination", map[string]any{"name": "r2-code", "endpoint": "https://acct.r2.cloudflarestorage.com", "bucket": "code",
			"access_key_id": keyID, "secret_access_key": secretKey, "password": password}}, // 409
		{"add_destination", map[string]any{"name": "tool-made", "endpoint": "https://other.r2.cloudflarestorage.com", "bucket": "tool",
			"access_key_id": keyID, "secret_access_key": secretKey, "password": password}}, // an owner's password is not returned
		{"add_destination", map[string]any{"name": "bad", "endpoint": "nope", "bucket": "b", "access_key_id": keyID, "secret_access_key": secretKey, "password": password}},
		{"update_destination", map[string]any{"destination": "r2-code", "access_key_id": newKeyID, "secret_access_key": newSecret}},
		{"update_destination", map[string]any{"destination": "r2-code", "password": badPass}},
		{"update_destination", map[string]any{"destination": "nope", "access_key_id": newKeyID, "secret_access_key": newSecret}},
		{"remove_destination", map[string]any{"destination": "r2-code"}}, // in use
		{"set_backup", map[string]any{"machine": "g4f4r0-apps", "destination": "r2-apps", "schedule": "0 4 * * *", "retention_days": 7}},
		{"get_backup_status", nil},
		{"unset_backup", map[string]any{"machine": "g4f4r0-apps"}},
		{"add_recipient", map[string]any{"email": "ops@example.com"}},
		{"send_test_email", nil},
		{"remove_recipient", map[string]any{"id": 2}},
	} {
		tool(c.name, c.args)
	}
	rest("GET", "/v1/backups/destinations", nil)
	rest("GET", "/v1/backups/destinations/tool-made", nil)
	rest("GET", "/v1/machines/g4f4r0-apps/backups/settings", nil)

	checked := 0
	for _, o := range outputs {
		checked++
		for _, s := range secrets {
			if strings.Contains(o.text, s) {
				t.Errorf("%s shows a secret (%q...): %s", o.label, s[:12], o.text)
			}
		}
		for _, field := range []string{`"password"`, `"secret_access_key"`, `"access_key_id"`, `"credentials"`, `"token"`} {
			// None of the outputs above is one of the few that show a secret once, so none has these fields.
			if strings.Contains(o.text, field) {
				t.Errorf("%s has the field %s: %s", o.label, field, o.text)
			}
		}
	}
	if checked < 40 {
		t.Errorf("only %d outputs were checked, want the whole surface", checked)
	}
	for _, s := range secrets {
		if strings.Contains(logs.String(), s) {
			t.Errorf("the hub log shows a secret (%q...):\n%s", s[:12], logs.String())
		}
	}
	// The log did record the requests, so an empty log can't pass this check.
	if !strings.Contains(logs.String(), "path=/v1/machines/g4f4r0-code/backups/settings") {
		t.Errorf("the hub log has no request lines:\n%s", logs.String())
	}
}
