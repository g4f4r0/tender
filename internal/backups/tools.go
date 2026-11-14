package backups

import (
	"context"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

type statusInput struct {
	Machine string `json:"machine,omitempty" jsonschema:"machine name; leave out to get every machine"`
}

type snapshotsInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Limit   int    `json:"limit,omitempty" jsonschema:"most snapshots to return, 1 to 500, default 50"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page"`
}

type historyInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Type    string `json:"type,omitempty" jsonschema:"only runs of this type: backup, cleanup or check"`
	Status  string `json:"status,omitempty" jsonschema:"only runs with this status: pending, in_progress, completed or failed"`
	Limit   int    `json:"limit,omitempty" jsonschema:"most runs to return, 1 to 500, default 50"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page"`
}

// RegisterTools adds the backups tools to s. They read through the same store methods as the REST
// routes. lookup resolves machine names to ids.
func (s *Store) RegisterTools(m *mcp.Server, lookup mcp.MachineLookup) {
	mcp.AddReadOnly(m, "get_backup_status", "Get backup status",
		"Get the backup state of one machine, or of every machine when machine is left out. Per machine: settings (destination, "+
			"cron schedule in UTC, retention_days), latest_snapshot (size_bytes, created_at), last_backup and last_check (the newest "+
			"run of that type, with status completed, failed or in_progress, and its error), and snapshot_count. A part that "+
			"doesn't exist yet is null. Times are RFC 3339 UTC. Use this to answer whether backups are healthy.",
		func(ctx context.Context, in statusInput) (server.List[Summary], error) {
			if in.Machine == "" {
				sums, err := s.Summaries(ctx)
				return server.List[Summary]{Data: sums}, err
			}
			id, err := mcp.Machine(ctx, lookup, in.Machine)
			if err != nil {
				return server.List[Summary]{}, err
			}
			sum, err := s.Summary(ctx, id, in.Machine)
			return server.List[Summary]{Data: []Summary{sum}}, err
		})

	mcp.AddReadOnly(m, "list_backup_snapshots", "List backup snapshots",
		"List one machine's restic snapshots, newest first. Each has restic_id, size_bytes (null when unknown), the backed-up "+
			"paths and created_at (the snapshot time, RFC 3339 UTC). Pass next_cursor as cursor for older snapshots.",
		func(ctx context.Context, in snapshotsInput) (server.List[Snapshot], error) {
			id, err := mcp.Machine(ctx, lookup, in.Machine)
			if err != nil {
				return server.List[Snapshot]{}, err
			}
			page, err := server.NewPage(in.Limit, in.Cursor)
			if err != nil {
				return server.List[Snapshot]{}, err
			}
			snaps, err := s.ListSnapshots(ctx, id, page)
			return server.NewList(snaps, page, snapshotCursor), err
		})

	mcp.AddReadOnly(m, "list_backup_history", "List backup runs",
		"List one machine's backup, cleanup and check runs, newest first, optionally of one type or status. Each has type, "+
			"status (pending, in_progress, completed or failed), error (null unless it failed), snapshot_id and created_at (when it "+
			"started, RFC 3339 UTC). Pass next_cursor as cursor for older runs.",
		func(ctx context.Context, in historyInput) (server.List[History], error) {
			id, err := mcp.Machine(ctx, lookup, in.Machine)
			if err != nil {
				return server.List[History]{}, err
			}
			if err := validateFilters(in.Type, in.Status); err != nil {
				return server.List[History]{}, err
			}
			page, err := server.NewPage(in.Limit, in.Cursor)
			if err != nil {
				return server.List[History]{}, err
			}
			runs, err := s.ListHistory(ctx, id, in.Type, in.Status, page)
			return server.NewList(runs, page, historyCursor), err
		})
}

func snapshotCursor(sn Snapshot) server.Cursor {
	return server.Cursor{CreatedAt: sn.CreatedAt.Unix(), ID: sn.ID}
}

func historyCursor(h History) server.Cursor {
	return server.Cursor{CreatedAt: h.CreatedAt.Unix(), ID: h.ID}
}

// validateFilters is a 422 unless typ and status are empty or known.
func validateFilters(typ, status string) error {
	if typ != "" && !validType(typ) {
		return server.Invalid("type must be backup, cleanup or check.")
	}
	if status != "" && !validStatus(status) {
		return server.Invalid("status must be pending, in_progress, completed or failed.")
	}
	return nil
}
