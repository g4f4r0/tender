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

	// A machine silent for longer than 14 days keeps its newest reading, for last seen.
	if _, err := s.Create(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	for _, age := range []time.Duration{20 * 24 * time.Hour, 40 * 24 * time.Hour} {
		if _, err := s.AddUsage(ctx, 2, UsageInput{CreatedAt: now.Add(-age)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.pruneUsage(ctx, now); err != nil {
		t.Fatal(err)
	}
	var newest int64
	if err := d.Read.QueryRow(`SELECT count(*), max(created_at) FROM machine_usage WHERE machine_id = 2`).Scan(&kept, &newest); err != nil ||
		kept != 1 || newest != now.Add(-20*24*time.Hour).Unix() {
		t.Errorf("silent machine kept %d readings, newest %d, %v; want only its 20-day-old one", kept, newest, err)
	}
}
