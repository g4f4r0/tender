package notifications

import (
	"context"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

type listInput struct {
	Machine  string `json:"machine,omitempty" jsonschema:"only this machine's notifications; leave out for every machine"`
	Status   string `json:"status,omitempty" jsonschema:"only notifications with this status: pending, sent or resolved"`
	Severity string `json:"severity,omitempty" jsonschema:"only notifications with this severity: info, warning or critical"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most notifications to return, 1 to 500, default 50"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page"`
}

// RegisterTools adds the notifications tool to s. It reads through the same store method as the
// REST routes. lookup resolves machine names to ids.
func (s *Store) RegisterTools(m *mcp.Server, lookup mcp.MachineLookup) {
	mcp.AddReadOnly(m, "list_notifications", "List notifications",
		"List alerts, newest first, optionally of one machine, status or severity. Each has machine (null for the hub itself), "+
			"type, severity (info, warning or critical), status (pending and sent are open, resolved is closed), content and "+
			"created_at (RFC 3339 UTC). Leave status out to get all of them. Pass next_cursor as cursor for older ones.",
		func(ctx context.Context, in listInput) (server.List[Notification], error) {
			var machineID int64
			if in.Machine != "" {
				id, err := mcp.Machine(ctx, lookup, in.Machine)
				if err != nil {
					return server.List[Notification]{}, err
				}
				machineID = id
			}
			page, err := server.NewPage(in.Limit, in.Cursor)
			if err != nil {
				return server.List[Notification]{}, err
			}
			items, err := s.List(ctx, machineID, in.Status, in.Severity, page)
			return server.NewList(items, page, cursor), err
		})
}

func cursor(n Notification) server.Cursor {
	return server.Cursor{CreatedAt: n.CreatedAt.Unix(), ID: n.ID}
}
