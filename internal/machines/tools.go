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

// RegisterTools adds the machines tools to s. Both read through the same store methods as the REST routes.
func (s *Store) RegisterTools(m *mcp.Server) {
	mcp.AddReadOnly(m, "list_machines", "List machines",
		"List every machine that reports to this hub, by name. Each has last_seen (when its latest usage reading was taken, RFC 3339 UTC, "+
			"null before the first) and usage, its latest reading: cpu_load (1-minute load average), memory_used_percent and "+
			"disk_used_percent (0 to 100) and uptime_seconds. Start here to learn the machine names the other tools take.",
		func(ctx context.Context, _ noInput) (server.List[Overview], error) {
			machines, err := s.List(ctx)
			return server.List[Overview]{Data: machines}, err
		})

	mcp.AddReadOnly(m, "get_machine_usage", "Get machine usage",
		"Get one machine's usage readings, newest first, optionally between since and until. Each reading has cpu_load (1-minute "+
			"load average), memory_used_percent and disk_used_percent (0 to 100), uptime_seconds and created_at (when it was taken, "+
			"RFC 3339 UTC). Pass next_cursor as cursor for older readings.",
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
}

func usageCursor(u Usage) server.Cursor {
	return server.Cursor{CreatedAt: u.CreatedAt.Unix(), ID: u.ID}
}
