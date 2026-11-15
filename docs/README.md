# Tender docs

Install Tender, do one task at a time, and look up every command, route and tool.

New to Tender? Start with the [quickstart](quickstart.md).

The examples use a machine called `apps`, a Docker service called `web`, a destination called `r2` on Cloudflare R2, and a hub at `https://apps.your-tailnet.ts.net`. Replace them with your own.

## Get started

1. [Quickstart](quickstart.md) sets up a hub, backs up one machine and lists its services in about 10 minutes.
2. [Install](install.md) covers the hub and the daemons in full, with API and machine tokens, the root daemon, the updates to v0.4 and v0.3.0, and removal.

## Guides

Each guide does one task.

1. [Add a machine](guides/add-a-machine.md) to back up another server.
2. [Restore files](guides/restore-files.md) from a snapshot with restic.
3. [Control a service](guides/control-a-service.md) to restart, stop or start a container and read its logs.
4. [Read a machine's logs](guides/read-machine-logs.md) from its systemd journal.
5. [Set up email alerts](guides/set-up-email-alerts.md) through your SMTP server.
6. [Connect an AI client](guides/connect-an-ai-client.md) such as Claude Code over MCP.

## Concepts

1. [Backups](backups.md) explains the destination types, what a backup covers, retention, the weekly check and encryption.
2. [Services](services.md) explains what the daemon reports about each container and how actions reach a machine.
3. [Log shipping](log-shipping.md) sends each machine's journal and container logs to an OpenTelemetry collector or a webhook, with the webhook contract.
4. [Notifications](notifications.md) lists every event and when it fires and resolves.
5. [Adapters](adapters.md) explains channel types, the event they get, and how to write a webhook receiver or an exec adapter.
6. [Metrics](metrics.md) lists what the hub serves to Prometheus at `/metrics`.
7. [Security](security.md) explains who can reach the hub, its API tokens, how secrets are stored, what the daemon can do and what it checks on each machine.
8. [Updates](updates.md) explains how Tender updates itself from the hub, one machine at a time.

## Reference

1. [REST API](api.md) lists every route with its parameters, examples and errors.
2. [MCP tools](mcp.md) lists every tool, and which route each one covers.
3. [CLI](cli.md) lists every `tender` command and option.
