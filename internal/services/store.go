package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services/deploy"
)

// Store reads and writes services, their containers and routes, and the registries.
type Store struct {
	db *db.DB
	// Key opens the managed services' env and the registry passwords; without it they can't be
	// stored or read.
	Key []byte
	// Actions queues the services' actions; without it nothing can be deployed, started or stopped.
	Actions Actions
	// Tunnels keeps the machines' Cloudflare tunnels; without it, cloudflare routes are refused.
	Tunnels Tunnels
	// Log takes what goes wrong for another service than the one asked about; slog's default when nil.
	Log *slog.Logger
}

func (s *Store) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// NewStore returns a store on d.
func NewStore(d *db.DB) *Store { return &Store{db: d} }

var states = map[string]bool{StateCreated: true, StateRunning: true, StatePaused: true, StateRestarting: true,
	StateExited: true, StateRemoving: true, StateDead: true}

var healths = map[string]bool{HealthHealthy: true, HealthUnhealthy: true, HealthStarting: true}

var policies = map[string]bool{PolicyNo: true, PolicyAlways: true, PolicyUnlessStopped: true, PolicyOnFailure: true}

const selectContainers = `
SELECT c.id, c.service_id, c.name, c.runtime, c.container_id, c.image, c.state, c.health, c.exit_code, c.restart_count,
       c.restart_policy, c.started_at, c.memory_bytes, c.memory_limit_bytes, c.cpu_percent, c.stopped_by_tender,
       c.down_reports, c.restarting_reports, c.unhealthy_reports, c.memory_high_reports, c.removed_at, c.created_at, c.updated_at
FROM containers c`

type scanner interface{ Scan(dest ...any) error }

func scanContainer(row scanner) (Container, error) {
	var c Container
	var started, removed *int64
	var created, updated int64
	err := row.Scan(&c.ID, &c.serviceID, &c.Name, &c.Runtime, &c.ContainerID, &c.Image, &c.State, &c.Health, &c.ExitCode,
		&c.RestartCount, &c.RestartPolicy, &started, &c.MemoryBytes, &c.MemoryLimitBytes, &c.CPUPercent, &c.StoppedByTender,
		&c.DownReports, &c.RestartingReports, &c.UnhealthyReports, &c.MemoryHighReports, &removed, &created, &updated)
	for _, t := range []struct {
		unix *int64
		to   **time.Time
	}{{started, &c.StartedAt}, {removed, &c.RemovedAt}} {
		if t.unix != nil {
			at := time.Unix(*t.unix, 0).UTC()
			*t.to = &at
		}
	}
	c.CreatedAt, c.UpdatedAt = at(created), at(updated)
	return c, err
}

func at(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

// finished says a stopped container did its job: it exited 0, and Docker wouldn't restart it.
func finished(c Container) bool {
	return c.State == StateExited && c.ExitCode != nil && *c.ExitCode == 0 &&
		(c.RestartPolicy == PolicyNo || c.RestartPolicy == PolicyOnFailure)
}

// stateOf is a service's state, from its containers.
func stateOf(list []Container) string {
	should, running := 0, 0
	for _, c := range list {
		switch {
		case c.State == StateRemoved:
		case c.State == StateUnknown:
			return ServiceUnknown
		case finished(c):
		default:
			should++
			if c.State == StateRunning {
				running++
			}
		}
	}
	switch {
	case should > 0 && running == should:
		return ServiceRunning
	case running > 0:
		return ServiceDegraded
	}
	return ServiceStopped
}

// List returns the services of one machine, or of every machine when machineID is 0, by machine
// and then by name, each with its routes and containers.
func (s *Store) List(ctx context.Context, machineID int64) ([]Service, error) {
	if err := s.reconcile(ctx, machineID); err != nil {
		return nil, err
	}
	return s.rows(ctx, "?1 = 0 OR s.machine_id = ?1", machineID)
}

// Machines returns the services of one machine, or of every machine that reported any when
// machineID is 0, with what the latest report said about its Docker, by machine name.
func (s *Store) Machines(ctx context.Context, machineID int64) ([]MachineServices, error) {
	list, err := s.List(ctx, machineID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT m.name, e.docker, e.docker_reason, coalesce(e.unreachable_reports, 0), e.updated_at
		FROM machines m LEFT JOIN service_engines e ON e.machine_id = m.id
		WHERE m.id = ? OR (? = 0 AND (e.id IS NOT NULL OR EXISTS (SELECT 1 FROM services s WHERE s.machine_id = m.id)))
		ORDER BY m.name`, machineID, machineID)
	if err != nil {
		return nil, err
	}
	out := []MachineServices{}
	where := map[string]int{}
	for rows.Next() {
		ms := MachineServices{Data: []Service{}}
		var reported *int64
		if err := rows.Scan(&ms.Machine, &ms.Docker, &ms.DockerReason, &ms.unreachableReports, &reported); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if reported != nil {
			t := at(*reported)
			ms.reportedAt = &t
		}
		where[ms.Machine] = len(out)
		out = append(out, ms)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, sv := range list {
		if i, ok := where[sv.Machine]; ok {
			out[i].Data = append(out[i].Data, sv)
		}
	}
	return out, nil
}

func notFound(name string) error {
	return server.NotFound("Service %q doesn't exist on this machine. Run tender services list to see the names.", name)
}

// Get returns one of a machine's services by name.
func (s *Store) Get(ctx context.Context, machineID int64, name string) (Service, error) {
	if err := s.reconcile(ctx, machineID); err != nil {
		return Service{}, err
	}
	return s.row(ctx, machineID, name)
}

// row reads one of the machine's services as stored, without reconciling it.
func (s *Store) row(ctx context.Context, machineID int64, name string) (Service, error) {
	list, err := s.rows(ctx, "s.machine_id = ? AND s.name = ?", machineID, name)
	if err != nil {
		return Service{}, err
	}
	if len(list) == 0 {
		return Service{}, notFound(name)
	}
	return list[0], nil
}

const selectServices = `SELECT s.id, s.machine_id, m.name, s.name, s.managed, s.compose, s.files, s.env, s.deploy_status, s.last_error,
  s.deployed_at, s.action_id, s.action_type, s.alerts_enabled, s.created_at, s.updated_at
FROM services s JOIN machines m ON m.id = s.machine_id`

func scanService(r scanner) (Service, error) {
	var sv Service
	var files, env string
	var deployed *int64
	var actionType *string
	var created, updated int64
	err := r.Scan(&sv.ID, &sv.machineID, &sv.Machine, &sv.Name, &sv.Managed, &sv.Compose, &files, &env, &sv.DeployStatus, &sv.LastError,
		&deployed, &sv.ActionID, &actionType, &sv.AlertsEnabled, &created, &updated)
	if err != nil {
		return sv, err
	}
	if err := json.Unmarshal([]byte(files), &sv.Files); err != nil {
		return sv, fmt.Errorf("service %s: files are damaged: %w", sv.Name, err)
	}
	if err := json.Unmarshal([]byte(env), &sv.sealedEnv); err != nil {
		return sv, fmt.Errorf("service %s: env is damaged: %w", sv.Name, err)
	}
	sv.Env = map[string]string{}
	for k := range sv.sealedEnv {
		sv.Env[k] = deploy.Hidden
	}
	if sv.Files == nil {
		sv.Files = map[string]string{}
	}
	if deployed != nil {
		t := at(*deployed)
		sv.DeployedAt = &t
	}
	if actionType != nil {
		sv.actionType = *actionType
	}
	sv.CreatedAt, sv.UpdatedAt = at(created), at(updated)
	sv.Routes, sv.Containers = []Route{}, []Container{}
	return sv, nil
}

// rows reads the services that where and args select, by machine and name, with their routes and
// containers.
func (s *Store) rows(ctx context.Context, where string, args ...any) ([]Service, error) {
	rs, err := s.db.Read.QueryContext(ctx, selectServices+" WHERE "+where+" ORDER BY m.name, s.name", args...)
	if err != nil {
		return nil, err
	}
	out := []Service{}
	for rs.Next() {
		sv, err := scanService(rs)
		if err != nil {
			return nil, errors.Join(err, rs.Close())
		}
		out = append(out, sv)
	}
	if err := errors.Join(rs.Err(), rs.Close()); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]int64, len(out))
	index := map[int64]int{}
	for i, sv := range out {
		ids[i], index[sv.ID] = sv.ID, i
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	routes, err := s.routes(ctx, `r.service_id IN (SELECT value FROM json_each(?))`, string(list))
	if err != nil {
		return nil, err
	}
	names, err := s.tailnetNames(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		sv := &out[index[r.serviceID]]
		r.URL = routeURL(r.Route, names[sv.machineID])
		sv.Routes = append(sv.Routes, r.Route)
	}
	cs, err := s.db.Read.QueryContext(ctx, selectContainers+` WHERE c.service_id IN (SELECT value FROM json_each(?)) ORDER BY c.service_id, c.name`, string(list))
	if err != nil {
		return nil, err
	}
	for cs.Next() {
		c, err := scanContainer(cs)
		if err != nil {
			return nil, errors.Join(err, cs.Close())
		}
		sv := &out[index[c.serviceID]]
		sv.Containers = append(sv.Containers, c)
	}
	if err := errors.Join(cs.Err(), cs.Close()); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].State = stateOf(out[i].Containers)
	}
	return out, nil
}

func (in ContainerInput) validate() error {
	switch {
	case in.Name == "" || len(in.Name) > 255:
		return server.Invalid("Every container needs a name of 1 to 255 characters.")
	case in.ContainerID == "":
		return server.Invalid("Container %q has no container_id.", in.Name)
	case !runtimes[in.Runtime]:
		return server.Invalid("Container %q has runtime %q; this hub knows only docker. Update the hub to the daemon's version.", in.Name, in.Runtime)
	case !states[in.State]:
		return server.Invalid("Container %q has state %q; it must be one of Docker's: created, running, paused, restarting, exited, removing or dead.", in.Name, in.State)
	case in.Health != nil && !healths[*in.Health]:
		return server.Invalid("Container %q has health %q; it must be healthy, unhealthy, starting or null.", in.Name, *in.Health)
	case in.RestartCount < 0:
		return server.Invalid("Container %q has a negative restart_count.", in.Name)
	case !policies[in.RestartPolicy]:
		return server.Invalid("Container %q has restart_policy %q; it must be no, always, unless-stopped or on-failure.", in.Name, in.RestartPolicy)
	case in.MemoryBytes != nil && *in.MemoryBytes < 0, in.MemoryLimitBytes != nil && *in.MemoryLimitBytes <= 0,
		in.CPUPercent != nil && (*in.CPUPercent < 0 || math.IsNaN(*in.CPUPercent) || math.IsInf(*in.CPUPercent, 0)):
		return server.Invalid("Container %q has a negative memory_bytes or cpu_percent, or a memory_limit_bytes that isn't above 0.", in.Name)
	case in.StatsUnavailable && (in.State != StateRunning || in.MemoryBytes != nil || in.MemoryLimitBytes != nil || in.CPUPercent != nil):
		return server.Invalid("Container %q has stats_unavailable, which is only for a running container without usage numbers.", in.Name)
	}
	return nil
}

// Apply stores one report of the machine's daemon.
func (s *Store) Apply(ctx context.Context, machineID int64, r Report) error {
	list := r.Containers
	if list == nil {
		list = r.Services // a daemon before v0.4
	} else if r.Services != nil {
		return server.Invalid("Send the list as containers or, from a daemon before v0.4, as services, not both.")
	}
	switch r.Docker {
	case DockerRunning:
		if list == nil {
			return server.Invalid("containers is required when docker is running; send [] for none.")
		}
		if r.DockerReason != "" {
			return server.Invalid("docker_reason is only for docker unreachable.")
		}
		return s.Replace(ctx, machineID, *list)
	case DockerUnreachable:
		if list != nil {
			return server.Invalid("Leave containers out when docker is unreachable: the hub keeps the last list.")
		}
		if !reasons[r.DockerReason] {
			return server.Invalid("docker_reason must be socket_missing, permission_denied, refused, timeout or error when docker is unreachable.")
		}
		return s.Unreachable(ctx, machineID, r.DockerReason)
	case DockerOff:
		if list != nil || r.DockerReason != "" {
			return server.Invalid("Leave containers and docker_reason out when docker is off: the hub forgets the machine's Docker.")
		}
		return s.Off(ctx, machineID)
	}
	return server.Invalid("docker must be running, unreachable or off.")
}

// Off records a report that Docker is turned off on the machine, and forgets its Docker as for a
// machine without it: the engine's row goes, so GET shows docker null and the rule for a daemon
// that stops reporting Docker no longer applies, and every container is removed, as a container a
// report lacks is, so its problems resolve. Another off report changes nothing.
func (s *Store) Off(ctx context.Context, machineID int64) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_engines WHERE machine_id = ?`, machineID); err != nil {
		return err
	}
	if err := removeGone(ctx, tx, machineID, "[]", time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// Unreachable records a report that the machine's Docker doesn't answer: the hub keeps the last
// list and sets every state to unknown, but a removed one's. The counts of reports in a row start
// again, so two reports can't span the outage; the notification rules keep open problems as they
// are meanwhile.
func (s *Store) Unreachable(ctx context.Context, machineID int64, reason string) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO service_engines (machine_id, docker, docker_reason, unreachable_reports, created_at, updated_at) VALUES (?, 'unreachable', ?, 1, ?, ?)
		 ON CONFLICT (machine_id) DO UPDATE SET docker = 'unreachable', docker_reason = excluded.docker_reason,
		   unreachable_reports = service_engines.unreachable_reports + 1, updated_at = excluded.updated_at`, machineID, reason, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE containers SET state = 'unknown', exit_code = NULL, down_reports = 0, restarting_reports = 0,
		   unhealthy_reports = 0, memory_high_reports = 0, memory_bytes = NULL, cpu_percent = NULL, updated_at = ?
		 WHERE machine_id = ? AND state != 'removed'`, now, machineID); err != nil {
		return err
	}
	return tx.Commit()
}

// previous is what the last report said about a container.
type previous struct {
	containerID                            string
	restartCount                           int64
	down, restarting, unhealth, memoryHigh int
	memory, limit                          *int64
}

// removedFor is how long the hub keeps a removed container, for a container back under its name.
const removedFor = 7 * 24 * time.Hour

// memoryHighShare is how much of its memory limit a container may use before it counts as near it.
const memoryHighShare = 0.9

// streak is n+1 when the condition holds in this report, else 0.
func streak(holds bool, n int) int {
	if holds {
		return n + 1
	}
	return 0
}

// Replace makes in the machine's full container list, from a report that Docker is running. It
// files each container under its service (see Name), making the services it doesn't have yet as
// unmanaged ones, adds new containers, updates the others, marks the ones that are gone removed
// and deletes those removed for 7 days, with the unmanaged services left without containers.
// stopped_by_tender clears once the container runs again, or is made again or started since (a
// new container_id or started_at).
func (s *Store) Replace(ctx context.Context, machineID int64, in []ContainerInput) error {
	for i := range in {
		if in[i].RestartPolicy == "" { // Docker's own word for no policy
			in[i].RestartPolicy = PolicyNo
		}
		if in[i].Runtime == "" { // a daemon before v0.3
			in[i].Runtime = RuntimeDocker
		}
	}
	seen := map[string]bool{}
	for _, c := range in {
		if err := c.validate(); err != nil {
			return err
		}
		if seen[c.Name] {
			return server.Invalid("Container %q appears twice.", c.Name)
		}
		seen[c.Name] = true
	}
	named := Name(in)

	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	prev := map[Named]previous{}
	rows, err := tx.QueryContext(ctx,
		`SELECT s.name, c.name, c.container_id, c.restart_count, c.down_reports, c.restarting_reports, c.unhealthy_reports, c.memory_high_reports,
			   c.memory_bytes, c.memory_limit_bytes
			 FROM containers c JOIN services s ON s.id = c.service_id WHERE c.machine_id = ?`, machineID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n Named
		var p previous
		if err := rows.Scan(&n.Service, &n.Container, &p.containerID, &p.restartCount, &p.down, &p.restarting, &p.unhealth, &p.memoryHigh,
			&p.memory, &p.limit); err != nil {
			return errors.Join(err, rows.Close())
		}
		prev[n] = p
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO service_engines (machine_id, docker, unreachable_reports, created_at, updated_at) VALUES (?, 'running', 0, ?, ?)
		 ON CONFLICT (machine_id) DO UPDATE SET docker = 'running', docker_reason = NULL, unreachable_reports = 0, updated_at = excluded.updated_at`,
		machineID, now, now); err != nil {
		return err
	}
	serviceIDs := map[string]int64{}
	keep := []int64{}
	for i, c := range in {
		n := named[i]
		sid, ok := serviceIDs[n.Service]
		if !ok {
			if err := tx.QueryRowContext(ctx, `INSERT INTO services (machine_id, name, created_at, updated_at) VALUES (?, ?, ?, ?)
				ON CONFLICT (machine_id, name) DO UPDATE SET name = excluded.name RETURNING id`, machineID, n.Service, now, now).Scan(&sid); err != nil {
				return err
			}
			serviceIDs[n.Service] = sid
		}
		p, known := prev[n]
		grew := known && p.containerID == c.ContainerID && c.RestartCount > p.restartCount
		down := streak(c.State == StateExited || c.State == StateDead, p.down)
		restarting := streak(c.State == StateRestarting || grew, p.restarting)
		unhealthy := streak(c.Health != nil && *c.Health == HealthUnhealthy, p.unhealth)
		memoryHigh := streak(c.MemoryBytes != nil && c.MemoryLimitBytes != nil &&
			float64(*c.MemoryBytes) > memoryHighShare*float64(*c.MemoryLimitBytes), p.memoryHigh)
		if c.StatsUnavailable { // its usage is unknown this time, not gone: keep the last numbers and the count
			memoryHigh, c.MemoryBytes, c.MemoryLimitBytes = 0, nil, nil
			if known && p.containerID == c.ContainerID {
				memoryHigh, c.MemoryBytes, c.MemoryLimitBytes = p.memoryHigh, p.memory, p.limit
			}
		}
		exitCode := c.ExitCode
		if c.State != StateExited && c.State != StateDead {
			exitCode = nil
		}
		var started *int64
		if c.StartedAt != nil && !c.StartedAt.IsZero() {
			u := c.StartedAt.Unix()
			started = &u
		}
		var id int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO containers (machine_id, service_id, name, runtime, container_id, image, state, health, exit_code, restart_count, restart_policy,
			   started_at, memory_bytes, memory_limit_bytes, cpu_percent, down_reports, restarting_reports, unhealthy_reports,
			   memory_high_reports, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (service_id, name) DO UPDATE SET runtime = excluded.runtime, container_id = excluded.container_id,
			   image = excluded.image, state = excluded.state, health = excluded.health, exit_code = excluded.exit_code,
			   restart_count = excluded.restart_count, restart_policy = excluded.restart_policy, started_at = excluded.started_at,
			   memory_bytes = excluded.memory_bytes, memory_limit_bytes = excluded.memory_limit_bytes, cpu_percent = excluded.cpu_percent,
			   stopped_by_tender = CASE WHEN excluded.state = 'running' OR excluded.container_id != containers.container_id
			     OR excluded.started_at IS NOT containers.started_at THEN 0 ELSE containers.stopped_by_tender END,
			   down_reports = excluded.down_reports, restarting_reports = excluded.restarting_reports,
			   unhealthy_reports = excluded.unhealthy_reports, memory_high_reports = excluded.memory_high_reports, removed_at = NULL,
			   updated_at = excluded.updated_at
			 RETURNING id`,
			machineID, sid, n.Container, c.Runtime, c.ContainerID, c.Image, c.State, c.Health, exitCode, c.RestartCount, c.RestartPolicy, started,
			c.MemoryBytes, c.MemoryLimitBytes, c.CPUPercent, down, restarting, unhealthy, memoryHigh, now, now).Scan(&id)
		if err != nil {
			return err
		}
		keep = append(keep, id)
	}
	ids, err := json.Marshal(keep)
	if err != nil {
		return err
	}
	if err := removeGone(ctx, tx, machineID, string(ids), now); err != nil {
		return err
	}
	return tx.Commit()
}

// removeGone marks removed each of the machine's containers whose id isn't in keep, a JSON array:
// its row stays for a container back under the name, and its counts start again, so its alerts
// resolve. It deletes the containers removed 7 days ago, and then the unmanaged services that have
// no container left.
func removeGone(ctx context.Context, tx *sql.Tx, machineID int64, keep string, now int64) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE containers SET state = 'removed', removed_at = ?3, health = NULL, exit_code = NULL, started_at = NULL, memory_bytes = NULL,
		   memory_limit_bytes = NULL, cpu_percent = NULL, stopped_by_tender = 0, down_reports = 0, restarting_reports = 0, unhealthy_reports = 0,
		   memory_high_reports = 0, updated_at = ?3
		 WHERE machine_id = ?1 AND state != 'removed' AND id NOT IN (SELECT value FROM json_each(?2))`, machineID, keep, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM containers WHERE machine_id = ? AND state = 'removed' AND removed_at < ?`,
		machineID, now-int64(removedFor/time.Second)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM services WHERE machine_id = ? AND managed = 0
		AND NOT EXISTS (SELECT 1 FROM containers c WHERE c.service_id = services.id)`, machineID)
	return err
}

// SetAlerts turns a service's alerts on or off, for all its containers. Off, its open problems
// resolve on the next check.
func (s *Store) SetAlerts(ctx context.Context, machineID int64, name string, on bool) (Service, error) {
	res, err := s.db.Write.ExecContext(ctx, `UPDATE services SET alerts_enabled = ?, updated_at = ? WHERE machine_id = ? AND name = ?`,
		on, time.Now().Unix(), machineID, name)
	if err != nil {
		return Service{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Service{}, err
	} else if n == 0 {
		return Service{}, notFound(name)
	}
	return s.Get(ctx, machineID, name)
}

// Check returns nil when the machine has the service, and container in it when container isn't
// empty, else a 404. The actions domain calls it before it queues a start, stop, restart or logs.
// A container that was removed, or a service with no container left, is a 409: Docker has nothing
// to act on.
func (s *Store) Check(ctx context.Context, machineID int64, service, container string) error {
	sv, err := s.row(ctx, machineID, service)
	if err != nil {
		return err
	}
	if container != "" {
		i := slices.IndexFunc(sv.Containers, func(c Container) bool { return c.Name == container })
		switch {
		case i < 0:
			names := []string{}
			for _, c := range sv.Containers {
				names = append(names, c.Name)
			}
			return server.NotFound("Container %q isn't in service %s. Its containers are: %s.", container, service, strings.Join(names, ", "))
		case sv.Containers[i].State == StateRemoved:
			return server.Conflict("Container %s in %s was removed: Docker on this machine no longer has it.", container, service)
		}
		return nil
	}
	if !slices.ContainsFunc(sv.Containers, func(c Container) bool { return c.State != StateRemoved }) {
		if sv.Managed {
			return server.Conflict("Service %q has no containers on this machine now. Deploy it again to make them: tender services redeploy.", service)
		}
		return server.Conflict("Service %q was removed: Docker on this machine no longer has its containers.", service)
	}
	return nil
}

// Finished hears that a start, stop or restart of a service, or of one container in it, completed.
// A stop marks its containers stopped_by_tender, so they don't alert as down until they run again;
// a start or restart clears the mark. A service that is gone meanwhile changes nothing.
func (s *Store) Finished(ctx context.Context, machineID int64, service, container, typ string) error {
	if typ != actStart && typ != actStop && typ != actRestart {
		return nil
	}
	_, err := s.db.Write.ExecContext(ctx, `UPDATE containers SET stopped_by_tender = ?1, updated_at = ?2
		WHERE service_id = (SELECT id FROM services WHERE machine_id = ?3 AND name = ?4) AND (?5 = '' OR name = ?5) AND state != 'removed'`,
		typ == actStop, time.Now().Unix(), machineID, service, container)
	return err
}
