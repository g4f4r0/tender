package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/machines"
)

func TestLoadMachines(t *testing.T) {
	database := filepath.Join(t.TempDir(), "hub.db")
	mustCLI(t, database, "add-machine", "a")
	mustCLI(t, database, "add-machine", "b")
	mustCLI(t, database, "add-destination", "r2", "--type", "s3", "--endpoint", "https://x.r2.cloudflarestorage.com", "--bucket", "b")
	mustCLI(t, database, "set-backup", "a", "--destination", "r2", "--schedule", "30 3 * * *", "--retention-days", "30")

	ctx := context.Background()
	d, err := openHub(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	}()
	ms, bs := machines.NewStore(d), backups.NewStore(d)
	at := time.Date(2026, 11, 14, 3, 30, 0, 0, time.UTC)
	if _, err := ms.AddUsage(ctx, 1, machines.UsageInput{DiskUsedPercent: 91.5, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	failure := "boom"
	for _, h := range []backups.HistoryInput{
		{Type: "backup", Status: "completed", CreatedAt: at.Add(-time.Hour)},
		{Type: "backup", Status: "failed", Error: &failure, CreatedAt: at},
	} {
		if _, err := bs.CreateHistory(ctx, 1, h); err != nil {
			t.Fatal(err)
		}
	}

	got, err := loadMachines(ms, bs)(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	a, b := got[0], got[1]
	if a.ID != 1 || a.Name != "a" || a.LastSeen == nil || !a.LastSeen.Equal(at) || a.DiskUsedPercent != 91.5 ||
		!a.HasBackupSettings || a.BackupSettingsCreatedAt.IsZero() || !a.LastCompletedBackup.Equal(at.Add(-time.Hour)) ||
		a.LastBackup == nil || a.LastBackup.Status != "failed" || a.LastBackup.Error != "boom" || !a.LastBackup.Time.Equal(at) {
		t.Errorf("a = %+v, last backup %+v", a, a.LastBackup)
	}
	if b.Name != "b" || b.LastSeen != nil || b.HasBackupSettings || b.LastBackup != nil || !b.LastCompletedBackup.IsZero() || b.CreatedAt.IsZero() {
		t.Errorf("b = %+v", b)
	}
}

func TestHubConfigFlagsOverrideFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(path, []byte("listen = \"127.0.0.1:9\"\ndatabase = \"/tmp/from-file.db\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ database, listen, wantDatabase, wantListen string }{
		{"", "", "/tmp/from-file.db", "127.0.0.1:9"},
		{"/tmp/flag.db", "", "/tmp/flag.db", "127.0.0.1:9"},
		{"", "0.0.0.0:1", "/tmp/from-file.db", "0.0.0.0:1"},
	} {
		cfg, err := hubConfig(path, c.database, c.listen)
		if err != nil || cfg.Database != c.wantDatabase || cfg.Listen != c.wantListen {
			t.Errorf("flags %q %q: %+v, %v", c.database, c.listen, cfg, err)
		}
	}

	// Admin commands use the file's database when --database is left out.
	database := filepath.Join(t.TempDir(), "hub.db")
	if err := os.WriteFile(path, []byte("database = \""+database+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := hubCommand("add-machine", []string{"--config", path, "a"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(database); err != nil {
		t.Errorf("the file's database wasn't used: %v", err)
	}
}
