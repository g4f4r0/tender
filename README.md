# Tender

Tender backs up your Linux servers to your own S3 bucket, watches their disks and Docker services, and tells you by email, Slack, a webhook or your own program when something breaks. An AI client can run all of it over MCP.

You install a small daemon on each machine and one hub on any of them. Each daemon backs up its whole machine with [restic](https://restic.net) on the schedule you set, keeps snapshots for as many days as you choose, and once a week reads part of the data back to prove it restores. The hub holds the settings, collects usage and container state from every machine once a minute, and alerts when a backup fails, a machine goes quiet, a disk fills up or a service stops.

Tender is one static Go binary. Your backups are plain, encrypted restic repositories, so you can restore them without Tender.

The hub has no login in this version. Anyone who can reach it can change your backups and control your services, so the installer publishes it only on your tailnet. Never expose it to the internet. See [Security](docs/security.md).

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- hub
tender destinations add r2 --endpoint https://<account-id>.r2.cloudflarestorage.com --bucket my-backups --path tender
tender machines add apps
tender backups set apps --destination r2 --schedule "30 3 * * *" --retention-days 30
curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- daemon --hub https://apps.your-tailnet.ts.net --machine apps
```

The [quickstart](docs/quickstart.md) walks through each line, with what to save and what you should see.

## Docs

Start with the [quickstart](docs/quickstart.md). The [docs index](docs/README.md) lists every guide, concept and reference page.

## License

MIT. See [LICENSE](LICENSE).
