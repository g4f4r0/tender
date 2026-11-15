// Package logs owns log sinks: where each daemon sends its journal and its containers' logs.
// The hub keeps the sinks and what each daemon says about sending to them; the logs go from the
// daemon straight to the sink and never through the hub.
package logs

import (
	"embed"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates this domain's tables. It needs the machines domain's tables first.
var Migrations = db.Migrations{Domain: "logs", Files: migrationFiles, Dir: "migrations"}
