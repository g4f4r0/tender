# Install

Install the hub on one machine and a daemon on every machine you want to back up and watch, then update them or remove them.

The [quickstart](quickstart.md) does all of this for one machine. This page covers each part in full.

## Prerequisites

1. Linux on x86_64 with systemd, and root on each machine through `sudo`.
2. A bucket on any S3-compatible storage, such as Cloudflare R2, AWS S3, Backblaze B2, Wasabi or MinIO, with an access key that can read and write it.
3. [Tailscale](https://tailscale.com) on every machine is recommended. The installer uses it to publish the hub privately on your tailnet. Without it, put your own proxy and access control in front of the hub.

## Install the hub

1. On the machine that will run the hub, run the installer with your SMTP server:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- hub \
     --smtp-host smtp.example.com --smtp-from "Tender <alerts@example.com>" --smtp-username alerts@example.com
   ```

   The installer downloads the latest release, checks it against the release's `SHA256SUMS` and installs `/usr/local/bin/tender`. It asks for the SMTP password with typing hidden. It writes `/etc/tender/hub.env` with a new encryption key, starts the `tenderhub` service and publishes the hub on your tailnet. It prints the hub's address:

   ```text
   The hub is running at https://apps.your-tailnet.ts.net.
   The tender command on this machine finds it by itself.
   Anywhere else, set TENDER_HUB=https://apps.your-tailnet.ts.net or pass --hub https://apps.your-tailnet.ts.net.
   ```

2. Print `/etc/tender/hub.env` and save it in your password manager or another safe place:

   ```sh
   sudo cat /etc/tender/hub.env
   ```

   Its key decrypts the bucket keys and passwords the hub stores. The hub won't start without the same key.

Leave out the `--smtp-` options to start without email. Run the command again with them later; it keeps the key and every setting you don't pass, and removes one you pass empty, as [`tender install hub`](cli.md#tender-install-hub) explains. To install a specific release, put `TENDER_VERSION=v0.3.0` before `sh`.

### Hub settings

The hub reads its settings from `/etc/tender/hub.env`. After editing the file by hand, run `sudo systemctl restart tenderhub`.

| Variable | Default | What it sets |
| --- | --- | --- |
| `TENDER_ENCRYPTION_KEY` | Made at install. | The key that encrypts the secrets the hub stores. Required. |
| `TENDER_SMTP_HOST` | None. | The SMTP server. Email is off without it. |
| `TENDER_SMTP_PORT` | `465` | The SMTP port, with implicit TLS. |
| `TENDER_SMTP_USERNAME` | None. | The SMTP login, for servers that ask for one. |
| `TENDER_SMTP_PASSWORD` | None. | The SMTP password. |
| `TENDER_SMTP_FROM` | None. | The sender, such as `Tender <alerts@example.com>`. |
| `TENDER_LISTEN` | `127.0.0.1:8080` | Where the hub listens, as an address with a port. The installer writes the port into the `tenderhub` service as `SocketBindAllow=tcp:PORT`, the only port the hub may bind, so run `sudo tender install refresh` after changing it. The installer stops before changing anything when the address has no port. |
| `TENDER_HOSTS` | None. | More names the hub answers to, comma-separated, such as `tender.example.com` behind your own proxy. |
| `TENDER_DATABASE` | `/var/lib/tender/hub/hub.db` | The hub's database. The `tenderhub` service can write only under `/var/lib/tender/hub`, and the hub refuses at startup a database path it can't write. Keep the database there, or allow its folder with `ReadWritePaths=` in a drop-in made with `sudo systemctl edit tenderhub`. |

## Add a machine and its token

Each daemon signs in to the hub with its own token. Make one from any machine that can reach the hub:

```sh
tender machines add apps
```

```text
Added machine apps.

Its hub token, shown only now. Save it before you go on:
  <token>

Install the daemon on that machine; it asks for the token:
  curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh \
    | sudo sh -s -- daemon --hub HUB_URL --machine apps
```

Keep the token for the next step. If you lose it, run `tender machines rotate-token apps` for a new one.

## Install the daemon

1. On the machine you named, run the installer with the hub's address and the machine's name:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- daemon \
     --hub https://apps.your-tailnet.ts.net --machine apps
   ```

2. Paste the token when it asks. Typing is hidden.

   The installer installs `/usr/local/bin/tender`, then checks the token with the hub before it sets up anything else. It installs restic, finds the filesystems to back up, writes `/etc/tender/daemon.toml` and starts the `tenderd` service as root. It prints:

   ```text
   The daemon is running. It gets its destination, schedule and keys from the hub. Next:
     1. Set when this machine backs up, from any machine that can reach the hub:
          tender backups set apps --destination DESTINATION --schedule "30 3 * * *" --retention-days 30
     2. Watch the daemon: journalctl -u tenderd -f
   ```

To install without a terminal, such as from a provisioning script, save the token in a file and pass `--token-file PATH`. The token never goes on the command line, where other users could see it.

## The daemon runs as root

The daemon, `tenderd`, runs as root, so it can back up every file, control Docker, read the journal and check the machine's security. A systemd sandbox lets it write only its own folders, and holds it to three capabilities. [Security](security.md#what-the-daemon-can-do) explains what it can do. The hub stays the unprivileged `tender` user.

The daemon reaches Docker at `/var/run/docker.sock`. If you install Docker after Tender, nothing needs to change: the daemon finds it within a minute.

> [!WARNING]
> Whoever can reach the hub can ask a daemon to stop a service, read logs, upgrade packages or update Tender. Keep the hub private.

## Update Tender

After v0.3.0, Tender updates itself from the hub, one machine at a time, with no sudo. By default the hub rolls out each new stable release by itself. To update now, from any machine that can reach the hub:

```sh
tender update --all
```

```text
Started rollout 3 of Tender 0.3.1, one machine at a time: db, apps.
Follow it with: tender updates show
```

[Updates](updates.md) explains auto and manual mode, the window, and what happens when an update fails.

On a machine, root can still update it by hand, and only this way go back to an older release:

```sh
sudo tender update --version 0.3.0
```

### Update to v0.3.0

A machine on v0.2.2 or earlier needs one update by hand, the last one that needs sudo. Update the hub's machine first, because a newer hub takes the reports of older daemons, then each other machine.

1. On each machine, run:

   ```sh
   sudo tender update
   ```

   ```text
   Updating Tender 0.2.2 to v0.3.0
   Downloaded Tender v0.3.0 and checked it against the release checksums
   Installed Tender v0.3.0 at /usr/local/bin/tender
   Gave /var/lib/tender/daemon to root, which the daemon runs as now
   Gave /var/cache/tender to root, which the daemon runs as now
   Took the tender user out of the docker group, which only the daemon needed
   Took the tender user out of the systemd-journal group, which only the daemon needed
   Restarted tenderd

   Updated to Tender v0.3.0.
   ```

Tender v0.2.2 can't check release signatures, so this update checks only the release's `SHA256SUMS`. Every update after it checks the signature too, as [Signed releases](security.md#signed-releases) explains.

The new binary's `tender install refresh` moves the daemon to root: it rewrites the `tenderd` unit, gives the daemon's state and cache folders to root, and takes the `tender` user out of the `docker` and `systemd-journal` groups. Run it yourself to repair an install:

```sh
sudo tender install refresh
```

## Uninstall

> [!WARNING]
> These steps delete the hub's database, with every machine, setting and history it holds, and the daemon's saved settings. They don't delete the backups in your bucket.

1. Remove the machine from the hub, from any machine that can reach it:

   ```sh
   tender machines remove apps --yes
   ```

   ```text
   Removed machine apps and its history. Its backups stay in the destination.
   ```

2. On each machine with a daemon, stop it and delete its files, restic and its cache:

   ```sh
   sudo systemctl disable --now tenderd
   sudo rm -rf /etc/systemd/system/tenderd.service /etc/tender/daemon.toml /etc/tender/credentials /var/lib/tender/daemon /var/cache/tender /usr/local/bin/restic
   sudo systemctl daemon-reload
   ```

3. On the hub machine, stop the hub, stop publishing it and delete its files:

   ```sh
   sudo systemctl disable --now tenderhub
   sudo tailscale serve --https=443 off
   sudo rm -rf /etc/systemd/system/tenderhub.service /etc/tender/hub.env /var/lib/tender/hub
   sudo systemctl daemon-reload
   ```

4. On each machine, once neither service runs there, delete the `tender` user, the binary and the empty folders:

   ```sh
   sudo userdel tender
   sudo rm -rf /usr/local/bin/tender /etc/tender /var/lib/tender
   ```

## Where things live

| Path | What it holds |
| --- | --- |
| `/usr/local/bin/tender` | The Tender binary. |
| `/usr/local/bin/tender.previous` | The binary before the last update, which a failed update puts back. |
| `/usr/local/bin/restic` | restic 0.19.1, checked against its official SHA-256, on machines with a daemon. |
| `/etc/tender/hub.env` | The hub's encryption key and email settings, readable only by root. |
| `/etc/tender/daemon.toml` | The daemon's hub, machine name, backup paths and excludes. |
| `/etc/tender/credentials/hub-token` | The daemon's token, readable only by root and the `tender` group. |
| `/var/lib/tender/hub/hub.db` | The hub's database. |
| `/var/lib/tender/daemon/` | The daemon's state, readable only by root: its last settings, any reports waiting for the hub, and an update's result. |
| `/var/cache/tender/` | restic's cache for the daemon's backups. |

## Next steps

1. [Add a machine](guides/add-a-machine.md) to back up another server.
2. [Set up email alerts](guides/set-up-email-alerts.md).
3. [Updates](updates.md) explains how Tender updates itself.
4. [Security](security.md) explains what the hub and the daemon can reach.
