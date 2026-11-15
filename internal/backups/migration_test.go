package backups_test

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/secret"
)

// A v0.2 hub's s3 destination, with a machine backing up to it, becomes a type and a config with
// its keys still sealed, and the machine's settings still point at it.
func TestDestinationConfigMigration(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Write.Exec(`CREATE TABLE machines (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, token_hash BLOB NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) STRICT; INSERT INTO machines VALUES (1, 'a', x'00', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, name := range []string{"001_init.sql", "002_destination_secrets.sql"} {
		b, err := fs.ReadFile(backups.Migrations.Files, "migrations/"+name)
		if err != nil {
			t.Fatal(err)
		}
		old["migrations/"+name] = &fstest.MapFile{Data: b}
	}
	if err := d.Migrate(ctx, db.Migrations{Domain: "backups", Files: old, Dir: "migrations"}); err != nil {
		t.Fatal(err)
	}
	seal := func(v string) []byte {
		b, err := secret.Seal(testKey, []byte(v))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	_, err = d.Write.Exec(`INSERT INTO backup_destinations (id, name, type, endpoint, bucket, path, access_key_id, secret_access_key, password, created_at, updated_at)
		VALUES (7, 'r2', 's3', 'https://x.r2.cloudflarestorage.com', 'tender', 'hosts', ?, ?, ?, 100, 200),
		       (8, 'top', 's3', 'https://y.example.com', 'b', '', ?, ?, ?, 100, 200)`,
		seal("old-id"), seal("old-secret"), seal("old-password"), seal("k2"), seal("s2"), seal("p2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec(`INSERT INTO backup_settings VALUES (1, 1, 7, '30 3 * * *', 30, 100, 200)`); err != nil {
		t.Fatal(err)
	}

	if err := d.Migrate(ctx, backups.Migrations); err != nil {
		t.Fatalf("migrate with a machine backing up: %v", err)
	}
	s := backups.NewStore(d, testKey)
	if err := s.CheckKey(ctx); err != nil {
		t.Fatalf("CheckKey after the migration: %v", err)
	}
	st, err := s.DaemonSettings(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	c := st.Credentials
	if st.Destination.Name != "r2" || st.Destination.Location != "s3:https://x.r2.cloudflarestorage.com/tender/hosts" || st.Schedule != "30 3 * * *" ||
		c.Password != "old-password" || c.Config["access_key_id"] != "old-id" || c.Config["secret_access_key"] != "old-secret" || c.Config["path"] != "hosts" ||
		st.Destination.Config["secret_access_key"] != "***" {
		t.Errorf("after the migration: %+v %+v", st, c)
	}
	top, err := s.Destination(ctx, "top")
	if err != nil || top.Location != "s3:https://y.example.com/b" || top.CreatedAt.Unix() != 100 || top.UpdatedAt.Unix() != 200 {
		t.Errorf("top = %+v, %v", top, err)
	}
	// The foreign key still holds: a destination in use can't go, and settings can't point nowhere.
	if _, err := d.Write.Exec(`DELETE FROM backup_destinations WHERE id = 7`); err == nil {
		t.Error("deleted a destination a machine uses")
	}
	if _, err := d.Write.Exec(`UPDATE backup_settings SET destination_id = 99`); err == nil {
		t.Error("settings point at a destination that doesn't exist")
	}
	// Two destinations can't share a location.
	if _, err := s.AddDestination(ctx, input("again", "https://y.example.com", "b", "")); err == nil {
		t.Error("added a second destination at top's location")
	}
}
