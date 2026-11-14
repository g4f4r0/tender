package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/backups"
)

// cli runs a hub command against database and returns what it printed.
func cli(t *testing.T, database string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := hubCommand(args[0], append(args[1:], "--database", database), &out)
	return out.String(), err
}

func mustCLI(t *testing.T, database string, args ...string) string {
	t.Helper()
	out, err := cli(t, database, args...)
	if err != nil {
		t.Fatalf("tender hub %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func TestHubCommands(t *testing.T) {
	database := filepath.Join(t.TempDir(), "hub.db")

	// add-machine prints the token and nothing else.
	out := mustCLI(t, database, "add-machine", "g4f4r0-code")
	token := strings.TrimSuffix(out, "\n")
	if len(token) != 43 || strings.ContainsAny(token, " \n") {
		t.Fatalf("add-machine printed %q, want one 43 character token", out)
	}
	if _, err := cli(t, database, "add-machine", "g4f4r0-code"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate machine: %v, want already exists", err)
	}
	if _, err := cli(t, database, "add-machine", "Bad Name"); err == nil {
		t.Error("invalid machine name was accepted")
	}
	if _, err := cli(t, database, "add-machine"); err == nil {
		t.Error("add-machine without a name was accepted")
	}
	if _, err := cli(t, database, "add-machine", "a", "b"); err == nil {
		t.Error("add-machine with two names was accepted")
	}
	if _, err := cli(t, database, "nope"); err == nil || !strings.Contains(err.Error(), "unknown hub command") {
		t.Errorf("unknown command: %v", err)
	}
	if out, err := cli(t, database, "help"); err != nil || !strings.Contains(out, "set-backup") {
		t.Errorf("help = %q, %v", out, err)
	}

	// The name may also come after the flags.
	if _, err := cli(t, database, "add-machine", "--database", database, "g4f4r0-apps"); err != nil {
		t.Errorf("flags before the name: %v", err)
	}

	// add-destination and set-backup.
	mustCLI(t, database, "add-destination", "r2-code", "--type", "s3", "--endpoint", "https://acct.r2.cloudflarestorage.com", "--bucket", "g4f4r0-backups-code")
	if _, err := cli(t, database, "add-destination", "r2-bad", "--type", "s3", "--endpoint", "nope", "--bucket", "b"); err == nil {
		t.Error("destination with a bad endpoint was accepted")
	}
	if _, err := cli(t, database, "add-destination", "r2-code", "--type", "s3", "--endpoint", "https://other.example.com", "--bucket", "b"); err == nil {
		t.Error("duplicate destination was accepted")
	}
	for name, args := range map[string][]string{
		"bad schedule":        {"set-backup", "g4f4r0-code", "--destination", "r2-code", "--schedule", "daily at 3", "--retention-days", "30"},
		"no retention":        {"set-backup", "g4f4r0-code", "--destination", "r2-code", "--schedule", "30 3 * * *"},
		"unknown destination": {"set-backup", "g4f4r0-code", "--destination", "nope", "--schedule", "30 3 * * *", "--retention-days", "30"},
		"unknown machine":     {"set-backup", "nope", "--destination", "r2-code", "--schedule", "30 3 * * *", "--retention-days", "30"},
	} {
		if _, err := cli(t, database, args...); err == nil {
			t.Errorf("set-backup with %s was accepted", name)
		}
	}
	mustCLI(t, database, "set-backup", "g4f4r0-code", "--destination", "r2-code", "--schedule", "30 3 * * *", "--retention-days", "30")

	// The hub serves what the commands wrote, with the real token and no other.
	d, err := openHub(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	srv := httptest.NewServer(newHub(d, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	get := func(path, bearer, login string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest("GET", srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if login != "" {
			req.Header.Set("Tailscale-User-Login", login)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, body
	}

	status, body := get("/v1/machines/g4f4r0-code/backups/settings", token, "")
	var st backups.Settings
	if err := json.Unmarshal(body, &st); err != nil || status != 200 || st.Destination.Name != "r2-code" || st.Schedule != "30 3 * * *" || st.RetentionDays != 30 {
		t.Fatalf("agent settings: %d %s", status, body)
	}
	if status, _ := get("/v1/machines/g4f4r0-apps/backups/settings", token, ""); status != 403 {
		t.Errorf("token on another machine: %d, want 403", status)
	}
	if status, _ := get("/v1/machines/g4f4r0-code/backups/settings", "wrong", ""); status != 401 {
		t.Errorf("wrong token: %d, want 401", status)
	}
	if status, _ := get("/v1/machines", "", ""); status != 401 {
		t.Errorf("reader route without the header: %d, want 401", status)
	}
	if status, _ := get("/v1/machines", "", "johan@example.com"); status != 200 {
		t.Errorf("reader route with the header: %d, want 200", status)
	}
	if status, _ := get("/v1/machines/g4f4r0-apps/backups/settings", "", "johan@example.com"); status != 404 {
		t.Errorf("machine without settings: %d, want 404", status)
	}

	// rotate-token: the old token stops working at once.
	fresh := strings.TrimSuffix(mustCLI(t, database, "rotate-token", "g4f4r0-code"), "\n")
	if fresh == token || len(fresh) != 43 {
		t.Fatalf("rotate-token printed %q", fresh)
	}
	if status, _ := get("/v1/machines/g4f4r0-code/backups/settings", token, ""); status != 401 {
		t.Errorf("old token after rotation: %d, want 401", status)
	}
	if status, _ := get("/v1/machines/g4f4r0-code/backups/settings", fresh, ""); status != 200 {
		t.Errorf("new token: %d, want 200", status)
	}
	if _, err := cli(t, database, "rotate-token", "nope"); err == nil {
		t.Error("rotate-token for an unknown machine was accepted")
	}
}
