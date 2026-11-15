# Tender docs

Install Tender, do one task at a time, and look up every command, route and tool.

New to Tender? Start with the [quickstart](quickstart.md).

The examples use a machine called `apps`, a Docker service called `web`, a destination called `r2` on Cloudflare R2, and a hub at `https://apps.your-tailnet.ts.net`. Replace them with your own.

## Get started

1. [Quickstart](quickstart.md) sets up a hub, backs up one machine and lists its services in about 10 minutes.
2. [Install](install.md) covers the hub and the daemons in full, with tokens, the Docker and journal groups, updates and removal.

## Guides

Each guide does one task.

1. [Add a machine](guides/add-a-machine.md) to back up another server.
2. [Restore files](guides/restore-files.md) from a snapshot with restic.
3. [Control a service](guides/control-a-service.md) to restart, stop or start a container and read its logs.
4. [Read a machine's logs](guides/read-machine-logs.md) from its systemd journal.
5. [Set up email alerts](guides/set-up-email-alerts.md) through your SMTP server.
6. [Connect an AI client](guides/connect-an-ai-client.md) such as Claude Code over MCP.

## Concepts

1. [Backups](backups.md) explains what a backup covers, retention, the weekly check and encryption.
2. [Services](services.md) explains what the daemon reports about each container and how actions reach a machine.
3. [Alerts](alerts.md) lists every alert and when it opens and resolves.
4. [Security](security.md) explains who can reach the hub, how secrets are stored and what the daemon can do.

## Reference

1. [REST API](api.md) lists every route with its parameters, examples and errors.
2. [MCP tools](mcp.md) lists every tool, and which route each one covers.
3. [CLI](cli.md) lists every `tender` command and option.
