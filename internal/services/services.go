// Package services owns each machine's Docker containers: the list its daemon reports, the
// actions people and AI clients queue for them (start, stop, restart, logs) and the alert switch.
package services

import (
	"embed"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates this domain's tables. It needs the machines domain's tables first.
var Migrations = db.Migrations{Domain: "services", Files: migrationFiles, Dir: "migrations"}
