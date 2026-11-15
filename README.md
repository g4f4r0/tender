# Tender

Tender backs up your Linux servers to your own storage, such as an S3 bucket or an SFTP server, watches their disks and Docker services, and tells you by email, Slack, a webhook or your own program when something breaks. An AI client can run all of it over MCP.

You install a small daemon on each machine and one hub on any of them. Each daemon backs up its whole machine with [restic](https://restic.net) on the schedule you set, keeps snapshots for as many days as you choose, and once a week reads part of the data back to prove it restores. The hub holds the settings, collects usage and container state from every machine once a minute, and notifies your channels when a backup fails, a machine goes quiet, a disk fills up or a service stops. Each daemon can also send its journal and container logs straight to an OpenTelemetry collector or your own webhook.

Tender is one static Go binary. Your backups are plain, encrypted restic repositories, so you can restore them without Tender.

People and AI clients reach the hub with an API token, and the installer publishes it only on your tailnet. A token can change your backups and control your services, so keep it like a root password, and never expose the hub to the internet. See [Security](docs/security.md).

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- hub
sudo tender tokens add laptop   # prints your API token once
export TENDER_TOKEN=tender_...
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
