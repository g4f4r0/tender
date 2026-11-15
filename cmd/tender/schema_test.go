package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

// The real schema: every domain's migrations together, as the hub applies them.
func schema(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	for range 2 {
		if err := d.Migrate(context.Background(), migrations()...); err != nil {
			t.Fatal(err)
		}
	}
	addTestToken(t, d)
	return d
}

// testToken is the API token every test database has, from addTestToken, and every test command sends.
const testToken = "tender_test-token-of-the-tests"

// addTestToken stores testToken, as "tests", in d.
func addTestToken(t *testing.T, d *db.DB) {
	t.Helper()
	sum := sha256.Sum256([]byte(testToken))
	must(t, d, `INSERT INTO api_tokens (name, token_hash, created_at, updated_at) VALUES ('tests', ?, 1, 1)`, sum[:])
}

func exec(t *testing.T, d *db.DB, query string, args ...any) error {
	t.Helper()
	_, err := d.Write.Exec(query, args...)
	return err
}

func must(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if err := exec(t, d, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func rows(t *testing.T, d *db.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSchemaAppliesTwice(t *testing.T) {
	d := schema(t)
	if n := rows(t, d, `SELECT count(*) FROM migrations`); n != 20 {
		t.Fatalf("migrations rows = %d, want 20 (machines twice, backups three times, notifications four times, services twice, actions three times, "+
			"logs, security, updates, tokens, server twice)", n)
	}
	for _, table := range []string{"machines", "machine_usage", "backup_destinations", "backup_settings",
		"backup_snapshots", "backup_history", "notifications", "notification_channels", "notification_deliveries", "service_engines", "services", "actions",
		"log_sinks", "log_sink_status", "idempotency_keys", "security_states", "update_settings", "update_rollouts", "update_rollout_machines", "api_tokens"} {
		if n := rows(t, d, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, table); n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
}

func TestEveryTableHasIDAndTimestamps(t *testing.T) {
	d := schema(t)
	tables, err := d.Read.Query(`SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := tables.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range names {
		for _, col := range []string{"id", "created_at", "updated_at"} {
			if n := rows(t, d, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ? AND "notnull" + pk > 0`, table, col); n != 1 {
				t.Errorf("%s.%s missing or nullable", table, col)
			}
		}
	}
}

func TestSchemaConstraints(t *testing.T) {
	d := schema(t)
	must(t, d, `INSERT INTO machines (id, name, token_hash, created_at, updated_at) VALUES (1, 'g4f4r0-apps', x'00', 1, 1)`)
	must(t, d, `INSERT INTO backup_destinations (id, name, type, config, secrets, password, location, created_at, updated_at)
	            VALUES (1, 'r2', 's3', '{}', '{}', x'03', 's3:https://example.r2.cloudflarestorage.com/g4f4r0-backups-apps', 1, 1)`)

	bad := map[string]string{
		"STRICT rejects text in an integer column": `INSERT INTO machine_usage (machine_id, uptime_seconds, created_at, updated_at) VALUES (1, 'soon', 1, 1)`,
		"history status must be a known word":      `INSERT INTO backup_history (machine_id, type, status, created_at, updated_at) VALUES (1, 'backup', 'done', 1, 1)`,
		"history type must be a known word":        `INSERT INTO backup_history (machine_id, type, status, created_at, updated_at) VALUES (1, 'prune', 'pending', 1, 1)`,
		"destination locations are unique":         `INSERT INTO backup_destinations (name, type, config, secrets, password, location, created_at, updated_at) VALUES ('x', 's3', '{}', '{}', x'03', 's3:https://example.r2.cloudflarestorage.com/g4f4r0-backups-apps', 1, 1)`,
		"destinations need their password":         `INSERT INTO backup_destinations (name, type, config, secrets, location, created_at, updated_at) VALUES ('y', 's3', '{}', '{}', 'l', 1, 1)`,
		"retention must be positive":               `INSERT INTO backup_settings (machine_id, destination_id, schedule, retention_days, created_at, updated_at) VALUES (1, 1, '30 3 * * *', 0, 1, 1)`,
		"settings need an existing destination":    `INSERT INTO backup_settings (machine_id, destination_id, schedule, retention_days, created_at, updated_at) VALUES (1, 99, '30 3 * * *', 30, 1, 1)`,
		"severity must be a known word":            `INSERT INTO notifications (event, severity, status, message, created_at, updated_at) VALUES ('disk.full', 'high', 'pending', 'c', 1, 1)`,
		"status must be a known word":              `INSERT INTO notifications (event, severity, status, message, created_at, updated_at) VALUES ('disk.full', 'warning', 'resolved', 'c', 1, 1)`,
		"a sent notification has sent_at":          `INSERT INTO notifications (event, severity, status, message, created_at, updated_at) VALUES ('disk.full', 'warning', 'sent', 'c', 1, 1)`,
		"only a sent one has sent_at":              `INSERT INTO notifications (event, severity, status, message, sent_at, created_at, updated_at) VALUES ('disk.full', 'warning', 'pending', 'c', 1, 1, 1)`,
		"a delivery has a notification":            `INSERT INTO notification_deliveries (channel_id, status, created_at, updated_at) VALUES (1, 'pending', 1, 1)`,
		"a channel's events are a JSON list":       `INSERT INTO notification_channels (name, type, config, events, created_at, updated_at) VALUES ('x', 'email', '{}', '"*"', 1, 1)`,
		"machine names are unique":                 `INSERT INTO machines (name, token_hash, created_at, updated_at) VALUES ('g4f4r0-apps', x'01', 1, 1)`,
	}
	for name, query := range bad {
		if err := exec(t, d, query); err == nil {
			t.Errorf("%s: insert was accepted", name)
		}
	}

	must(t, d, `INSERT INTO backup_settings (machine_id, destination_id, schedule, retention_days, created_at, updated_at) VALUES (1, 1, '30 3 * * *', 30, 1, 1)`)
	if err := exec(t, d, `DELETE FROM backup_destinations WHERE id = 1`); err == nil {
		t.Error("a destination in use was deleted")
	}
}

// A real v0.2.2 hub database, made by that release's binary with three recipients, upgrades: each
// recipient becomes an email channel named after its address, getting every alert.
func TestV022RecipientsBecomeEmailChannels(t *testing.T) {
	gz, err := os.ReadFile("testdata/v022-hub.db.gz")
	if err != nil {
		t.Fatal(err)
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hub.db")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if n := rows(t, d, `SELECT count(*) FROM notification_recipients`); n != 3 {
		t.Fatalf("the v0.2.2 database has %d recipients, want 3", n)
	}
	if err := d.Migrate(context.Background(), migrations()...); err != nil {
		t.Fatal(err)
	}
	if n := rows(t, d, `SELECT count(*) FROM sqlite_schema WHERE name = 'notification_recipients'`); n != 0 {
		t.Error("notification_recipients is still there")
	}
	addTestToken(t, d)
	srv := httptest.NewServer(newHub(d, testKey, nil, quiet))
	defer srv.Close()
	status, body := restCall(t, srv.URL, "GET", "/v1/notifications/channels", "", nil)
	var list server.List[notifications.Channel]
	if status != 200 || json.Unmarshal([]byte(body), &list) != nil {
		t.Fatalf("list: %d %s", status, body)
	}
	want := map[string]string{"ops-example-com": "ops@example.com", "alerts-team-tender-example-org": "Alerts.Team+tender@Example.ORG",
		"first-last-sub-example-co-uk": "first_last@sub.example.co.uk"}
	if len(list.Data) != 3 {
		t.Fatalf("channels: %s", body)
	}
	for _, c := range list.Data {
		if want[c.Name] != c.Config["to"] || c.Type != "email" || !c.Enabled || c.MinSeverity != "info" || !server.ValidName(c.Name) ||
			c.CreatedAt.Unix() != 1794751710 {
			t.Errorf("channel %+v", c)
		}
	}
	// The upgraded hub works on: a new channel, and the machine v0.2.2 added.
	if status, body := restCall(t, srv.URL, "POST", "/v1/notifications/channels", "", map[string]any{"name": "more", "type": "email",
		"config": map[string]any{"to": "more@example.com"}}); status != 201 {
		t.Errorf("add: %d %s", status, body)
	}
	if status, body := restCall(t, srv.URL, "GET", "/v1/machines/apps", "", nil); status != 200 {
		t.Errorf("machine: %d %s", status, body)
	}
}

// The migration names a channel after its address even when two addresses spell the same name,
// or an address has no letter or digit to keep.
func TestRecipientNamesMigrate(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	files := fstest.MapFS{}
	for _, name := range []string{"001_init.sql", "002_recipients.sql", "003_services.sql"} {
		b, err := fs.ReadFile(notifications.Migrations.Files, "migrations/"+name)
		if err != nil {
			t.Fatal(err)
		}
		files["migrations/"+name] = &fstest.MapFile{Data: b}
	}
	ctx := context.Background()
	if err := d.Migrate(ctx, machines.Migrations, db.Migrations{Domain: "notifications", Files: files, Dir: "migrations"}); err != nil {
		t.Fatal(err)
	}
	must(t, d, `INSERT INTO notification_recipients (id, email, created_at, updated_at) VALUES
		(1, 'ops@example.com', 1, 1), (2, 'ops.example@com', 1, 1), (3, 'Ops@Example-Com', 2, 2), (4, '__@__', 3, 3), (5, 'ünï@x.de', 4, 4)`)
	if err := d.Migrate(ctx, migrations()...); err != nil {
		t.Fatal(err)
	}
	got, err := d.Read.Query(`SELECT name, json_extract(config, '$.to') FROM notification_channels ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = got.Close() }()
	var names []string
	for got.Next() {
		var name, to string
		if err := got.Scan(&name, &to); err != nil {
			t.Fatal(err)
		}
		if !server.ValidName(name) {
			t.Errorf("%s: %q is not a valid name", to, name)
		}
		names = append(names, name+" "+to)
	}
	want := []string{"ops-example-com ops@example.com", "ops-example-com-2 ops.example@com", "ops-example-com-3 Ops@Example-Com",
		"email-4 __@__", "n--x-de ünï@x.de"}
	if !slices.Equal(names, want) {
		t.Errorf("names %q, want %q", names, want)
	}
}

// Migration 002 rebuilds backup_destinations. A database from before it keeps its destinations,
// with empty credentials, and the link from backup_settings still holds.
func TestDestinationSecretsMigrationKeepsRows(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	init, err := fs.ReadFile(backups.Migrations.Files, "migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	before := db.Migrations{Domain: "backups", Files: fstest.MapFS{"migrations/001_init.sql": {Data: init}}, Dir: "migrations"}
	ctx := context.Background()
	if err := d.Migrate(ctx, machines.Migrations, before); err != nil {
		t.Fatal(err)
	}
	must(t, d, `INSERT INTO backup_destinations (id, name, type, endpoint, bucket, created_at, updated_at)
	            VALUES (1, 'old', 's3', 'https://example.r2.cloudflarestorage.com', 'b', 5, 6)`)

	if err := d.Migrate(ctx, migrations()...); err != nil {
		t.Fatal(err)
	}
	if n := rows(t, d, `SELECT count(*) FROM backup_destinations WHERE id = 1 AND name = 'old' AND created_at = 5 AND updated_at = 6 AND length(password) = 0`); n != 1 {
		t.Errorf("the old destination did not survive the rebuild")
	}
	must(t, d, `INSERT INTO machines (id, name, token_hash, created_at, updated_at) VALUES (1, 'a', x'00', 1, 1)`)
	must(t, d, `INSERT INTO backup_settings (machine_id, destination_id, schedule, retention_days, created_at, updated_at) VALUES (1, 1, '30 3 * * *', 30, 1, 1)`)
	if err := exec(t, d, `DELETE FROM backup_destinations WHERE id = 1`); err == nil {
		t.Error("a destination in use was deleted after the rebuild")
	}
}

// base is the event without .escalated or .resolved: the problem it is about.
func TestNotificationBase(t *testing.T) {
	d := schema(t)
	must(t, d, `INSERT INTO notifications (event, severity, status, message, created_at, updated_at) VALUES
		('disk.full', 'warning', 'pending', 'c', 1, 1), ('disk.full.escalated', 'critical', 'pending', 'c', 1, 1),
		('disk.full.resolved', 'critical', 'pending', 'c', 1, 1), ('notification.test', 'info', 'pending', 'c', 1, 1)`)
	if n := rows(t, d, `SELECT count(*) FROM notifications WHERE base = 'disk.full'`); n != 3 {
		t.Errorf("%d notifications of disk.full, want 3", n)
	}
	if n := rows(t, d, `SELECT count(*) FROM notifications WHERE base = 'notification.test'`); n != 1 {
		t.Errorf("the test's base is not its event")
	}
}

func TestDeletingAMachineCascades(t *testing.T) {
	d := schema(t)
	must(t, d, `INSERT INTO machines (id, name, token_hash, created_at, updated_at) VALUES (1, 'g4f4r0-code', x'00', 1, 1)`)
	must(t, d, `INSERT INTO machine_usage (machine_id, cpu_load, created_at, updated_at) VALUES (1, 0.5, 1, 1)`)
	must(t, d, `INSERT INTO backup_snapshots (id, machine_id, restic_id, size_bytes, paths, created_at, updated_at) VALUES (1, 1, 'abc', 10, '["/"]', 1, 1)`)
	must(t, d, `INSERT INTO backup_history (machine_id, snapshot_id, type, status, created_at, updated_at) VALUES (1, 1, 'backup', 'completed', 1, 1)`)

	must(t, d, `DELETE FROM backup_snapshots WHERE id = 1`)
	if n := rows(t, d, `SELECT count(*) FROM backup_history WHERE snapshot_id IS NULL`); n != 1 {
		t.Error("deleting a snapshot did not set backup_history.snapshot_id to NULL")
	}
	must(t, d, `DELETE FROM machines WHERE id = 1`)
	for _, table := range []string{"machine_usage", "backup_history"} {
		if n := rows(t, d, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s kept %d rows after its machine was deleted", table, n)
		}
	}
}
