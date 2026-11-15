package main

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/machines"
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
	return d
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
	if n := rows(t, d, `SELECT count(*) FROM migrations`); n != 13 {
		t.Fatalf("migrations rows = %d, want 13 (machines, backups three times, notifications three times, services, actions twice, logs, server twice)", n)
	}
	for _, table := range []string{"machines", "machine_usage", "backup_destinations", "backup_settings",
		"backup_snapshots", "backup_history", "notifications", "notification_recipients", "service_engines", "services", "actions", "log_sinks", "log_sink_status", "idempotency_keys"} {
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
		"severity must be a known word":            `INSERT INTO notifications (type, severity, status, content, created_at, updated_at) VALUES ('disk_full', 'high', 'pending', 'c', 1, 1)`,
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

func TestRecipientEmailsAreUniqueInAnyCase(t *testing.T) {
	d := schema(t)
	must(t, d, `INSERT INTO notification_recipients (email, created_at, updated_at) VALUES ('johan@example.com', 1, 1)`)
	if err := exec(t, d, `INSERT INTO notification_recipients (email, created_at, updated_at) VALUES ('Johan@Example.com', 1, 1)`); err == nil {
		t.Error("the same address in another case was accepted")
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

func TestOneUnresolvedNotificationPerTypeAndMachine(t *testing.T) {
	d := schema(t)
	must(t, d, `INSERT INTO machines (id, name, token_hash, created_at, updated_at) VALUES (1, 'g4f4r0-ci', x'00', 1, 1)`)
	insert := `INSERT INTO notifications (machine_id, type, severity, status, content, created_at, updated_at) VALUES (?, ?, 'warning', 'pending', 'c', 1, 1)`

	for _, machine := range []any{1, nil} {
		must(t, d, insert, machine, "disk_full")
		if err := exec(t, d, insert, machine, "disk_full"); err == nil {
			t.Errorf("machine %v: a second unresolved disk_full was accepted", machine)
		}
		must(t, d, `UPDATE notifications SET status = 'resolved' WHERE type = 'disk_full' AND machine_id IS ?`, machine)
		must(t, d, insert, machine, "disk_full")
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
