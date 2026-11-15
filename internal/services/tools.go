package services

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

// ActionWait is how long the CLI and the MCP tools wait for the daemon to finish an action. Tests shorten it.
var ActionWait = 60 * time.Second

type listInput struct {
	Machine string `json:"machine,omitempty" jsonschema:"only this machine's services; leave out for every machine"`
}

type serviceInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Service string `json:"service" jsonschema:"service name, the container name, as list_services shows it"`
}

type logsInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Service string `json:"service" jsonschema:"service name, the container name, as list_services shows it"`
	Lines   int    `json:"lines,omitempty" jsonschema:"how many of the last lines, 1 to 2000, default 200"`
}

type alertsInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Service string `json:"service" jsonschema:"service name, the container name, as list_services shows it"`
	Enabled bool   `json:"enabled" jsonschema:"true to get alerts for this service, false to stop them and resolve its open alerts"`
}

const waitText = "It waits up to 60 seconds for the machine's daemon. If the daemon hasn't finished by then, the result has status " +
	"pending or in_progress: the action stays queued for 15 minutes and runs when the daemon picks it up."

// RegisterTools adds the services tools to s. They use the same store methods as the REST routes.
// lookup resolves machine names to ids.
func (s *Store) RegisterTools(m *mcp.Server, lookup mcp.MachineLookup) {
	mcp.AddReadOnly(m, "list_services", "List services",
		"List the Docker containers each machine's daemon reports, once a minute, by machine, or one machine's. Per machine: docker "+
			"(running, unreachable when its Docker engine doesn't answer, or null before any report) and data, its services. Each service is "+
			"one container, by its container name: project (the compose project, or null), image, state (created, running, paused, restarting, "+
			"exited, removing, dead, or unknown while Docker is unreachable), health (healthy, unhealthy, starting, or null without a health "+
			"check), exit_code (while exited or dead), restart_count, restart_policy (no, always, unless-stopped or on-failure), started_at, "+
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

	act := func(typ string) func(context.Context, serviceInput) (Action, error) {
		return func(ctx context.Context, in serviceInput) (Action, error) {
			id, err := s.toolService(ctx, lookup, in.Machine, in.Service)
			if err != nil {
				return Action{}, err
			}
			return s.toolAction(ctx, id, in.Service, ActionInput{Type: typ})
		}
	}
	mcp.AddWrite(m, "start_service", "Start a service",
		"Start a stopped service, through the daemon on its machine. Starting a running service changes nothing. "+waitText,
		mcp.WriteOptions{Idempotent: true}, act(TypeStart))
	mcp.AddWrite(m, "stop_service", "Stop a service",
		"Stop a running service, through the daemon on its machine, with the container's own stop timeout. Whatever it serves goes "+
			"offline until it is started again. A service Tender stopped doesn't alert as down. "+waitText,
		mcp.WriteOptions{Destructive: true, Idempotent: true}, act(TypeStop))
	mcp.AddWrite(m, "restart_service", "Restart a service",
		"Restart a service, through the daemon on its machine. Whatever it serves is offline while it restarts. "+waitText,
		mcp.WriteOptions{Destructive: true}, act(TypeRestart))

	mcp.AddReadOnly(m, "get_service_logs", "Get service logs",
		"Get a service's last lines of output, stdout and stderr together, each line starting with Docker's timestamp: at most "+
			"2000 lines and 256 KiB, the newest kept. The logs are in output. "+waitText,
		func(ctx context.Context, in logsInput) (Action, error) {
			id, err := s.toolService(ctx, lookup, in.Machine, in.Service)
			if err != nil {
				return Action{}, err
			}
			in.Lines = max(in.Lines, 0)
			var lines *int
			if in.Lines != 0 {
				lines = &in.Lines
			}
			return s.toolAction(ctx, id, in.Service, ActionInput{Type: TypeLogs, Lines: lines})
		})

	mcp.AddWrite(m, "set_service_alerts", "Turn a service's alerts on or off",
		"Turn alerts on or off for one service. Alerts are on for every new service: an email when it stops, keeps restarting or "+
			"turns unhealthy. Off, its open alerts resolve within a minute. The switch follows the service's name, so it stays when "+
			"the container is made again.",
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

// toolAction queues an action and waits for it. A failed action is the tool's error.
func (s *Store) toolAction(ctx context.Context, machineID int64, service string, in ActionInput) (Action, error) {
	a, err := s.CreateAction(ctx, machineID, service, in)
	if err != nil {
		return a, err
	}
	a, err = s.Await(ctx, machineID, a.ID, ActionWait)
	if err != nil {
		return a, err
	}
	if a.Status == StatusFailed {
		detail := "The action failed."
		if a.Output != nil {
			detail = *a.Output
		}
		return a, &server.Error{Status: http.StatusBadGateway, Detail: detail}
	}
	return a, nil
}
