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

type addDestinationInput struct {
	Name     string         `json:"name" jsonschema:"new destination name: lowercase letters, digits and hyphens, up to 63 characters"`
	Type     string         `json:"type,omitempty" jsonschema:"s3 (the default), local, sftp, rest or rclone"`
	Config   map[string]any `json:"config" jsonschema:"the type's settings; see the tool's description for each type's fields"`
	Password string         `json:"password,omitempty" jsonschema:"restic repository password for every machine at this destination; leave out and the hub makes one and returns it once"`
}

type updateDestinationInput struct {
	Destination string         `json:"destination" jsonschema:"destination name, as list_destinations shows it"`
	Config      map[string]any `json:"config,omitempty" jsonschema:"the config fields to change; null removes an optional one, and a secret given as *** stays as it is"`
	Password    *string        `json:"password,omitempty" jsonschema:"not accepted: the password can't change, because every repository is encrypted with it"`
	Type        *string        `json:"type,omitempty" jsonschema:"not accepted: the type can't change"`
}

type removeDestinationInput struct {
	Destination string `json:"destination" jsonschema:"destination name"`
}

type setBackupInput struct {
	Machine       string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Destination   string `json:"destination" jsonschema:"destination name"`
	Schedule      string `json:"schedule" jsonschema:"standard 5-field cron expression in UTC, such as 30 3 * * *"`
	RetentionDays int    `json:"retention_days" jsonschema:"days to keep snapshots, at least 1"`
}

type unsetBackupInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
}

// RegisterTools adds the backups tools to s. They use the same store methods as the REST
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
		"List one machine's restic snapshots, newest first. Each has id, restic_id, size_bytes (null when unknown), the backed-up "+
			"paths, created_at (the snapshot time, RFC 3339 UTC) and updated_at. At most limit a page, 1 to 500, default 50; pass next_cursor as cursor for older snapshots.",
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
		"List one machine's backup, cleanup and check runs, newest first, optionally of one type or status. Each has id, type, "+
			"status (pending, in_progress, completed or failed), error (null unless it failed), snapshot_id and created_at (when it "+
			"started, RFC 3339 UTC) and updated_at. At most limit a page, 1 to 500, default 50; pass next_cursor as cursor for older runs.",
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

	mcp.AddReadOnly(m, "list_destinations", "List backup destinations",
		"List the places backups go, by name. Each has id, name, type (s3, local, sftp, rest or rclone), config (the type's "+
			"fields, with each secret shown as *** and <field>_set true), and location: the restic repository, under which each "+
			"machine's repository is a folder named after the machine. Passwords are never shown. Each also has created_at and "+
			"updated_at (RFC 3339 UTC). Use a name here as the destination of set_backup.",
		func(ctx context.Context, _ struct{}) (server.List[Destination], error) {
			list, err := s.Destinations(ctx)
			return server.List[Destination]{Data: list}, err
		})

	const secrets = " Keys and passwords you pass here go through this conversation; for real keys, the owner is safer typing them " +
		"into \"tender destinations add\", which hides them as they are typed. They are stored encrypted and no tool, route or log ever shows them again."

	mcp.AddWrite(m, "add_destination", "Add a backup destination",
		"Add a place to send backups. Each machine gets its own folder in it. The config fields by type: "+destinationFields+
			" Without a password, the hub makes one and returns it in the result, shown only this once: tell the owner to save it "+
			"somewhere safe, because the backups can't be restored without it. A password you pass is not returned. The result is the "+
			"destination, as list_destinations shows it, with password when the hub made it. Check it from a machine with test_destination."+secrets,
		mcp.WriteOptions{},
		func(ctx context.Context, in addDestinationInput) (DestinationCreated, error) {
			return s.AddDestination(ctx, DestinationInput(in))
		})

	mcp.AddWrite(m, "update_destination", "Update a backup destination",
		"Change fields of a destination's config; anything left out stays as it is, null removes an optional field, and a secret "+
			"given as *** stays as it is. The type and the password can't change, because the repositories depend on them. "+
			"The result is the destination, as list_destinations shows it."+secrets,
		mcp.WriteOptions{Idempotent: true},
		func(ctx context.Context, in updateDestinationInput) (Destination, error) {
			switch {
			case in.Password != nil:
				return Destination{}, errPasswordFixed
			case in.Type != nil:
				return Destination{}, errTypeFixed
			}
			return s.PatchDestination(ctx, in.Destination, DestinationPatch{Config: in.Config})
		})

	mcp.AddWrite(m, "remove_destination", "Remove a backup destination",
		"Remove a destination that no machine uses; it is refused while one does, and the error names them. "+
			"The backups already in the bucket stay there. The result names what was removed.",
		mcp.WriteOptions{Destructive: true, Idempotent: true},
		func(ctx context.Context, in removeDestinationInput) (mcp.Removed, error) {
			if err := s.DeleteDestination(ctx, in.Destination); err != nil {
				return mcp.Removed{}, err
			}
			return mcp.Removed{Removed: in.Destination}, nil
		})

	mcp.AddWrite(m, "set_backup", "Set a machine's backups",
		"Set where, when and for how long a machine's backups are kept: a destination, a cron schedule in UTC and the days to keep "+
			"snapshots. It replaces any earlier settings. A daemon without settings picks them up within 30 seconds; a daemon that "+
			"already has some checks every 10 minutes. The result is the settings: destination, schedule, retention_days, created_at and updated_at.",
		mcp.WriteOptions{Idempotent: true},
		func(ctx context.Context, in setBackupInput) (Settings, error) {
			id, err := mcp.Machine(ctx, lookup, in.Machine)
			if err != nil {
				return Settings{}, err
			}
			return s.SetSettings(ctx, id, in.Destination, in.Schedule, in.RetentionDays)
		})

	mcp.AddWrite(m, "unset_backup", "Stop a machine's backups",
		"Remove a machine's backup settings, so it stops backing up. Its snapshots and history stay. Call set_backup to start again. The result names what was removed.",
		mcp.WriteOptions{Destructive: true, Idempotent: true},
		func(ctx context.Context, in unsetBackupInput) (mcp.Removed, error) {
			id, err := mcp.Machine(ctx, lookup, in.Machine)
			if err != nil {
				return mcp.Removed{}, err
			}
			if err := s.DeleteSettings(ctx, id); err != nil {
				return mcp.Removed{}, err
			}
			return mcp.Removed{Removed: "backup settings of " + in.Machine}, nil
		})
}

// destinationFields says each type's config fields, for the tool descriptions.
const destinationFields = "s3 (any S3-compatible bucket: AWS, Cloudflare R2, Backblaze B2, Wasabi, MinIO, Google Cloud Storage interop): " +
	"endpoint (URL), bucket, path (folder in the bucket, optional), access_key_id and secret_access_key (secrets). " +
	"local (a folder on each machine, such as a mounted disk): path (absolute). " +
	"sftp: host, port (default 22), user, path (absolute, or relative to the user's home); the SSH key and known_hosts must be " +
	"installed by root on each machine in /etc/tender/ssh, so no key goes through Tender. " +
	"rest (restic's rest-server): url, username and password (secret; give both or neither). " +
	"rclone: remote (an rclone remote and folder, such as gdrive:backups), configured on each machine in /etc/tender/rclone.conf."

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
