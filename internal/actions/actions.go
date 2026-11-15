// Package actions owns what people and AI clients ask a machine's daemon to do: start, stop or
// restart a Docker service, read a service's logs, or read the machine's journal. The hub keeps
// one queue per machine; the daemon long-polls it, runs each action and reports how it ended.
// The services domain does the Docker work on the daemon and keeps the services' state on the
// hub; cmd/tender wires the two together through the Services and Runner interfaces.
package actions

import (
	"embed"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates this domain's tables. It needs the machines domain's tables first.
var Migrations = db.Migrations{Domain: "actions", Files: migrationFiles, Dir: "migrations"}
