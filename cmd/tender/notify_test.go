package main

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/logs"
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
	if _, err := bs.AddDestination(ctx, backups.DestinationInput{Name: "r2", Config: map[string]any{"endpoint": "https://x.r2.cloudflarestorage.com", "bucket": "bkt", "access_key_id": "k", "secret_access_key": "s"}}); err != nil {
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
	if err := ss.Replace(ctx, 1, []services.ContainerInput{{Name: "web-1", ContainerID: "c1", Image: "nginx", State: "exited", ExitCode: &code, RestartCount: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := ss.Replace(ctx, 1, []services.ContainerInput{{Name: "web-1", ContainerID: "c1", Image: "nginx", State: "exited", ExitCode: &code, RestartCount: 2}}); err != nil {
		t.Fatal(err)
	}

	ls := logs.NewStore(d, testKey)
	sink, err := ls.AddSink(ctx, logs.SinkInput{Name: "otel", Type: "otlp", Config: map[string]any{"endpoint": "https://otel.example.com/v1/logs"}})
	if err != nil {
		t.Fatal(err)
	}
	sendErr := "the sink answered 503 Service Unavailable"
	if err := ls.SetStatus(ctx, 1, logs.StatusReport{Data: []logs.StatusInput{{SinkID: sink.ID, FailingSince: &at, LastError: &sendErr, LastDroppedAt: &at}}}); err != nil {
		t.Fatal(err)
	}

	load := loadMachines(ms, bs, ss, ls, newSecurity(d, ms), newUpdates(d, ms, newActions(d, testKey)))
	got, err := load(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	a, b := got[0], got[1]
	if a.ID != 1 || a.Name != "a" || a.LastSeen == nil || !a.LastSeen.Equal(at) || a.DiskUsedPercent != 91.5 ||
		!a.HasBackupSettings || a.BackupSettingsCreatedAt.IsZero() || !a.LastCompletedBackup.Equal(at.Add(-time.Hour)) ||
		a.LastBackup == nil || a.LastBackup.Status != "failed" || a.LastBackup.Error != "boom" || !a.LastBackup.Time.Equal(at) {
		t.Errorf("a = %+v, last backup %+v", a, a.LastBackup)
	}
	want := []notifications.Service{{Name: "web-1", Container: "web-1", State: "exited", ExitCode: &code, RestartCount: 2, RestartPolicy: "no", AlertsEnabled: true, DownReports: 2}}
	if !reflect.DeepEqual(a.Services, want) || a.Docker != "running" || a.DockerUnreachableReports != 0 ||
		a.DockerReportedAt == nil || time.Since(*a.DockerReportedAt) > time.Minute {
		t.Errorf("a's services = %+v, docker %q; want %+v", a.Services, a.Docker, want)
	}
	if len(a.Findings) != 1 || a.Findings[0].Event != "logs.shipping_failing" || a.Findings[0].Severity != "warning" ||
		!strings.Contains(a.Findings[0].Message, "Log shipping from a to otel has failed since") || !strings.Contains(a.Findings[0].Message, sendErr) ||
		!strings.Contains(a.Findings[0].Message, "dropped logs at") || b.Findings != nil {
		t.Errorf("findings: a %+v, b %+v", a.Findings, b.Findings)
	}
	for range 2 {
		if err := ss.Unreachable(ctx, 1, services.ReasonRefused); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := load(ctx); err != nil || got[0].Docker != "unreachable" || got[0].DockerUnreachableReports != 2 ||
		got[0].Services[0].State != "unknown" {
		t.Errorf("after two unreachable reports: %+v, %v", got[0], err)
	}
	if b.Name != "b" || b.Services != nil || b.Docker != "" || b.DockerReportedAt != nil || b.LastSeen != nil || b.HasBackupSettings || b.LastBackup != nil || !b.LastCompletedBackup.IsZero() || b.CreatedAt.IsZero() {
		t.Errorf("b = %+v", b)
	}
}

// Docker turned off resolves the machine's docker.unreachable and its services' problems, through
// the real stores and rules, and nothing opens again on the next run.
func TestDockerOffResolves(t *testing.T) {
	d := schema(t)
	ctx := context.Background()
	ms, bs, ss := machines.NewStore(d), backups.NewStore(d, testKey), services.NewStore(d)
	if _, err := ms.Create(ctx, "ci"); err != nil {
		t.Fatal(err)
	}
	load := loadMachines(ms, bs, ss, logs.NewStore(d, testKey), newSecurity(d, ms), newUpdates(d, ms, newActions(d, testKey)))
	engine := notifications.NewEngine(notifications.NewStore(d, testKey), nil, notifications.Hub{Name: "hub", URL: "http://hub"},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	tick := func(reports ...services.Report) []string {
		t.Helper()
		for _, r := range reports {
			if err := ss.Apply(ctx, 1, r); err != nil {
				t.Fatal(err)
			}
		}
		now := time.Now()
		if _, err := ms.AddUsage(ctx, 1, machines.UsageInput{DiskUsedPercent: 10, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		list, err := load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Tick(ctx, now, list); err != nil {
			t.Fatal(err)
		}
		r, err := d.Read.Query(`SELECT event FROM notifications ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		var events []string
		for r.Next() {
			var e string
			if err := r.Scan(&e); err != nil {
				t.Fatal(err)
			}
			events = append(events, e)
		}
		return events
	}
	code := int64(1)
	down := services.RunningReport([]services.ContainerInput{{Name: "web", ContainerID: "c1", Image: "nginx", State: "exited", ExitCode: &code}})
	unreachable := services.UnreachableReport(services.ReasonRefused)
	if got, want := tick(down, down), []string{"service.down"}; !slices.Equal(got, want) {
		t.Fatalf("web down: %v, want %v", got, want)
	}
	if got, want := tick(unreachable, unreachable), []string{"service.down", "docker.unreachable"}; !slices.Equal(got, want) {
		t.Fatalf("Docker unreachable: %v, want %v", got, want)
	}
	want := []string{"service.down", "docker.unreachable", "service.down.resolved", "docker.unreachable.resolved"}
	if got := tick(services.OffReport()); !slices.Equal(got, want) {
		t.Errorf("after Docker is off: %v, want %v", got, want)
	}
	if got := tick(services.OffReport()); !slices.Equal(got, want) {
		t.Errorf("after a second off report: %v, want %v", got, want)
	}
}
