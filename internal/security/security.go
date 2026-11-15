// Package security owns each machine's security checks. The daemon collects them every 15 minutes:
// the firewall, the sockets listening on a public address, the SSH server's password and root
// logins, the pending and security updates, unattended-upgrades and a required reboot. It only reads.
// The hub keeps each machine's latest report, and the notifications rules alert on it; cmd/tender
// wires the two. The daemon also runs the upgrade of the system's packages for the actions domain.
package security

import (
	"embed"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates this domain's tables. It needs the machines domain's tables first.
var Migrations = db.Migrations{Domain: "security", Files: migrationFiles, Dir: "migrations"}
