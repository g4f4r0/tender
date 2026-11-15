# Install

Install the hub on one machine and a daemon on every machine you want to back up and watch, then keep them updated or remove them.

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

Leave out the `--smtp-` options to start without email. Run the command again with them later; it keeps the key and every setting you don't pass. To install a specific release, put `TENDER_VERSION=v0.3.0` before `sh`.

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
| `TENDER_LISTEN` | `127.0.0.1:8080` | Where the hub listens. The `tenderhub` service may bind only port 8080, so change the address, not the port. |
| `TENDER_HOSTS` | None. | More names the hub answers to, comma-separated, such as `tender.example.com` behind your own proxy. |
| `TENDER_DATABASE` | `/var/lib/tender/hub/hub.db` | The hub's database. The `tenderhub` service can write only under `/var/lib/tender/hub`. |

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

   The installer installs `/usr/local/bin/tender`, then checks the token with the hub before it sets up anything else. It installs restic, creates the `tender` user, finds the filesystems to back up, writes `/etc/tender/daemon.toml` and starts the `tenderd` service. It prints:

   ```text
   The daemon is running. It gets its destination, schedule and keys from the hub. Next:
     1. Set when this machine backs up, from any machine that can reach the hub:
          tender backups set apps --destination DESTINATION --schedule "30 3 * * *" --retention-days 30
     2. Watch the daemon: journalctl -u tenderd -f
   ```

To install without a terminal, such as from a provisioning script, save the token in a file and pass `--token-file PATH`. The token never goes on the command line, where other users could see it.

## Docker and the journal

The daemon reaches Docker through `/var/run/docker.sock` as a member of the `docker` group, and reads the systemd journal as a member of the `systemd-journal` group. The installer adds each group to the `tenderd` service when the machine has it.

> [!WARNING]
> The `docker` group is root-equivalent: whoever can use Docker's socket controls the whole machine. Tender's daemon uses it only to list, start, stop and restart containers and to read their logs and usage. Anyone who can reach the hub can ask it to do the same, so keep the hub private.

The `systemd-journal` group can read every unit's logs, which can hold secrets. See [Security](security.md#logs-can-hold-secrets).

If you install Docker after Tender, add the group to the daemon:

```sh
sudo tender install refresh
```

```text
Restarted tenderd
```

## Update Tender

Update the hub first, then the daemons.

1. On each machine, run:

   ```sh
   sudo tender update
   ```

   ```text
   Updating Tender 0.2.2 to v0.3.0
   Downloaded Tender v0.3.0 and checked it against the release checksums
   Installed Tender v0.3.0 at /usr/local/bin/tender
   Restarted tenderd

   Updated to Tender v0.3.0.
   ```

`update` installs the latest release, then runs `tender install refresh` to rewrite and restart what Tender runs on the machine. Add `--version v0.3.0` for a specific release.

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
| `/usr/local/bin/restic` | restic 0.19.1, checked against its official SHA-256, on machines with a daemon. |
| `/etc/tender/hub.env` | The hub's encryption key and email settings, readable only by root. |
| `/etc/tender/daemon.toml` | The daemon's hub, machine name, backup paths and excludes. |
| `/etc/tender/credentials/hub-token` | The daemon's token, readable only by root and `tender`. |
| `/var/lib/tender/hub/hub.db` | The hub's database. |
| `/var/lib/tender/daemon/` | The daemon's state, its last settings and any reports waiting for the hub. |
| `/var/cache/tender/` | restic's cache for the daemon's backups. |

## Next steps

1. [Add a machine](guides/add-a-machine.md) to back up another server.
2. [Set up email alerts](guides/set-up-email-alerts.md).
3. [Security](security.md) explains what the hub and the daemon can reach.
