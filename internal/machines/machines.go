// Package machines owns enrolled machines and their usage readings.
package machines

import (
	"embed"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates this domain's tables.
var Migrations = db.Migrations{Domain: "machines", Files: migrationFiles, Dir: "migrations"}
