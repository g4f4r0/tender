package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/services"
)

func TestLoadMachines(t *testing.T) {
	d := schema(t)
	ctx := context.Background()
	ms, bs := machines.NewStore(d), backups.NewStore(d, testKey)
	for _, name := range []string{"a", "b"} {
		if _, err := ms.Create(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bs.AddDestination(ctx, backups.DestinationInput{Name: "r2", Endpoint: "https://x.r2.cloudflarestorage.com", Bucket: "b",
		AccessKeyID: "k", SecretAccessKey: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bs.SetSettings(ctx, 1, "r2", "30 3 * * *", 30); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 14, 3, 30, 0, 0, time.UTC)
	if _, err := ms.AddUsage(ctx, 1, machines.UsageInput{DiskUsedPercent: 91.5, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	failure := "boom"
	for _, h := range []backups.HistoryInput{
		{Type: "backup", Status: "completed", CreatedAt: at.Add(-time.Hour)},
		{Type: "backup", Status: "failed", Error: &failure, CreatedAt: at},
		// A retry in progress doesn't hide the failure before it ends.
		{Type: "backup", Status: "in_progress", CreatedAt: at.Add(time.Minute)},
	} {
		if _, err := bs.CreateHistory(ctx, 1, h); err != nil {
			t.Fatal(err)
		}
	}

	ss := services.NewStore(d)
	code := int64(1)
	if _, err := ss.Replace(ctx, 1, []services.ServiceInput{{Name: "web-1", ContainerID: "c1", Image: "nginx", State: "exited", ExitCode: &code, RestartCount: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.Replace(ctx, 1, []services.ServiceInput{{Name: "web-1", ContainerID: "c1", Image: "nginx", State: "exited", ExitCode: &code, RestartCount: 2}}); err != nil {
		t.Fatal(err)
	}

	got, err := loadMachines(ms, bs, ss)(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	a, b := got[0], got[1]
	if a.ID != 1 || a.Name != "a" || a.LastSeen == nil || !a.LastSeen.Equal(at) || a.DiskUsedPercent != 91.5 ||
		!a.HasBackupSettings || a.BackupSettingsCreatedAt.IsZero() || !a.LastCompletedBackup.Equal(at.Add(-time.Hour)) ||
		a.LastBackup == nil || a.LastBackup.Status != "failed" || a.LastBackup.Error != "boom" || !a.LastBackup.Time.Equal(at) {
		t.Errorf("a = %+v, last backup %+v", a, a.LastBackup)
	}
	want := []notifications.Service{{Name: "web-1", State: "exited", ExitCode: &code, RestartCount: 2, AlertsEnabled: true, DownReports: 2}}
	if !reflect.DeepEqual(a.Services, want) {
		t.Errorf("a's services = %+v, want %+v", a.Services, want)
	}
	if b.Name != "b" || b.Services != nil || b.LastSeen != nil || b.HasBackupSettings || b.LastBackup != nil || !b.LastCompletedBackup.IsZero() || b.CreatedAt.IsZero() {
		t.Errorf("b = %+v", b)
	}
}
