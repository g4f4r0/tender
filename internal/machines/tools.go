package machines

import (
	"context"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

type noInput struct{}

type usageInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Since   string `json:"since,omitempty" jsonschema:"only readings at or after this RFC 3339 time, such as 2026-11-01T00:00:00Z"`
	Until   string `json:"until,omitempty" jsonschema:"only readings at or before this RFC 3339 time"`
	Limit   int    `json:"limit,omitempty" jsonschema:"most readings to return, 1 to 500, default 50"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page"`
}

type machineInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
}

type updateMachineInput struct {
	Machine              string    `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Updates              *string   `json:"updates,omitempty" jsonschema:"inherit (follow the hub's update mode), auto or manual; leave out to keep it"`
	SecurityAllowedPorts *[]string `json:"security_allowed_ports,omitempty" jsonschema:"the whole list of ports that may listen on a public address without a port_exposed alert, such as [\"41641/udp\"]; it replaces the list; leave out to keep it"`
	SecurityAlerts       *bool     `json:"security_alerts,omitempty" jsonschema:"turn the machine's security alerts on or off; leave out to keep it"`
}

type addMachineInput struct {
	Name string `json:"name" jsonschema:"new machine name: lowercase letters, digits and hyphens, up to 63 characters"`
}

// RegisterTools adds the machines tools to s. They use the same store methods as the REST routes.
func (s *Store) RegisterTools(m *mcp.Server) {
	mcp.AddReadOnly(m, "list_machines", "List machines",
		"List every machine that reports to this hub, by name. Each has id, name, version (the Tender version its daemon last reported, "+
			"or null), updates (inherit, auto or manual), security_allowed_ports, security_alerts, last_seen (when its latest usage reading was taken, RFC 3339 UTC, "+
			"null before the first) and usage, its latest reading: cpu_load (1-minute load average), memory_used_percent and "+
			"disk_used_percent (0 to 100) and uptime_seconds, then created_at and updated_at. Start here to learn the machine names the other tools take.",
		func(ctx context.Context, _ noInput) (server.List[Overview], error) {
			machines, err := s.List(ctx)
			return server.List[Overview]{Data: machines}, err
		})

	mcp.AddReadOnly(m, "get_machine_usage", "Get machine usage",
		"Get one machine's usage readings, newest first, optionally between since and until. Each reading has id, cpu_load (1-minute "+
			"load average), memory_used_percent and disk_used_percent (0 to 100), uptime_seconds, created_at (when it was taken, "+
			"RFC 3339 UTC) and updated_at. At most limit a page, 1 to 500, default 50; pass next_cursor as cursor for older readings.",
		func(ctx context.Context, in usageInput) (server.List[Usage], error) {
			id, err := mcp.Machine(ctx, s.Lookup, in.Machine)
			if err != nil {
				return server.List[Usage]{}, err
			}
			since, err := parseTime("since", in.Since)
			if err != nil {
				return server.List[Usage]{}, err
			}
			until, err := parseTime("until", in.Until)
			if err != nil {
				return server.List[Usage]{}, err
			}
			page, err := server.NewPage(in.Limit, in.Cursor)
			if err != nil {
				return server.List[Usage]{}, err
			}
			usage, err := s.ListUsage(ctx, id, since, until, page)
			if err != nil {
				return server.List[Usage]{}, err
			}
			return server.NewList(usage, page, usageCursor), nil
		})

	mcp.AddWrite(m, "add_machine", "Add a machine",
		"Add a machine to the hub. The result has its id, name, version (null until its daemon reports), updates (inherit), "+
			"security_allowed_ports (empty), security_alerts (true), created_at, updated_at and token, the token shown only this once: "+
			"it is not stored and can't be read again. "+
			"Give it to the owner to install on that machine with \"tender install daemon\"; if it is lost, call rotate_machine_token.",
		mcp.WriteOptions{},
		func(ctx context.Context, in addMachineInput) (MachineCreated, error) {
			token, err := s.Create(ctx, in.Name)
			if err != nil {
				return MachineCreated{}, err
			}
			o, err := s.Get(ctx, in.Name)
			return MachineCreated{Machine: o.Machine, Token: token}, err
		})

	mcp.AddWrite(m, "update_machine", "Update a machine's settings",
		"Change a machine's settings: updates (inherit, auto or manual: whether Tender updates itself on it with the hub's mode, always "+
			"automatically, or only when asked), security_allowed_ports (the whole list of ports, such as 41641/udp, that may listen on "+
			"a public address without a port_exposed alert) and security_alerts (on or off). Fields left out keep their value. "+
			"The result is the machine, as list_machines shows it.",
		mcp.WriteOptions{Idempotent: true},
		func(ctx context.Context, in updateMachineInput) (Overview, error) {
			if _, err := mcp.Machine(ctx, s.Lookup, in.Machine); err != nil {
				return Overview{}, err
			}
			return s.Update(ctx, in.Machine, Patch{Updates: in.Updates, SecurityAllowedPorts: in.SecurityAllowedPorts, SecurityAlerts: in.SecurityAlerts})
		})

	mcp.AddWrite(m, "rotate_machine_token", "Rotate a machine's token",
		"Replace a machine's token. The old token stops working at once, so its daemon is cut off until the new token is installed "+
			"on that machine. The new token is in the result, shown only this once.",
		mcp.WriteOptions{Destructive: true},
		func(ctx context.Context, in machineInput) (Token, error) {
			if _, err := mcp.Machine(ctx, s.Lookup, in.Machine); err != nil {
				return Token{}, err
			}
			token, err := s.RotateToken(ctx, in.Machine)
			return Token{Token: token}, err
		})

	mcp.AddWrite(m, "remove_machine", "Remove a machine",
		"Remove a machine and everything the hub knows about it: usage, backup settings, snapshots, history and notifications. "+
			"This can't be undone. Its backups in the bucket stay where they are, and its daemon can no longer report. The result names what was removed.",
		mcp.WriteOptions{Destructive: true, Idempotent: true},
		func(ctx context.Context, in machineInput) (mcp.Removed, error) {
			if _, err := mcp.Machine(ctx, s.Lookup, in.Machine); err != nil {
				return mcp.Removed{}, err
			}
			if err := s.Delete(ctx, in.Machine); err != nil {
				return mcp.Removed{}, err
			}
			return mcp.Removed{Removed: in.Machine}, nil
		})
}

func usageCursor(u Usage) server.Cursor {
	return server.Cursor{CreatedAt: u.CreatedAt.Unix(), ID: u.ID}
}
