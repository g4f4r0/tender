package actions

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

// Wait is how long the CLI and the MCP tools wait for the daemon to finish an action. Tests shorten it.
var Wait = 60 * time.Second

type serviceInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Service string `json:"service" jsonschema:"service name, the container name, as list_services shows it"`
}

type serviceLogsInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Service string `json:"service" jsonschema:"service name, the container name, as list_services shows it"`
	Lines   *int   `json:"lines,omitempty" jsonschema:"how many of the newest lines, 1 to 2000, default 200"`
	Since   string `json:"since,omitempty" jsonschema:"only lines from this far back, such as 30m, 1h or 7d, at most 7d; leave out for no limit"`
}

type machineLogsInput struct {
	Machine  string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Unit     string `json:"unit,omitempty" jsonschema:"only this systemd unit, such as tenderd or nginx.service; leave out for every unit"`
	Priority string `json:"priority,omitempty" jsonschema:"only this priority and the more severe ones: emerg, alert, crit, err, warning, notice, info or debug"`
	Lines    *int   `json:"lines,omitempty" jsonschema:"how many of the newest entries, 1 to 2000, default 200"`
	Since    string `json:"since,omitempty" jsonschema:"only entries from this far back, such as 30m, 1h or 7d, at most 7d; leave out for no limit"`
}

type actionInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	ID      int64  `json:"id" jsonschema:"the action's id, from the tool that queued it or list_actions"`
}

type listInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
}

const waitText = "It waits up to 60 seconds for the machine's daemon. If the daemon hasn't finished by then, the result has status " +
	"pending or in_progress: the action stays queued for 15 minutes and runs when the daemon picks it up; check it later with get_action."

const actionFields = "The result is the action: id, machine, type (start, stop, restart or logs), service (null for the machine's logs), " +
	"unit, priority, since and lines (logs only), status (pending, in_progress, completed or failed), output, output_cleared, " +
	"created_at and updated_at (RFC 3339 UTC). "

const logsLimits = "At most 2000 lines and 256 KiB, the newest kept, in output. Logs can hold secrets: the hub clears a logs action's " +
	"output 1 hour after it finished (output becomes null and output_cleared true), and logs never go into alerts or emails. "

// RegisterTools adds the actions tools to m. They use the same store methods as the REST routes.
// lookup resolves machine names to ids.
func (s *Store) RegisterTools(m *mcp.Server, lookup mcp.MachineLookup) {
	act := func(typ string) func(context.Context, serviceInput) (Action, error) {
		return func(ctx context.Context, in serviceInput) (Action, error) {
			return s.toolAction(ctx, lookup, in.Machine, Input{Type: typ, Service: &in.Service})
		}
	}
	mcp.AddWrite(m, "start_service", "Start a service",
		"Start a stopped service, through the daemon on its machine. Starting a running service changes nothing. "+actionFields+
			"A failed start is the tool's error, with Docker's reason. "+waitText,
		mcp.WriteOptions{Idempotent: true}, act(TypeStart))
	mcp.AddWrite(m, "stop_service", "Stop a service",
		"Stop a running service, through the daemon on its machine, with the container's own stop timeout. Whatever it serves goes "+
			"offline until it is started again. A service Tender stopped doesn't alert as down. "+actionFields+
			"A failed stop is the tool's error, with Docker's reason. "+waitText,
		mcp.WriteOptions{Destructive: true, Idempotent: true}, act(TypeStop))
	mcp.AddWrite(m, "restart_service", "Restart a service",
		"Restart a service, through the daemon on its machine. Whatever it serves is offline while it restarts. "+actionFields+
			"A failed restart is the tool's error, with Docker's reason. "+waitText,
		mcp.WriteOptions{Destructive: true}, act(TypeRestart))

	mcp.AddReadOnly(m, "get_service_logs", "Get service logs",
		"Get a service's newest lines of output, stdout and stderr together, each line starting with Docker's timestamp. "+logsLimits+
			actionFields+waitText,
		func(ctx context.Context, in serviceLogsInput) (Action, error) {
			return s.toolAction(ctx, lookup, in.Machine, Input{Type: TypeLogs, Service: &in.Service, Lines: in.Lines, Since: opt(in.Since)})
		})
	mcp.AddReadOnly(m, "get_machine_logs", "Get machine logs",
		"Get the newest entries of a machine's systemd journal, read by the daemon with journalctl, one per line: the time in UTC, "+
			"the unit (or the program) and the message. Filter by unit, priority and since. "+logsLimits+actionFields+waitText,
		func(ctx context.Context, in machineLogsInput) (Action, error) {
			return s.toolAction(ctx, lookup, in.Machine, Input{Type: TypeLogs, Unit: opt(in.Unit), Priority: opt(in.Priority),
				Lines: in.Lines, Since: opt(in.Since)})
		})

	mcp.AddReadOnly(m, "get_action", "Get an action",
		"Get one action on a machine by id, with its output: to check one that was still pending or in_progress when a tool stopped "+
			"waiting. "+actionFields+"A logs action's output is cleared 1 hour after it finished.",
		func(ctx context.Context, in actionInput) (Action, error) {
			id, err := mcp.Machine(ctx, lookup, in.Machine)
			if err != nil {
				return Action{}, err
			}
			return s.Get(ctx, id, in.ID)
		})
	mcp.AddReadOnly(m, "list_actions", "List actions",
		"List a machine's last 100 actions, newest first, without their output (get_action has it): id, machine, type, service, unit, "+
			"priority, since, lines, status, output_cleared, created_at and updated_at.",
		func(ctx context.Context, in listInput) (server.List[Summary], error) {
			id, err := mcp.Machine(ctx, lookup, in.Machine)
			if err != nil {
				return server.List[Summary]{}, err
			}
			list, err := s.List(ctx, id)
			return server.List[Summary]{Data: list}, err
		})
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// toolAction queues an action and waits for it. A failed action is the tool's error. A missing
// service says how to find the names.
func (s *Store) toolAction(ctx context.Context, lookup mcp.MachineLookup, machine string, in Input) (Action, error) {
	id, err := mcp.Machine(ctx, lookup, machine)
	if err != nil {
		return Action{}, err
	}
	if in.Service != nil && *in.Service == "" {
		return Action{}, server.Invalid("service is required. Call list_services to see the names.")
	}
	a, err := s.Create(ctx, id, in)
	var e *server.Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound && in.Service != nil {
		return a, server.NotFound("Service %q doesn't exist on %s. Call list_services to see the names.", *in.Service, machine)
	}
	if err != nil {
		return a, err
	}
	if a, err = s.Await(ctx, id, a.ID, Wait); err != nil {
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
