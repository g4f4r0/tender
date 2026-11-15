// Package apps owns the apps Tender deploys on the machines and how they are reached. This branch
// holds only the hub's Cloudflare settings, which the cloudflare exposure uses; the apps
// themselves come with the apps branch.
package apps

import (
	"embed"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates this domain's tables. It needs the machines domain's tables first.
var Migrations = db.Migrations{Domain: "apps", Files: migrationFiles, Dir: "migrations"}
