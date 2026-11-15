package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/tokens"
)

var pathValue = regexp.MustCompile(`\{[a-z]+\}`)

// Every route people may call, as the hub registers them, answers 401 problem+json saying how to
// get a token when the request has none, a wrong one or a machine token, and lets the API token
// through. A new route can't be left open.
func TestEveryPeopleRouteNeedsAToken(t *testing.T) {
	d := seeded(t)
	machineToken, err := machines.NewStore(d).RotateToken(context.Background(), "g4f4r0-code")
	if err != nil {
		t.Fatal(err)
	}
	hub := newHub(d, testKey, nil, quiet)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	n := 0
	for _, r := range hub.Routes() {
		if r.Caller == server.CallerDaemon {
			continue
		}
		n++
		method, path, _ := strings.Cut(r.Pattern, " ")
		// Unknown names: the token is checked before anything is looked up or changed.
		path = pathValue.ReplaceAllString(path, "nope")
		for name, token := range map[string]string{"no token": "-", "a wrong token": "tender_nope", "a machine token": machineToken} {
			if r.Caller == server.CallerBoth && name == "a machine token" {
				continue // the machine's own routes take its token, on its own name only
			}
			status, body := restCall(t, srv.URL, method, path, token, nil)
			if status != http.StatusUnauthorized || !strings.Contains(body, `"status":401`) || !strings.Contains(body, "sudo tender tokens add NAME") {
				t.Errorf("%s with %s: %d %s, want 401 saying how to make a token", r.Pattern, name, status, body)
			}
		}
		if status, body := restCall(t, srv.URL, method, path, "", nil); status == http.StatusUnauthorized {
			t.Errorf("%s with the API token: 401 %s", r.Pattern, body)
		}
	}
	if n != len(mcpCoverage) {
		t.Errorf("checked %d people routes, want all %d", n, len(mcpCoverage))
	}
	// Health is open, for the update's health check and load balancers.
	if status, body := restCall(t, srv.URL, "GET", "/v1/health", "-", nil); status != http.StatusOK {
		t.Errorf("health without a token: %d %s", status, body)
	}
}

// A machine token never passes as people's, and an API token never passes as a daemon's: on a
// route both may call, each gets its own view, and only the daemon sees its secrets.
func TestMachineAndPeopleTokensNeverSwap(t *testing.T) {
	d := seeded(t)
	machineToken, err := machines.NewStore(d).RotateToken(context.Background(), "g4f4r0-code")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newHub(d, testKey, nil, quiet))
	t.Cleanup(srv.Close)
	const settings = "/v1/machines/g4f4r0-code/backups/settings"
	if status, body := restCall(t, srv.URL, "GET", settings, machineToken, nil); status != 200 || !strings.Contains(body, "repo-password") {
		t.Errorf("daemon view: %d %s", status, body)
	}
	if status, body := restCall(t, srv.URL, "GET", settings, "", nil); status != 200 || strings.Contains(body, "repo-password") {
		t.Errorf("people view: %d %s", status, body)
	}
	usage := map[string]any{"cpu_load": 1, "memory_used_percent": 2, "disk_used_percent": 3, "uptime_seconds": 4, "created_at": time.Now().UTC()}
	if status, body := restCall(t, srv.URL, "POST", "/v1/machines/g4f4r0-code/usage", "", usage); status != http.StatusUnauthorized {
		t.Errorf("an API token on a daemon route: %d %s", status, body)
	}
	if status, body := restCall(t, srv.URL, "POST", "/v1/machines/g4f4r0-code/usage", machineToken, usage); status != http.StatusCreated {
		t.Errorf("the machine token on its daemon route: %d %s", status, body)
	}
	if status, body := restCall(t, srv.URL, "GET", "/v1/machines", machineToken, nil); status != http.StatusUnauthorized {
		t.Errorf("a machine token on a people route: %d %s", status, body)
	}
}

// A hub updated to v0.4 has no API token: it refuses people and MCP clients, and its daemons go on.
func TestHubWithoutTokensRefusesPeopleOnly(t *testing.T) {
	d := seeded(t)
	if err := exec(t, d, `DELETE FROM api_tokens`); err != nil {
		t.Fatal(err)
	}
	machineToken, err := machines.NewStore(d).RotateToken(context.Background(), "g4f4r0-code")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newHub(d, testKey, nil, quiet))
	t.Cleanup(srv.Close)
	if status, body := restCall(t, srv.URL, "GET", "/v1/machines", "", nil); status != http.StatusUnauthorized {
		t.Errorf("people: %d %s", status, body)
	}
	if res, body := post(t, srv, false, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("MCP: %d %s", res.StatusCode, body)
	}
	usage := map[string]any{"cpu_load": 1, "memory_used_percent": 2, "disk_used_percent": 3, "uptime_seconds": 4, "created_at": time.Now().UTC()}
	if status, body := restCall(t, srv.URL, "POST", "/v1/machines/g4f4r0-code/usage", machineToken, usage); status != http.StatusCreated {
		t.Errorf("daemon: %d %s", status, body)
	}
	if status, _ := restCall(t, srv.URL, "GET", "/v1/machines/g4f4r0-code/backups/settings", machineToken, nil); status != 200 {
		t.Errorf("daemon settings: %d", status)
	}
}

// "sudo tender tokens add" on the hub machine writes the first token to the database, without the
// hub; then list and remove go through the API with it, and a removed token is refused.
func TestTokensCommands(t *testing.T) {
	d := schema(t)
	if err := exec(t, d, `DELETE FROM api_tokens`); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(t.TempDir(), "missing.db")
	srv := httptest.NewServer(newHub(d, testKey, nil, quiet))
	t.Cleanup(srv.Close)
	t.Setenv("TENDER_HUB", srv.URL)
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runTokens(args, session{out: &out, now: time.Now})
		return out.String(), err
	}

	// No token and no root: the hub refuses, and the error says how to make the first token.
	t.Setenv(envToken, "")
	t.Setenv(config.EnvTestRoot, "")
	if os.Geteuid() != 0 {
		if _, err := run("list"); err == nil || !strings.Contains(err.Error(), "sudo tender tokens add NAME") || !strings.Contains(err.Error(), envToken) {
			t.Errorf("list without a token: %v", err)
		}
	}

	// As root, without a token: straight to the database, whose path comes from --database.
	t.Setenv(config.EnvTestRoot, t.TempDir())
	path := hubDatabasePath(t, d)
	if _, err := run("add", "laptop", "--database", database); err == nil || !strings.Contains(err.Error(), "no hub database at "+database) {
		t.Errorf("add to a missing database: %v", err)
	}
	if has, err := hubHasTokens(context.Background(), path); has || err != nil {
		t.Errorf("before: has tokens %v, %v", has, err)
	}
	out, err := run("add", "laptop", "--database", path)
	token := regexp.MustCompile(`tender_[A-Za-z0-9_-]{43}`).FindString(out)
	if err != nil || token == "" || !strings.Contains(out, "export TENDER_TOKEN="+token) {
		t.Fatalf("bootstrap add: %v\n%s", err, out)
	}
	if has, err := hubHasTokens(context.Background(), path); !has || err != nil {
		t.Errorf("after: has tokens %v, %v", has, err)
	}
	if _, err := run("add", "laptop", "--database", path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("same name twice: %v", err)
	}

	// With the token: the API.
	t.Setenv(envToken, token)
	if out, err := run("add", "claude-code"); err != nil || !strings.Contains(out, "Made API token claude-code.") {
		t.Fatalf("add through the API: %v\n%s", err, out)
	}
	if _, err := run("add", "other", "--database", path); err == nil || !strings.Contains(err.Error(), "--database works only as root") {
		t.Errorf("--database with a token: %v", err)
	}
	out, err = run("list")
	if err != nil || !strings.Contains(out, "NAME          LAST USED   CREATED") || !strings.Contains(out, "claude-code   never") ||
		!strings.Contains(out, "laptop        just now") || strings.Contains(out, "tender_") {
		t.Errorf("list: %v\n%s", err, out)
	}
	if _, err := run("remove", "claude-code"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("remove without --yes: %v", err)
	}
	if out, err := run("remove", "claude-code", "--yes"); err != nil || !strings.Contains(out, "Deleted API token claude-code.") {
		t.Errorf("remove: %v %s", err, out)
	}
	if _, err := run("remove", "laptop", "--yes"); err != nil {
		t.Fatal(err)
	}
	// The token it used is gone, so the hub refuses it, and the error says what to do.
	if _, err := run("list"); err == nil || !strings.Contains(err.Error(), "refused the API token") {
		t.Errorf("list with a deleted token: %v", err)
	}
	// With none left, root makes one again; --token wins over TENDER_TOKEN.
	t.Setenv(envToken, "")
	out, err = run("add", "again", "--database", path)
	again := regexp.MustCompile(`tender_[A-Za-z0-9_-]{43}`).FindString(out)
	if err != nil || again == "" {
		t.Fatalf("add again: %v %s", err, out)
	}
	t.Setenv(envToken, "tender_nope")
	if out, err := run("list", "--token", again); err != nil || !strings.Contains(out, "again ") {
		t.Errorf("--token: %v %s", err, out)
	}
}

// hubDatabasePath is the file of d.
func hubDatabasePath(t *testing.T, d *db.DB) string {
	t.Helper()
	var file string
	if err := d.Read.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&file); err != nil || file == "" {
		t.Fatalf("database file %q: %v", file, err)
	}
	return file
}

// list_tokens and delete_token work as the REST routes do, and no tool makes a token.
func TestTokenTools(t *testing.T) {
	d := seeded(t)
	if _, err := tokens.NewStore(d).Create(context.Background(), "laptop"); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, d)
	restJSON(t, newHub(d, testKey, nil, quiet), "/v1/tokens") // notes that the tests token was used, so both answers have it
	want := restJSON(t, newHub(d, testKey, nil, quiet), "/v1/tokens")
	var list server.List[tokens.Token]
	structured(t, callTool(t, cs, "list_tokens", nil), &list)
	if len(list.Data) != 2 || list.Data[0].Name != "laptop" || list.Data[1].Name != "tests" || list.Data[1].LastUsedAt == nil {
		t.Errorf("list_tokens = %+v", list.Data)
	}
	if !reflect.DeepEqual(mustJSON(t, list), want) {
		t.Errorf("list_tokens = %+v, REST %v", list, want)
	}
	if res := callTool(t, cs, "delete_token", map[string]any{"name": "laptop"}); res.IsError {
		t.Errorf("delete_token: %+v", res.Content)
	}
	if res := callTool(t, cs, "delete_token", map[string]any{"name": "laptop"}); !res.IsError {
		t.Error("delete_token of a missing token succeeded")
	}
	for _, name := range toolNames(t) {
		if strings.Contains(name, "token") && name != "list_tokens" && name != "delete_token" && name != "rotate_machine_token" {
			t.Errorf("tool %s touches tokens; making an API token is not a tool", name)
		}
	}
}
