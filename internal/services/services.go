// Package services owns each machine's Docker containers: the list its daemon reports, their
// state and usage, and the alert switch. The actions domain queues start, stop, restart and logs;
// this domain's daemon does them with Docker, and its store hears how they ended.
package services

import (
	"embed"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates this domain's tables. It needs the machines domain's tables first.
var Migrations = db.Migrations{Domain: "services", Files: migrationFiles, Dir: "migrations"}
