# Tender

Tender backs up your Linux servers to your own S3 bucket, watches their disks and Docker services, and emails you when something breaks. An AI client can run all of it over MCP.

You install a small daemon on each machine and one hub on any of them. Every night, each daemon backs up its whole machine with [restic](https://restic.net), keeps snapshots for as many days as you choose, and once a week reads part of the data back to prove it restores. The hub holds the settings, collects usage and container state from every machine once a minute, and alerts when a backup fails, a machine goes quiet, a disk fills up or a service stops.

Tender is one static Go binary. Your backups are plain, encrypted restic repositories, so you can restore them without Tender.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- hub
tender destinations add r2 --endpoint https://<account-id>.r2.cloudflarestorage.com --bucket my-backups --path tender
tender machines add apps
tender backups set apps --destination r2 --schedule "30 3 * * *" --retention-days 30
curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- daemon --hub https://apps.your-tailnet.ts.net --machine apps
```

The [quickstart](docs/quickstart.md) walks through each line, with what to save and what you should see.

> [!WARNING]
> The hub has no login in this version. Anyone who can reach it can change your backups and control your services. The installer publishes it only on your tailnet. Never expose it to the internet. See [Security](docs/security.md).

## Docs

1. [Quickstart](docs/quickstart.md) gets you from nothing to a first backup in about 10 minutes.
2. [Install](docs/install.md) covers the hub, the daemons, updates and removal.
3. Guides do one task each: [add a machine](docs/guides/add-a-machine.md), [restore files](docs/guides/restore-files.md), [control a service](docs/guides/control-a-service.md), [read a machine's logs](docs/guides/read-machine-logs.md), [set up email alerts](docs/guides/set-up-email-alerts.md) and [connect an AI client](docs/guides/connect-an-ai-client.md).
4. Concepts explain how it works: [backups](docs/backups.md), [services](docs/services.md), [alerts](docs/alerts.md) and [security](docs/security.md).
5. References list everything: the [REST API](docs/api.md), the [MCP tools](docs/mcp.md) and the [CLI](docs/cli.md).

## License

MIT. See [LICENSE](LICENSE).
