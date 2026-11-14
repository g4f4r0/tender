# Tender

Whole-server backups for your Linux machines, one place to see how they're doing, and an MCP endpoint so your AI client can check on them for you.

You run a small agent on each machine and one hub on any of them. Each night, every agent backs up its whole machine to your S3 bucket, keeps snapshots for as long as you choose, and once a week reads part of the data back to prove it restores. The hub collects the results and usage from every machine. It emails you when a backup fails, a machine goes quiet or a disk fills up. It also answers questions like "did last night's backups work?" from Claude Code or any other MCP client.

Tender is one static Go binary. It uses [restic](https://restic.net) underneath, so your backups are in a well-known, encrypted format you can read without Tender.

## What you get

1. **Whole-server backups.** Every real filesystem on the machine, without caches, temp files, container images or `node_modules`. Snapshots are deduplicated and encrypted before they leave the machine.
2. **Retention and checks.** Each machine keeps snapshots for the number of days you set. Once a week, Tender reads back 5% of the stored data.
3. **Usage.** CPU load, memory, disk and uptime from every machine, once a minute.
4. **Email alerts.** One email when something goes wrong and one when it's fixed. No floods.
5. **Read-only MCP.** Six tools for AI clients: `list_machines`, `get_machine_usage`, `get_backup_status`, `list_backup_snapshots`, `list_backup_history` and `list_notifications`.
6. **Keeps going when the hub is down.** Agents back up on their last known schedule and send their reports once the hub is back.

## Requirements

1. Linux on x86_64 with systemd.
2. [Tailscale](https://tailscale.com) on every machine. The hub is reachable only over your tailnet.
3. A bucket on any S3-compatible storage, such as Cloudflare R2, AWS S3, Backblaze B2, Wasabi or MinIO, with an access key that can read and write it.

## Quick start

This sets up a hub and backs up the same machine. The machine is called `web-1` and the bucket `my-backups`.

### 1. Install the hub

```sh
curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- hub
```

The installer downloads the latest release, checks it against the release's `SHA256SUMS`, installs `/usr/local/bin/tender` and starts the `tender-hub` service. It then publishes the hub on your tailnet with Tailscale Serve and prints its address, such as `https://web-1.your-tailnet.ts.net`.

### 2. Tell the hub where backups go

```sh
sudo tender hub add-destination r2 --type s3 \
  --endpoint https://<account-id>.r2.cloudflarestorage.com \
  --bucket my-backups --path tender
```

Each machine that uses this destination gets its own folder, `my-backups/tender/<machine>`.

`--type s3` works with every S3-compatible service, and only the endpoint changes. AWS S3 uses one like `https://s3.us-east-1.amazonaws.com`, and Backblaze B2 one like `https://s3.us-west-004.backblazeb2.com`.

### 3. Add the machine and its schedule

```sh
sudo tender hub add-machine web-1
sudo tender hub set-backup web-1 --destination r2 --schedule "30 3 * * *" --retention-days 30
```

`add-machine` prints the machine's token once. Keep it for the next step. The schedule is standard cron, in UTC.

### 4. Install the agent

On the machine you're backing up:

```sh
curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- agent \
  --hub https://web-1.your-tailnet.ts.net --machine web-1
```

The installer asks for the token and hides what you type. It installs restic, creates the `tender` user, finds the filesystems to back up and starts the `tender-agent` service.

### 5. Add the bucket's keys

```sh
sudo tender install credential r2.access-key-id < access-key-id.txt
sudo tender install credential r2.secret-access-key < secret-access-key.txt
sudo tender install credential r2.password < password.txt
```

`r2.password` encrypts this machine's backups. Make it long and random, for example with `openssl rand -base64 32`, and keep a copy off the machine, such as in a password manager. **Without it, nobody can read the backups, including you.**

The first backup starts within a minute. Watch it with `journalctl -u tender-agent -f`.

To add more machines, repeat steps 3 to 5 for each.

## Email alerts

The hub sends email through any SMTP server that accepts implicit TLS, usually on port 465:

```sh
curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- hub --force \
  --email-to you@example.com --email-from "Tender <alerts@example.com>" \
  --smtp-host smtp.example.com --smtp-port 465 --smtp-username alerts@example.com
sudo tender install credential smtp-password < smtp-password.txt
sudo tender hub test-email
```

It checks every machine once a minute:

| Alert | When | Severity |
| --- | --- | --- |
| `machine_silent` | No report from the machine for 10 minutes | critical |
| `backup_failed` | The latest finished backup failed | critical |
| `backup_stale` | No completed backup for 26 hours | warning, critical after 50 hours |
| `disk_full` | The disk is at least 90% full | warning, critical at 95% |

You get one email when an alert opens, one if it gets worse, and one when it resolves. If a send fails, the hub keeps retrying until the email goes out.

## Ask your AI client

The hub serves MCP at `/mcp`. To add it to Claude Code:

```sh
claude mcp add --transport http tender https://web-1.your-tailnet.ts.net/mcp --scope user
```

Then ask things like "did last night's backups work?" or "which machine is running out of disk?". Every tool only reads.

On the hub machine, `sudo tender mcp --stdio` serves the same tools to local clients.

## Updating

```sh
sudo tender update
```

`update` checks and installs the latest release, then refreshes and restarts what Tender runs on the machine. Add `--version v0.1.0` for a specific release. For a fresh install of a specific release, put `TENDER_VERSION=v0.1.0` before `sh` in the install command.

## Restoring

`tender restore` is coming. Until then, restore with restic, as root on any machine that has the keys:

```sh
export AWS_ACCESS_KEY_ID=$(cat /etc/tender/credentials/r2.access-key-id)
export AWS_SECRET_ACCESS_KEY=$(cat /etc/tender/credentials/r2.secret-access-key)
export RESTIC_PASSWORD_FILE=/etc/tender/credentials/r2.password
export RESTIC_REPOSITORY=s3:https://<account-id>.r2.cloudflarestorage.com/my-backups/tender/web-1

restic snapshots --host web-1 --tag tender
restic restore latest --host web-1 --tag tender --target /root/restore --include /etc/nginx
```

Restore into an empty folder, like `/root/restore`, and copy back what you need. Restoring over a running system can break it.

If the machine is gone, any Linux machine with restic works. Use the keys and password from your password manager.

## How it works

The agent runs as the `tender` system user. It can read every file on the machine through the `CAP_DAC_READ_SEARCH` capability, but can write only to its own folder. It gets its destination, schedule and retention from the hub and keeps the last copy, so backups go on when the hub is unreachable.

The hub keeps everything in one SQLite database. It listens on `127.0.0.1:8080` only, and Tailscale Serve is the only way in. Each agent signs in with its own token. The hub stores only a SHA-256 hash of it, and an agent can write only its own machine's data.

Bucket keys never reach the hub. Each machine holds only its own keys, and on the hub machine, the hub service can't read them.

Every backup covers all real filesystems on the machine and leaves out:

```
/var/lib/docker  /var/lib/containerd  /var/cache  /tmp  /var/tmp
/home/*/.cache  /home/*/.local/share/docker  **/node_modules
```

Docker volumes live under `/var/lib/docker`, so they're left out as well. Dump application databases with their own tools into a folder that Tender backs up. You can change the paths and excludes in `/etc/tender/agent.toml`.

### Where things live

| Path | What it holds |
| --- | --- |
| `/usr/local/bin/tender` | The Tender binary |
| `/usr/local/bin/restic` | restic 0.19.1, checked against its official SHA-256 |
| `/etc/tender/agent.toml` | The agent's hub, machine name, backup paths and excludes |
| `/etc/tender/hub.toml` | The hub's address, database and email settings |
| `/etc/tender/credentials/` | Tokens, bucket keys and passwords, readable only by root and `tender` |
| `/var/lib/tender/agent/` | The agent's state and any reports waiting for the hub |
| `/var/lib/tender/hub/hub.db` | The hub's database |

Run `tender help` for every command, and `tender hub help` for the hub's.

## License

MIT. See [LICENSE](LICENSE).
