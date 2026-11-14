package machines

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
)

func TestPruneUsage(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := context.Background()
	if err := d.Migrate(ctx, Migrations); err != nil {
		t.Fatal(err)
	}
	s := NewStore(d)
	if _, err := s.Create(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 4, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{time.Hour, 13 * 24 * time.Hour, 15 * 24 * time.Hour, 30 * 24 * time.Hour} {
		if _, err := s.AddUsage(ctx, 1, UsageInput{CreatedAt: now.Add(-age)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.pruneUsage(ctx, now); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err := d.Read.QueryRow(`SELECT count(*) FROM machine_usage`).Scan(&kept); err != nil || kept != 2 {
		t.Errorf("kept %d readings, %v, want the two from the last 14 days", kept, err)
	}
}
