package main

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/metrics"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/services"
)

// metricSource reads one domain's state as metric families, at now.
type metricSource func(ctx context.Context, now time.Time) ([]*metrics.Family, error)

// metricSources are what GET /metrics shows, in order.
func metricSources(d *db.DB) []metricSource {
	return []metricSource{
		buildMetrics,
		machineMetrics(machines.NewStore(d)),
		serviceMetrics(services.NewStore(d)),
		backupMetrics(backups.NewStore(d, nil)),
		alertMetrics(notifications.NewStore(d, nil)),
		// The log-shipping counters join here.
	}
}

// metricsRoute serves GET /metrics in the Prometheus text format. It builds the whole page
// first, so a failed read answers 500 instead of half a page.
func metricsRoute(sources []metricSource, now func() time.Time) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		at := now()
		var fams []*metrics.Family
		for _, src := range sources {
			f, err := src(r.Context(), at)
			if err != nil {
				return err
			}
			fams = append(fams, f...)
		}
		var page bytes.Buffer
		if err := metrics.Write(&page, fams); err != nil {
			return err
		}
		w.Header().Set("Content-Type", metrics.ContentType)
		w.Header().Set("Cache-Control", "no-store")
		_, err := page.WriteTo(w)
		return err
	}
}

func buildMetrics(context.Context, time.Time) ([]*metrics.Family, error) {
	f := metrics.New("tender_build_info", metrics.Gauge, "The hub's version, as the version label; always 1.")
	f.Add(1, "version", version)
	return []*metrics.Family{f}, nil
}

func unix(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }

func machineMetrics(s *machines.Store) metricSource {
	return func(ctx context.Context, _ time.Time) ([]*metrics.Family, error) {
		list, err := s.List(ctx)
		if err != nil {
			return nil, err
		}
		seen := metrics.New("tender_machine_last_seen_timestamp_seconds", metrics.Gauge,
			"When the machine's daemon measured its latest reading, in Unix seconds. A machine that never reported has no series.")
		load := metrics.New("tender_machine_cpu_load", metrics.Gauge, "The machine's 1-minute load average, from its latest reading.")
		mem := metrics.New("tender_machine_memory_used_percent", metrics.Gauge, "The machine's memory in use, 0 to 100, from its latest reading.")
		disk := metrics.New("tender_machine_disk_used_percent", metrics.Gauge, "The root file system's space in use, 0 to 100, from the latest reading.")
		up := metrics.New("tender_machine_uptime_seconds", metrics.Gauge, "How long the machine had been up at its latest reading.")
		for _, m := range list {
			if m.Usage == nil || m.LastSeen == nil {
				continue
			}
			l := []string{"machine", m.Name}
			seen.Add(unix(*m.LastSeen), l...)
			load.Add(m.Usage.CPULoad, l...)
			mem.Add(m.Usage.MemoryUsedPercent, l...)
			disk.Add(m.Usage.DiskUsedPercent, l...)
			up.Add(float64(m.Usage.UptimeSeconds), l...)
		}
		return []*metrics.Family{seen, load, mem, disk, up}, nil
	}
}

func serviceMetrics(s *services.Store) metricSource {
	return func(ctx context.Context, _ time.Time) ([]*metrics.Family, error) {
		list, err := s.Machines(ctx, 0)
		if err != nil {
			return nil, err
		}
		docker := metrics.New("tender_docker_up", metrics.Gauge,
			"1 while the machine's daemon reaches its container runtime, 0 while it can't. A machine without a report has no series.")
		state := metrics.New("tender_service_state", metrics.Gauge,
			"1 for the service's current state: created, running, paused, restarting, exited, removing, dead, unknown or removed.")
		mem := metrics.New("tender_service_memory_bytes", metrics.Gauge, "The service's memory in use, less the page cache it can drop, while it runs.")
		limit := metrics.New("tender_service_memory_limit_bytes", metrics.Gauge, "The service's memory limit, when it has one.")
		cpu := metrics.New("tender_service_cpu_percent", metrics.Gauge, "The service's CPU use since the previous report; 100 is one whole CPU.")
		restarts := metrics.New("tender_service_restart_count", metrics.Gauge, "How often the runtime restarted the service's container.")
		for _, m := range list {
			if m.Docker != nil {
				docker.Add(map[bool]float64{true: 1}[*m.Docker == services.DockerRunning], "machine", m.Machine)
			}
			for _, sv := range m.Data {
				l := []string{"machine", m.Machine, "service", sv.Name}
				state.Add(1, append(l, "runtime", sv.Runtime, "state", sv.State)...)
				restarts.Add(float64(sv.RestartCount), l...)
				if sv.MemoryBytes != nil {
					mem.Add(float64(*sv.MemoryBytes), l...)
				}
				if sv.MemoryLimitBytes != nil {
					limit.Add(float64(*sv.MemoryLimitBytes), l...)
				}
				if sv.CPUPercent != nil {
					cpu.Add(*sv.CPUPercent, l...)
				}
			}
		}
		return []*metrics.Family{docker, state, mem, limit, cpu, restarts}, nil
	}
}

func backupMetrics(s *backups.Store) metricSource {
	return func(ctx context.Context, now time.Time) ([]*metrics.Family, error) {
		list, err := s.Summaries(ctx)
		if err != nil {
			return nil, err
		}
		at := metrics.New("tender_backup_last_timestamp_seconds", metrics.Gauge, "When the machine's newest snapshot was taken, in Unix seconds.")
		age := metrics.New("tender_backup_last_age_seconds", metrics.Gauge, "How old the machine's newest snapshot is.")
		size := metrics.New("tender_backup_last_size_bytes", metrics.Gauge, "The size of the machine's newest snapshot, when restic reported it.")
		count := metrics.New("tender_backup_snapshots", metrics.Gauge, "How many snapshots the machine has.")
		failed := metrics.New("tender_backup_last_run_failed", metrics.Gauge,
			"1 when the machine's newest backup run failed, 0 when it didn't. A machine without a run has no series.")
		for _, sum := range list {
			l := []string{"machine", sum.Machine}
			count.Add(float64(sum.SnapshotCount), l...)
			if sn := sum.LatestSnapshot; sn != nil {
				at.Add(unix(sn.CreatedAt), l...)
				age.Add(max(now.Sub(sn.CreatedAt).Seconds(), 0), l...)
				if sn.SizeBytes != nil {
					size.Add(float64(*sn.SizeBytes), l...)
				}
			}
			if run := sum.LastBackup; run != nil {
				failed.Add(map[bool]float64{true: 1}[run.Status == backups.StatusFailed], l...)
			}
		}
		return []*metrics.Family{at, age, size, count, failed}, nil
	}
}

func alertMetrics(s *notifications.Store) metricSource {
	return func(ctx context.Context, _ time.Time) ([]*metrics.Family, error) {
		counts, err := s.OpenAlerts(ctx)
		if err != nil {
			return nil, err
		}
		open := metrics.New("tender_alerts_open", metrics.Gauge, "How many alerts are open, by type and severity.")
		for _, c := range counts {
			open.Add(float64(c.Count), "type", c.Type, "severity", c.Severity)
		}
		return []*metrics.Family{open}, nil
	}
}
