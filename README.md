# Tender

Whole-server backups for your Linux machines, control of their Docker services, one place to see how they're doing, and an MCP endpoint so your AI client can run it all for you.

You install a small daemon on each machine and one hub on any of them. Each night, every daemon backs up its whole machine to your S3 bucket, keeps snapshots for as long as you choose, and once a week reads part of the data back to prove it restores. The hub holds the settings, collects results and usage from every machine, and emails you when a backup fails, a machine goes quiet, a disk fills up or a service stops.

You set everything up with the `tender` command or from an AI client such as Claude Code. Root is needed only to install.

Tender is one static Go binary. It uses [restic](https://restic.net) underneath, so your backups are in a well-known, encrypted format you can read without Tender.

## What you get

1. Whole-server backups of every real filesystem, without caches, temp files, container images or `node_modules`. Snapshots are deduplicated and encrypted before they leave the machine.
2. Retention by days, and a weekly check that reads back 5% of the stored data.
3. CPU load, memory, disk and uptime from every machine, once a minute.
4. Every Docker container on every machine, with its state and health, and a way to start, stop, restart it and read its logs.
5. Email alerts to as many people as you like: one email when something goes wrong, one if it gets worse and one when it's fixed.
6. MCP tools for AI clients that can do everything the `tender` command does, from "did last night's backups work?" to adding a machine.
7. Backups keep running when the hub is down. Daemons use their last known settings and send their reports once the hub is back.

## Requirements

1. Linux on x86_64 with systemd.
2. A bucket on any S3-compatible storage, such as Cloudflare R2, AWS S3, Backblaze B2, Wasabi or MinIO, with an access key that can read and write it.
3. [Tailscale](https://tailscale.com) on every machine is recommended. The installer uses it to publish the hub privately on your tailnet. Without it, put your own proxy and access control in front of the hub.

> [!WARNING]
> The hub has no login in this version. Anyone who can reach it can add, change and remove machines, destinations and alert recipients. It never shows bucket keys or passwords to anyone. Keep it on your tailnet or behind your own access control, and never expose it to the internet.
>
> Web pages can't use the hub through your browser: it refuses requests from other websites, and it answers only to its IP addresses, its Tailscale name and the names in `TENDER_HOSTS`.

## Quick start

This sets up a hub on `web-1` and backs up the same machine to a bucket called `my-backups`.

### 1. Install the hub

```sh
curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- hub \
  --smtp-host smtp.example.com --smtp-from "Tender <alerts@example.com>" --smtp-username alerts@example.com
```

The installer downloads the latest release, checks it against the release's `SHA256SUMS` and installs `/usr/local/bin/tender`. It asks for the SMTP password with typing hidden. Then it writes `/etc/tender/hub.env` with a new encryption key, starts the `tenderhub` service and publishes the hub on your tailnet, such as `https://web-1.your-tailnet.ts.net`.

Leave out the `--smtp-` options to start without email, and run the command again with them later. Running it again keeps the key and any setting you don't pass.

Back up `/etc/tender/hub.env`. Its key decrypts the bucket keys and passwords the hub stores, and the hub won't start without the same key.

### 2. Tell the hub where backups go

```sh
tender destinations add r2 --endpoint https://<account-id>.r2.cloudflarestorage.com --bucket my-backups --path tender
```

It asks for the access key ID and the secret access key, with typing hidden. It also asks for a repository password; leave it empty and the hub makes a strong one and shows it once.

**Save that password in a password manager.** Every machine's backups at this destination are encrypted with it, and without it nobody can restore them, including you. It can't change later.

Each machine gets its own folder, such as `my-backups/tender/web-1`. Only the endpoint changes between providers. AWS S3 uses one like `https://s3.us-east-1.amazonaws.com`, and Backblaze B2 one like `https://s3.us-west-004.backblazeb2.com`.

### 3. Add the machine and its schedule

```sh
tender machines add web-1
tender backups set web-1 --destination r2 --schedule "30 3 * * *" --retention-days 30
```

`machines add` prints the machine's token once. Keep it for step 5. The schedule is standard cron, in UTC.

### 4. Choose who gets alerts

```sh
tender recipients add you@example.com
tender email test
```

Add as many recipients as you need. `email test` sends a test email to each of them.

### 5. Install the daemon

On the machine you're backing up:

```sh
curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- daemon \
  --hub https://web-1.your-tailnet.ts.net --machine web-1
```

The installer asks for the token with typing hidden. It installs restic, creates the `tender` user, finds the filesystems to back up and starts the `tenderd` service. The daemon gets its destination, keys and schedule from the hub, so there's nothing else to copy to the machine.

The first backup starts within a minute, and the next ones follow the schedule. Watch it with `journalctl -u tenderd -f`, or check on every machine at once with:

```sh
tender status
```

To add more machines, repeat steps 3 and 5 for each.

## The tender command

`tender` talks to the hub over its API, so it doesn't need root. On the hub machine it finds the hub by itself. Anywhere else, set `TENDER_HUB=https://web-1.your-tailnet.ts.net` or pass `--hub`.

| Command | What it does |
| --- | --- |
| `tender status` | Every machine, its last backup, its services and the open alerts |
| `tender machines list \| add \| remove \| rotate-token` | Manage machines and their tokens |
| `tender destinations list \| add \| remove` | Manage where backups go |
| `tender backups status \| set \| unset` | See and change each machine's backups |
| `tender services list \| start \| stop \| restart \| logs \| alerts` | See and control each machine's Docker services |
| `tender recipients list \| add \| remove` | Manage who gets alert emails |
| `tender email test` | Send a test email to every recipient |
| `tender update` | Install the latest release on this machine (root) |

Run `tender help`, or add `--help` to any command, for the details.

## Services

Every daemon reports its machine's Docker containers to the hub once a minute. Each container is a service, named by its container name, such as `shop-web-1` for a compose service. A machine without Docker has no services and works as before.

```sh
tender services list web-1
tender services restart web-1 shop-web-1
tender services logs web-1 shop-web-1 --lines 500
tender services alerts web-1 shop-worker-1 off
```

`start`, `stop`, `restart` and `logs` go to the machine's daemon, which runs them through Docker's API. The command waits up to 60 seconds for the result. If the daemon hasn't answered by then, the action stays queued for 15 minutes. `logs` shows at most the last 2000 lines and 256 KiB.

The daemon reaches Docker through `/var/run/docker.sock`, as a member of the `docker` group. The installer adds the group to `tenderd` when the machine has one. If you install Docker after Tender, run `sudo tender install refresh`.

> [!WARNING]
> The `docker` group is root-equivalent: whoever can use Docker's socket controls the whole machine. Tender's daemon uses it only to list, start, stop and restart containers and to read their logs. Anyone who can reach the hub can do the same, so keep the hub private.

## Ask your AI client

The hub serves MCP at `/mcp`. To add it to Claude Code:

```sh
claude mcp add --transport http tender https://web-1.your-tailnet.ts.net/mcp --scope user
```

Then ask things like "did last night's backups work?", "which machine is running out of disk?" or "back up web-2 every night at 4 and keep two weeks".

The tools cover everything the `tender` command does. Each one tells your client whether it only reads, changes something or removes something, so the client can ask you before a removal. The tools never return bucket keys or passwords. Keys you give a tool pass through the AI conversation, so for real keys, `tender destinations add` is safer: it hides them as you type.

## Email alerts

The hub checks every machine once a minute:

| Alert | When | Severity |
| --- | --- | --- |
| `machine_silent` | No report from the machine for 10 minutes | critical |
| `backup_failed` | The latest finished backup failed | critical |
| `backup_stale` | No completed backup for 26 hours | warning, critical after 50 hours |
| `disk_full` | The disk is at least 90% full | warning, critical at 95% |
| `service_down` | The service is exited or dead in two reports in a row, unless Tender stopped it | critical |
| `service_restarting` | The service is restarting, or Docker restarted it again, in two reports in a row | critical |
| `service_unhealthy` | The service's health check fails in two reports in a row | warning |

Each service has its own alerts, and you can turn them off and on with `tender services alerts`. Turning them off resolves the service's open alerts. A silent machine alerts once, as `machine_silent`, not for each of its services.

You get one email when an alert opens, one if it gets worse, and one when it resolves. If a send fails, the hub keeps retrying until the email goes out.

Email goes out through any SMTP server with implicit TLS. The settings live in `/etc/tender/hub.env`:

| Variable | Default | What it sets |
| --- | --- | --- |
| `TENDER_ENCRYPTION_KEY` | made at install | The key that encrypts the secrets the hub stores. Required |
| `TENDER_SMTP_HOST` | | The SMTP server. Email is off without it |
| `TENDER_SMTP_PORT` | `465` | The SMTP port |
| `TENDER_SMTP_USERNAME` | | The SMTP login, for servers that ask for one |
| `TENDER_SMTP_PASSWORD` | | The SMTP password |
| `TENDER_SMTP_FROM` | | The sender, such as `Tender <alerts@example.com>` |
| `TENDER_LISTEN` | `127.0.0.1:8080` | Where the hub listens |
| `TENDER_HOSTS` | | More names the hub answers to, comma-separated, such as `tender.example.com` behind your own proxy |
| `TENDER_DATABASE` | `/var/lib/tender/hub/hub.db` | The hub's database |

After editing the file by hand, run `sudo systemctl restart tenderhub`.

## Updating

```sh
sudo tender update
```

`update` checks and installs the latest release, then refreshes and restarts what Tender runs on the machine. Add `--version v0.2.0` for a specific release. For a fresh install of a specific release, put `TENDER_VERSION=v0.2.0` before `sh` in the install command.

## Restoring

`tender restore` is coming. Until then, restore with restic on any Linux machine, using the bucket keys and the repository password from your password manager:

```sh
export AWS_ACCESS_KEY_ID=<access key ID>
export AWS_SECRET_ACCESS_KEY=<secret access key>
export RESTIC_PASSWORD=<repository password>
export RESTIC_REPOSITORY=s3:https://<account-id>.r2.cloudflarestorage.com/my-backups/tender/web-1

restic snapshots --host web-1 --tag tender
restic restore latest --host web-1 --tag tender --target /root/restore --include /etc/nginx
```

On a machine whose daemon still runs, root can use the daemon's copy of the password instead: `export RESTIC_PASSWORD_FILE=/var/lib/tender/daemon/repository-password`.

Restore into an empty folder, like `/root/restore`, and copy back what you need. Restoring over a running system can break it.

## How it works

The hub keeps everything in one SQLite database and listens on `127.0.0.1:8080` only. It stores bucket keys and repository passwords encrypted with AES-256-GCM under `TENDER_ENCRYPTION_KEY`. It sends them to one caller only: the daemon of a machine that uses that destination, signed in with its own token. No other route, MCP tool or log line ever shows them.

Each daemon signs in with its own token. The hub stores only a SHA-256 hash of it, and a token works only for its own machine: it can't report for another one or get another machine's keys.

The daemon runs as the `tender` system user. It can read every file on the machine through the `CAP_DAC_READ_SEARCH` capability, but can write only to its own folder. On a machine with Docker it is also in the `docker` group, which is root-equivalent. It keeps the last settings it got from the hub, readable only by itself, so backups go on when the hub is unreachable.

Every backup covers all real filesystems on the machine and leaves out:

```
/var/lib/docker  /var/lib/containerd  /var/cache  /tmp  /var/tmp
/home/*/.cache  /home/*/.local/share/docker  **/node_modules
```

Docker volumes live under `/var/lib/docker`, so they're left out as well. Dump application databases with their own tools into a folder that Tender backs up. You can change the paths and excludes in `/etc/tender/daemon.toml`.

### Where things live

| Path | What it holds |
| --- | --- |
| `/usr/local/bin/tender` | The Tender binary |
| `/usr/local/bin/restic` | restic 0.19.1, checked against its official SHA-256 |
| `/etc/tender/hub.env` | The hub's encryption key and email settings, readable only by root |
| `/etc/tender/daemon.toml` | The daemon's hub, machine name, backup paths and excludes |
| `/etc/tender/credentials/hub-token` | The daemon's token, readable only by root and `tender` |
| `/var/lib/tender/hub/hub.db` | The hub's database |
| `/var/lib/tender/daemon/` | The daemon's state, its last settings and any reports waiting for the hub |

## License

MIT. See [LICENSE](LICENSE).
