package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/metrics"
	"github.com/g4f4r0/tender/internal/services"
)

func TestMetrics(t *testing.T) {
	d := seeded(t)
	ctx := context.Background()
	mem, limit, cpu := int64(200<<20), int64(1<<30), 12.5
	web := services.ServiceInput{Name: "web", ContainerID: "c1", Image: "nginx", State: services.StateRunning, RestartCount: 2,
		MemoryBytes: &mem, MemoryLimitBytes: &limit, CPUPercent: &cpu}
	old := services.ServiceInput{Name: "old", ContainerID: "c2", Image: "x", State: "exited"}
	if err := services.NewStore(d).Apply(ctx, 1, services.RunningReport([]services.ServiceInput{web, old})); err != nil {
		t.Fatal(err)
	}
	if err := services.NewStore(d).Apply(ctx, 2, services.UnreachableReport("refused")); err != nil {
		t.Fatal(err)
	}

	// The hub serves it to people, as text.
	hub := httptest.NewServer(newHub(d, testKey, nil, quiet))
	defer hub.Close()
	res, err := http.Get(hub.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != metrics.ContentType || !strings.Contains(string(body), "# TYPE tender_alerts_open gauge") {
		t.Fatalf("GET /metrics: %d %q\n%s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}

	// The values, at a fixed time: 2026-11-15 04:00, an hour after the newest snapshot.
	now := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	page := scrape(t, metricsRoute(metricSources(d), func() time.Time { return now }))
	for _, want := range []string{
		`tender_build_info{version="dev"} 1`,
		`tender_machine_last_seen_timestamp_seconds{machine="g4f4r0-code"} 1794628800`,
		`tender_machine_cpu_load{machine="g4f4r0-code"} 1.5`,
		`tender_machine_memory_used_percent{machine="g4f4r0-code"} 40`,
		`tender_machine_disk_used_percent{machine="g4f4r0-code"} 50`,
		`tender_machine_uptime_seconds{machine="g4f4r0-code"} 3600`,
		`tender_docker_up{machine="g4f4r0-code"} 1`,
		`tender_docker_up{machine="g4f4r0-apps"} 0`,
		`tender_service_state{machine="g4f4r0-code",service="web",runtime="docker",state="running"} 1`,
		`tender_service_state{machine="g4f4r0-code",service="old",runtime="docker",state="exited"} 1`,
		`tender_service_memory_bytes{machine="g4f4r0-code",service="web"} 209715200`,
		`tender_service_memory_limit_bytes{machine="g4f4r0-code",service="web"} 1073741824`,
		`tender_service_cpu_percent{machine="g4f4r0-code",service="web"} 12.5`,
		`tender_service_restart_count{machine="g4f4r0-code",service="web"} 2`,
		`tender_backup_last_timestamp_seconds{machine="g4f4r0-code"} 1794711600`,
		`tender_backup_last_age_seconds{machine="g4f4r0-code"} 3600`,
		`tender_backup_snapshots{machine="g4f4r0-code"} 2`,
		`tender_backup_snapshots{machine="g4f4r0-apps"} 0`,
		`tender_backup_last_run_failed{machine="g4f4r0-code"} 1`,
		`tender_alerts_open{type="backup_failed",severity="critical"} 1`,
		`tender_alerts_open{type="disk_full",severity="warning"} 0`,
		`tender_alerts_open{type="hub_restarted",severity="info"} 1`,
	} {
		if !strings.Contains(page, "\n"+want+"\n") {
			t.Errorf("no line %s", want)
		}
	}
	// No reading, no snapshot size, no run: no series, not a made-up zero.
	for _, absent := range []string{`tender_machine_cpu_load{machine="g4f4r0-apps"}`, `tender_backup_last_size_bytes{`,
		`tender_backup_last_run_failed{machine="g4f4r0-apps"}`, `tender_service_memory_bytes{machine="g4f4r0-code",service="old"}`} {
		if strings.Contains(page, absent) {
			t.Errorf("has %s", absent)
		}
	}

	// A failed read answers 500, not half a page.
	broken := metricsRoute([]metricSource{buildMetrics, func(context.Context, time.Time) ([]*metrics.Family, error) {
		return nil, errors.New("disk I/O error")
	}}, time.Now)
	rec := httptest.NewRecorder()
	if err := broken(rec, httptest.NewRequest("GET", "/metrics", nil)); err == nil || rec.Body.Len() != 0 {
		t.Errorf("a failed source: %v, wrote %q", err, rec.Body.String())
	}
}

func scrape(t *testing.T, h func(http.ResponseWriter, *http.Request) error) string {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := h(rec, httptest.NewRequest("GET", "/metrics", nil)); err != nil {
		t.Fatal(err)
	}
	return "\n" + rec.Body.String()
}
