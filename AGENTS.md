# tender

One Go binary that orchestrates restic backups across machines and serves their state to AI clients over MCP. `tender daemon` runs on each machine; `tender hub` stores state in SQLite and serves the REST API and MCP. V1 has no web UI and no login: people and AI clients manage it through the `tender` CLI and MCP, which have the same features as the REST API.

## Commands

Run all four before every commit; CI runs the same, plus the tests with `-race`.

```sh
golangci-lint run ./...
go test ./...
govulncheck ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags --always --dirty)" -o tender ./cmd/tender
```

## Rules

1. Code is organized by domain: `internal/machines`, `internal/backups`, `internal/notifications`, `internal/services`, `internal/actions`. A domain never imports another domain. Shared packages (`db`, `config`, `server`, `mcp`, `client`) never import a domain. Only `cmd/tender` wires them together. `golangci-lint` enforces this.
2. Each domain owns its migrations in `internal/<domain>/migrations/NNN_name.sql`. Never edit an applied migration; add a new one.
3. Schema conventions: every table has `id`, its own columns, then `created_at` and `updated_at` (integer Unix seconds, UTC). Table names carry their domain. Use `type`, never `kind`. Columns with a unit carry it (`size_bytes`, `uptime_seconds`, `*_percent`, `retention_days`). Status words are `pending`, `in_progress`, `completed`, `failed`. Alerts use `severity` (`info`, `warning`, `critical`). Secrets (bucket keys, backup passwords) are stored encrypted with `TENDER_ENCRYPTION_KEY` (see `internal/secret`), and only a machine's own daemon ever gets them back; people routes, MCP and logs never show them. Machine tokens are stored as hashes.
4. REST lives under `/v1` as nested resources (`/v1/machines/{machine}/backups/snapshots`). JSON fields match column names. Errors are RFC 9457 problem details.
5. restic runs as a subprocess with fixed argument lists, never through a shell. Status comes from its exit code.
6. Plain names. Add a package, option, or file only when the current milestone needs it.
7. Every route people may call has an MCP tool, listed in `mcpCoverage` in `cmd/tender/parity_test.go`. User docs are plain Markdown in `docs/`; `docs/api.md` and `docs/mcp.md` name every such route and tool, and the same test fails when one is missing.
