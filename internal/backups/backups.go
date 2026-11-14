// Package backups owns backup destinations, per-machine settings, snapshots and history.
package backups

import (
	"embed"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates this domain's tables. It needs the machines domain's tables first.
var Migrations = db.Migrations{Domain: "backups", Files: migrationFiles, Dir: "migrations"}
