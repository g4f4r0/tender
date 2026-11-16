package main

import (
	"context"
	"os"
	"time"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/logs"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/security"
	"github.com/g4f4r0/tender/internal/services"
	"github.com/g4f4r0/tender/internal/updates"
)

// hubName is the host name that emails say they were sent from.
func hubName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown"
	}
	return name
}

// hubIdentity names the hub in what it sends: its host name, and its URL from TENDER_HUB_URL,
// else http:// and the host name. The hub checked TENDER_HUB_URL when it started.
func hubIdentity() notifications.Hub {
	h := notifications.Hub{Name: hubName()}
	if u, err := config.LoadHubURL(os.Getenv); err == nil && u != "" {
		h.URL = u
	} else {
		h.URL = "http://" + h.Name
	}
	return h
}

// loadMachines returns what the notification rules need to know about every machine, from the
// machines, backups, services, logs, security and updates stores. The notifications domain imports none of them.
func loadMachines(ms *machines.Store, bs *backups.Store, ss *services.Store, ls *logs.Store, sec *security.Store,
	up *updates.Store) func(context.Context) ([]notifications.Machine, error) {
	return func(ctx context.Context) ([]notifications.Machine, error) {
		overviews, err := ms.List(ctx)
		if err != nil {
			return nil, err
		}
		summaries, err := bs.Summaries(ctx)
		if err != nil {
			return nil, err
		}
		completed, err := bs.LastCompleted(ctx)
		if err != nil {
			return nil, err
		}
		finished, err := bs.LastFinished(ctx)
		if err != nil {
			return nil, err
		}
		engines, err := ss.Machines(ctx, 0)
		if err != nil {
			return nil, err
		}
		byMachine := map[string]services.MachineServices{}
		for _, e := range engines {
			byMachine[e.Machine] = e
		}
		failures, err := ss.Failures(ctx)
		if err != nil {
			return nil, err
		}
		health, err := ls.Health(ctx)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		findings := logFindings(health, now)
		byName := map[string]backups.Summary{}
		for _, s := range summaries {
			byName[s.Machine] = s
		}
		out := make([]notifications.Machine, len(overviews))
		for i, o := range overviews {
			m := notifications.Machine{ID: o.ID, Name: o.Name, CreatedAt: o.CreatedAt, LastSeen: o.LastSeen, LastCompletedBackup: completed[o.Name],
				Findings: append(findings[o.Name], deployFindings(o.Name, failures[o.ID])...)}
			if e, ok := byMachine[o.Name]; ok {
				if e.Docker != nil {
					m.Docker = *e.Docker
				}
				if e.DockerReason != nil {
					m.DockerReason = *e.DockerReason
				}
				m.DockerUnreachableReports, m.DockerReportedAt = e.UnreachableReports(), e.ReportedAt()
				for _, sv := range e.Data {
					for _, c := range sv.Containers {
						m.Services = append(m.Services, notifications.Service{
							Name: sv.Name, Container: c.Name, State: c.State, ExitCode: c.ExitCode, RestartCount: c.RestartCount,
							RestartPolicy: c.RestartPolicy, AlertsEnabled: sv.AlertsEnabled, StoppedByTender: c.StoppedByTender,
							MemoryBytes: c.MemoryBytes, MemoryLimitBytes: c.MemoryLimitBytes,
							DownReports: c.DownReports, RestartingReports: c.RestartingReports, UnhealthyReports: c.UnhealthyReports,
							MemoryHighReports: c.MemoryHighReports,
						})
					}
				}
			}
			if o.Usage != nil {
				m.DiskUsedPercent = o.Usage.DiskUsedPercent
			}
			s := byName[o.Name]
			if s.Settings != nil {
				m.HasBackupSettings, m.BackupSettingsCreatedAt = true, s.Settings.CreatedAt
			}
			if h, ok := finished[o.Name]; ok {
				m.LastBackup = &notifications.Run{Status: h.Status, Time: h.CreatedAt}
				if h.Error != nil {
					m.LastBackup.Error = *h.Error
				}
			}
			out[i] = m
		}
		allowed, err := withPublicPorts(ctx, ss, overviews)
		if err != nil {
			return nil, err
		}
		return out, systemRules(ctx, allowed, sec, up, out, now)
	}
}
