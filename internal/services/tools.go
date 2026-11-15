package services

import (
	"context"
	"errors"
	"net/http"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

type listInput struct {
	Machine string `json:"machine,omitempty" jsonschema:"only this machine's services; leave out for every machine"`
}

type alertsInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Service string `json:"service" jsonschema:"service name, the container name, as list_services shows it"`
	Enabled bool   `json:"enabled" jsonschema:"true to get alerts for this service, false to stop them and resolve its open alerts"`
}

// RegisterTools adds the services tools to s. They use the same store methods as the REST routes.
// lookup resolves machine names to ids.
func (s *Store) RegisterTools(m *mcp.Server, lookup mcp.MachineLookup) {
	mcp.AddReadOnly(m, "list_services", "List services",
		"List the Docker containers each machine's daemon reports, once a minute, by machine, or one machine's. Per machine: docker "+
			"(running, unreachable when its Docker engine doesn't answer, or null before any report) and data, its services. Each service is "+
			"one container, by its container name: project (the compose project, or null), image, state (created, running, paused, restarting, "+
			"exited, removing, dead, or unknown while Docker is unreachable), health (healthy, unhealthy, starting, or null without a health "+
			"check), exit_code (while exited or dead), restart_count, restart_policy (no, always, unless-stopped or on-failure), started_at, "+
			"memory_bytes (less the page cache it can drop), memory_limit_bytes (null without a limit) and cpu_percent (100 is one whole "+
			"CPU; null on its first report), from the latest report and null while stopped, "+
			"alerts_enabled, stopped_by_tender (a Tender stop left it stopped, so it doesn't alert as down) and updated_at (RFC 3339 UTC). "+
			"A machine without Docker has none.",
		func(ctx context.Context, in listInput) (server.List[MachineServices], error) {
			var id int64
			if in.Machine != "" {
				var err error
				if id, err = mcp.Machine(ctx, lookup, in.Machine); err != nil {
					return server.List[MachineServices]{}, err
				}
			}
			list, err := s.Machines(ctx, id)
			return server.List[MachineServices]{Data: list}, err
		})

	mcp.AddWrite(m, "set_service_alerts", "Turn a service's alerts on or off",
		"Turn alerts on or off for one service. Alerts are on for every new service: an email when it stops, keeps restarting or "+
			"turns unhealthy or above 90% of its memory limit. Off, its open alerts resolve within a minute. The switch follows the service's name, so it stays when "+
			"the container is made again. The result is the service, as list_services shows it.",
		mcp.WriteOptions{Idempotent: true},
		func(ctx context.Context, in alertsInput) (Service, error) {
			id, err := s.toolService(ctx, lookup, in.Machine, in.Service)
			if err != nil {
				return Service{}, err
			}
			return s.SetAlerts(ctx, id, in.Service, in.Enabled)
		})
}

// toolService resolves a tool's machine and checks its service exists, with errors that say
// how to find the right names.
func (s *Store) toolService(ctx context.Context, lookup mcp.MachineLookup, machine, service string) (int64, error) {
	id, err := mcp.Machine(ctx, lookup, machine)
	if err != nil {
		return 0, err
	}
	if service == "" {
		return 0, server.Invalid("service is required. Call list_services to see the names.")
	}
	if _, err := s.Get(ctx, id, service); err != nil {
		var e *server.Error
		if errors.As(err, &e) && e.Status == http.StatusNotFound {
			return 0, server.NotFound("Service %q doesn't exist on %s. Call list_services to see the names.", service, machine)
		}
		return 0, err
	}
	return id, nil
}
