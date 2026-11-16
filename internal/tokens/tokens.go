// Package tokens owns people's API tokens: every people route and the MCP endpoint need one.
package tokens

import (
	"embed"
	"time"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates this domain's tables.
var Migrations = db.Migrations{Domain: "tokens", Files: migrationFiles, Dir: "migrations"}

// Prefix starts every API token, so one is easy to tell from a machine token and to find in a leak.
const Prefix = "tender_"

// Token is an API token as GET /v1/tokens lists it. The token itself and its hash are never shown.
type Token struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	LastUsedAt *time.Time `json:"last_used_at"` // the last request with it, to the minute; null before the first
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// TokenInput is the body of POST /v1/tokens.
type TokenInput struct {
	Name string `json:"name"`
}

// TokenCreated is the 201 response of POST /v1/tokens. It is the only time the token is shown.
type TokenCreated struct {
	Token
	Value string `json:"token"`
}
