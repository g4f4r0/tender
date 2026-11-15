package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

// Store reads and writes services and their actions.
type Store struct{ db *db.DB }

// NewStore returns a store on d.
func NewStore(d *db.DB) *Store { return &Store{db: d} }

var states = map[string]bool{StateCreated: true, StateRunning: true, StatePaused: true, StateRestarting: true,
	StateExited: true, StateRemoving: true, StateDead: true}

var healths = map[string]bool{HealthHealthy: true, HealthUnhealthy: true, HealthStarting: true}

var policies = map[string]bool{PolicyNo: true, PolicyAlways: true, PolicyUnlessStopped: true, PolicyOnFailure: true}

// The machine's name comes from the machines table, which the schema already ties this domain to.
const selectServices = `
SELECT s.id, m.name, s.name, s.project, s.container_id, s.image, s.state, s.health, s.exit_code, s.restart_count,
       s.restart_policy, s.started_at, s.memory_bytes, s.memory_limit_bytes, s.cpu_percent, s.alerts_enabled, s.stopped_by_tender,
       s.down_reports, s.restarting_reports, s.unhealthy_reports, s.memory_high_reports, s.created_at, s.updated_at
FROM services s JOIN machines m ON m.id = s.machine_id`

type scanner interface{ Scan(dest ...any) error }

func scanService(row scanner) (Service, error) {
	var s Service
	var started *int64
	var created, updated int64
	err := row.Scan(&s.ID, &s.Machine, &s.Name, &s.Project, &s.ContainerID, &s.Image, &s.State, &s.Health, &s.ExitCode,
		&s.RestartCount, &s.RestartPolicy, &started, &s.MemoryBytes, &s.MemoryLimitBytes, &s.CPUPercent, &s.AlertsEnabled, &s.StoppedByTender,
		&s.DownReports, &s.RestartingReports, &s.UnhealthyReports, &s.MemoryHighReports, &created, &updated)
	if started != nil {
		t := time.Unix(*started, 0).UTC()
		s.StartedAt = &t
	}
	s.CreatedAt, s.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return s, err
}

// List returns the services of one machine, or of every machine when machineID is 0, by machine
// and then by name.
func (s *Store) List(ctx context.Context, machineID int64) ([]Service, error) {
	rows, err := s.db.Read.QueryContext(ctx, selectServices+` WHERE ? = 0 OR s.machine_id = ? ORDER BY m.name, s.name`, machineID, machineID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Service{}
	for rows.Next() {
		sv, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

// Machines returns the services of one machine, or of every machine that reported any when
// machineID is 0, with what the latest report said about its Docker, by machine name.
func (s *Store) Machines(ctx context.Context, machineID int64) ([]MachineServices, error) {
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT m.name, e.docker, coalesce(e.unreachable_reports, 0)
		FROM machines m LEFT JOIN service_engines e ON e.machine_id = m.id
		WHERE m.id = ? OR (? = 0 AND (e.id IS NOT NULL OR EXISTS (SELECT 1 FROM services s WHERE s.machine_id = m.id)))
		ORDER BY m.name`, machineID, machineID)
	if err != nil {
		return nil, err
	}
	out := []MachineServices{}
	at := map[string]int{}
	for rows.Next() {
		ms := MachineServices{Data: []Service{}}
		if err := rows.Scan(&ms.Machine, &ms.Docker, &ms.unreachableReports); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		at[ms.Machine] = len(out)
		out = append(out, ms)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	list, err := s.List(ctx, machineID)
	if err != nil {
		return nil, err
	}
	for _, sv := range list {
		if i, ok := at[sv.Machine]; ok {
			out[i].Data = append(out[i].Data, sv)
		}
	}
	return out, nil
}

// Get returns one of a machine's services by name.
func (s *Store) Get(ctx context.Context, machineID int64, name string) (Service, error) {
	sv, err := scanService(s.db.Read.QueryRowContext(ctx, selectServices+` WHERE s.machine_id = ? AND s.name = ?`, machineID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return sv, server.NotFound("Service %q doesn't exist on this machine.", name)
	}
	return sv, err
}

func (in ServiceInput) validate() error {
	switch {
	case in.Name == "" || len(in.Name) > 255:
		return server.Invalid("Every service needs a name of 1 to 255 characters.")
	case in.ContainerID == "":
		return server.Invalid("Service %q has no container_id.", in.Name)
	case !states[in.State]:
		return server.Invalid("Service %q has state %q; it must be one of Docker's: created, running, paused, restarting, exited, removing or dead.", in.Name, in.State)
	case in.Health != nil && !healths[*in.Health]:
		return server.Invalid("Service %q has health %q; it must be healthy, unhealthy, starting or null.", in.Name, *in.Health)
	case in.RestartCount < 0:
		return server.Invalid("Service %q has a negative restart_count.", in.Name)
	case !policies[in.RestartPolicy]:
		return server.Invalid("Service %q has restart_policy %q; it must be no, always, unless-stopped or on-failure.", in.Name, in.RestartPolicy)
	case in.MemoryBytes != nil && *in.MemoryBytes < 0, in.MemoryLimitBytes != nil && *in.MemoryLimitBytes <= 0,
		in.CPUPercent != nil && (*in.CPUPercent < 0 || math.IsNaN(*in.CPUPercent) || math.IsInf(*in.CPUPercent, 0)):
		return server.Invalid("Service %q has a negative memory_bytes or cpu_percent, or a memory_limit_bytes that isn't above 0.", in.Name)
	case in.StatsUnavailable && (in.State != StateRunning || in.MemoryBytes != nil || in.MemoryLimitBytes != nil || in.CPUPercent != nil):
		return server.Invalid("Service %q has stats_unavailable, which is only for a running container without usage numbers.", in.Name)
	}
	return nil
}

// Apply stores one report of the machine's daemon.
func (s *Store) Apply(ctx context.Context, machineID int64, r Report) error {
	switch r.Docker {
	case DockerRunning:
		if r.Services == nil {
			return server.Invalid("services is required when docker is running; send [] for none.")
		}
		_, err := s.Replace(ctx, machineID, *r.Services)
		return err
	case DockerUnreachable:
		if r.Services != nil {
			return server.Invalid("Leave services out when docker is unreachable: the hub keeps the last list.")
		}
		return s.Unreachable(ctx, machineID)
	}
	return server.Invalid("docker must be running or unreachable.")
}

// Unreachable records a report that the machine's Docker doesn't answer: the hub keeps the last
// list and sets every state to unknown. The counts of reports in a row start again, so two
// reports can't span the outage; the alert rules keep open alerts as they are meanwhile.
func (s *Store) Unreachable(ctx context.Context, machineID int64) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO service_engines (machine_id, docker, unreachable_reports, created_at, updated_at) VALUES (?, 'unreachable', 1, ?, ?)
		 ON CONFLICT (machine_id) DO UPDATE SET docker = 'unreachable', unreachable_reports = service_engines.unreachable_reports + 1,
		   updated_at = excluded.updated_at`, machineID, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE services SET state = 'unknown', down_reports = 0, restarting_reports = 0, unhealthy_reports = 0, memory_high_reports = 0,
		   memory_bytes = NULL, cpu_percent = NULL, updated_at = ? WHERE machine_id = ?`, now, machineID); err != nil {
		return err
	}
	return tx.Commit()
}

// previous is what the last report said about a service.
type previous struct {
	containerID                            string
	restartCount                           int64
	down, restarting, unhealth, memoryHigh int
	memory, limit                          *int64
}

// memoryHighShare is how much of its memory limit a service may use before it counts as near it.
const memoryHighShare = 0.9

// streak is n+1 when the condition holds in this report, else 0.
func streak(holds bool, n int) int {
	if holds {
		return n + 1
	}
	return 0
}

// Replace makes in the machine's full service list, from a report that Docker is running, as the daemon reports it: it adds new
// services, updates the others and deletes the ones that are gone. A service
// keeps its alert switch. stopped_by_tender clears once the service runs again, or its container
// is made again or started since (a new container_id or started_at).
func (s *Store) Replace(ctx context.Context, machineID int64, in []ServiceInput) ([]Service, error) {
	names := make([]string, 0, len(in))
	seen := map[string]bool{}
	for i := range in {
		if in[i].RestartPolicy == "" { // Docker's own word for no policy
			in[i].RestartPolicy = PolicyNo
		}
	}
	for _, sv := range in {
		if err := sv.validate(); err != nil {
			return nil, err
		}
		if seen[sv.Name] {
			return nil, server.Invalid("Service %q appears twice.", sv.Name)
		}
		seen[sv.Name] = true
		names = append(names, sv.Name)
	}
	keep, err := json.Marshal(names)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	prev := map[string]previous{}
	rows, err := tx.QueryContext(ctx,
		`SELECT name, container_id, restart_count, down_reports, restarting_reports, unhealthy_reports, memory_high_reports,
			   memory_bytes, memory_limit_bytes
			 FROM services WHERE machine_id = ?`, machineID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		var p previous
		if err := rows.Scan(&name, &p.containerID, &p.restartCount, &p.down, &p.restarting, &p.unhealth, &p.memoryHigh, &p.memory, &p.limit); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		prev[name] = p
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO service_engines (machine_id, docker, unreachable_reports, created_at, updated_at) VALUES (?, 'running', 0, ?, ?)
		 ON CONFLICT (machine_id) DO UPDATE SET docker = 'running', unreachable_reports = 0, updated_at = excluded.updated_at`,
		machineID, now, now); err != nil {
		return nil, err
	}
	for _, sv := range in {
		p, known := prev[sv.Name]
		grew := known && p.containerID == sv.ContainerID && sv.RestartCount > p.restartCount
		down := streak(sv.State == StateExited || sv.State == StateDead, p.down)
		restarting := streak(sv.State == StateRestarting || grew, p.restarting)
		unhealthy := streak(sv.Health != nil && *sv.Health == HealthUnhealthy, p.unhealth)
		memoryHigh := streak(sv.MemoryBytes != nil && sv.MemoryLimitBytes != nil &&
			float64(*sv.MemoryBytes) > memoryHighShare*float64(*sv.MemoryLimitBytes), p.memoryHigh)
		if sv.StatsUnavailable { // its usage is unknown this time, not gone: keep the last numbers and the count
			memoryHigh, sv.MemoryBytes, sv.MemoryLimitBytes = 0, nil, nil
			if known && p.containerID == sv.ContainerID {
				memoryHigh, sv.MemoryBytes, sv.MemoryLimitBytes = p.memoryHigh, p.memory, p.limit
			}
		}
		exitCode := sv.ExitCode
		if sv.State != StateExited && sv.State != StateDead {
			exitCode = nil
		}
		var started *int64
		if sv.StartedAt != nil && !sv.StartedAt.IsZero() {
			u := sv.StartedAt.Unix()
			started = &u
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO services (machine_id, name, project, container_id, image, state, health, exit_code, restart_count, restart_policy,
			   started_at, memory_bytes, memory_limit_bytes, cpu_percent, down_reports, restarting_reports, unhealthy_reports,
			   memory_high_reports, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (machine_id, name) DO UPDATE SET project = excluded.project, container_id = excluded.container_id,
			   image = excluded.image, state = excluded.state, health = excluded.health, exit_code = excluded.exit_code,
			   restart_count = excluded.restart_count, restart_policy = excluded.restart_policy, started_at = excluded.started_at,
			   memory_bytes = excluded.memory_bytes, memory_limit_bytes = excluded.memory_limit_bytes, cpu_percent = excluded.cpu_percent,
			   stopped_by_tender = CASE WHEN excluded.state = 'running' OR excluded.container_id != services.container_id
			     OR excluded.started_at IS NOT services.started_at THEN 0 ELSE services.stopped_by_tender END,
			   down_reports = excluded.down_reports, restarting_reports = excluded.restarting_reports,
			   unhealthy_reports = excluded.unhealthy_reports, memory_high_reports = excluded.memory_high_reports, updated_at = excluded.updated_at`,
			machineID, sv.Name, sv.Project, sv.ContainerID, sv.Image, sv.State, sv.Health, exitCode, sv.RestartCount, sv.RestartPolicy, started,
			sv.MemoryBytes, sv.MemoryLimitBytes, sv.CPUPercent, down, restarting, unhealthy, memoryHigh, now, now)
		if err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM services WHERE machine_id = ? AND name NOT IN (SELECT value FROM json_each(?))`, machineID, string(keep)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.List(ctx, machineID)
}

// SetAlerts turns a service's alerts on or off. Off, its open alerts resolve on the next check.
func (s *Store) SetAlerts(ctx context.Context, machineID int64, name string, on bool) (Service, error) {
	res, err := s.db.Write.ExecContext(ctx, `UPDATE services SET alerts_enabled = ?, updated_at = ? WHERE machine_id = ? AND name = ?`,
		on, time.Now().Unix(), machineID, name)
	if err != nil {
		return Service{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Service{}, err
	} else if n == 0 {
		return Service{}, server.NotFound("Service %q doesn't exist on this machine.", name)
	}
	return s.Get(ctx, machineID, name)
}

// Check returns nil when the machine has the service, else a 404. The actions domain calls it
// before it queues an action for a service.
func (s *Store) Check(ctx context.Context, machineID int64, name string) error {
	_, err := s.Get(ctx, machineID, name)
	return err
}

// Finished hears that a start, stop or restart of a service completed. A stop marks the service
// stopped_by_tender, so it doesn't alert as down until it runs again; a start or restart clears
// the mark. A service that is gone meanwhile changes nothing.
func (s *Store) Finished(ctx context.Context, machineID int64, name, typ string) error {
	if typ != actStart && typ != actStop && typ != actRestart {
		return nil
	}
	_, err := s.db.Write.ExecContext(ctx, `UPDATE services SET stopped_by_tender = ?, updated_at = ? WHERE machine_id = ? AND name = ?`,
		typ == actStop, time.Now().Unix(), machineID, name)
	return err
}
