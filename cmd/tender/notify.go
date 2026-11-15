package main

import (
	"context"
	"os"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/services"
)

// hubName is the host name that emails say they were sent from.
func hubName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown"
	}
	return name
}

// loadMachines returns what the notification rules need to know about every machine, from the
// machines, backups and services stores. The notifications domain imports none of them.
func loadMachines(ms *machines.Store, bs *backups.Store, ss *services.Store) func(context.Context) ([]notifications.Machine, error) {
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
		byName := map[string]backups.Summary{}
		for _, s := range summaries {
			byName[s.Machine] = s
		}
		out := make([]notifications.Machine, len(overviews))
		for i, o := range overviews {
			m := notifications.Machine{ID: o.ID, Name: o.Name, CreatedAt: o.CreatedAt, LastSeen: o.LastSeen, LastCompletedBackup: completed[o.Name]}
			if e, ok := byMachine[o.Name]; ok {
				if e.Docker != nil {
					m.Docker = *e.Docker
				}
				m.DockerUnreachableReports = e.UnreachableReports()
				for _, s := range e.Data {
					m.Services = append(m.Services, notifications.Service{
						Name: s.Name, State: s.State, ExitCode: s.ExitCode, RestartCount: s.RestartCount, RestartPolicy: s.RestartPolicy,
						AlertsEnabled: s.AlertsEnabled, StoppedByTender: s.StoppedByTender,
						MemoryBytes: s.MemoryBytes, MemoryLimitBytes: s.MemoryLimitBytes,
						DownReports: s.DownReports, RestartingReports: s.RestartingReports, UnhealthyReports: s.UnhealthyReports,
						MemoryHighReports: s.MemoryHighReports,
					})
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
		return out, nil
	}
}
