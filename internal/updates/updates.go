// Package updates owns how Tender updates itself: the hub's update settings, the rollouts that update
// the machines one at a time, the hub's look at GitHub for a new release, and on each machine the job
// that installs a release. The job is "tender update --apply VERSION", run in the transient unit
// tenderupdate, which survives the daemon's restart. cmd/tender wires this domain to the machines,
// whose versions and modes it reads, and to the actions, which carry an update to a machine.
package updates

import (
	"embed"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates this domain's tables. It needs the machines domain's tables first.
var Migrations = db.Migrations{Domain: "updates", Files: migrationFiles, Dir: "migrations"}
