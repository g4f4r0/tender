# Security

Learn who can reach the hub, how Tender stores your keys and passwords, what the daemon can do on each machine, and what it checks.

## The hub has no login

The hub has no login in this version. Anyone who can reach it can add, change and remove machines, destinations, log sinks and notification channels, stop and restart services, read service and journal logs, and read the metrics at `/metrics`. Keep it on your tailnet or behind your own access control, and never expose it to the internet.

The installer publishes the hub on your tailnet with Tailscale Serve, so only devices on your tailnet reach it. The hub itself listens on `127.0.0.1:8080` only.

Web pages can't use the hub through your browser:

1. The hub refuses a browser request from another website that would change something, by its `Origin` and `Sec-Fetch-Site` headers. It sends no CORS headers, so such a page can't read its answers either.
2. It answers only to a `Host` that is an IP address, a name without a dot such as `localhost`, a Tailscale name under `.ts.net`, or a name in `TENDER_HOSTS`. Any other name, as a DNS rebinding attack uses, gets `403`.

## How Tender stores secrets

The hub keeps destination keys, repository passwords and the secrets of notification channels, such as a Slack webhook URL or a webhook's signing secret, in its SQLite database, encrypted with AES-256-GCM under `TENDER_ENCRYPTION_KEY` from `/etc/tender/hub.env`. The hub refuses to start with a key that doesn't open them.

A channel's secrets never come back out: every route, tool and command shows them as `"***"` with a `<field>_set` field. A webhook's signing secret is shown once, when the hub makes it. A failed delivery's error never holds the URL it was sent to.

The hub sends a destination's keys and password only to a machine's daemon, signed in with its own token: the daemon of a machine that backs up to that destination, or of the machine you named in a [destination test](backups.md#test-a-destination), with that test. No other route, MCP tool, CLI command or log line shows them. The SFTP key and the rclone remotes stay on each machine and never reach the hub.

Back up `/etc/tender/hub.env`. Without its key, nobody can read the secrets in the database, and the hub won't start while it stores any destination, log sink or channel secret.

## Log sink secrets

A log sink's secrets are an OTLP sink's `headers`, a webhook sink's signing `secret`, and the query string of either one's URL, which can carry a key and shows as `?***`. The hub stores them in its database encrypted under the same key, and every route, MCP tool and CLI command shows them as `"***"`. The only time one is shown is a webhook secret the hub made, once, in the answer that added the sink.

The hub sends a sink's whole config, secrets included, only to the daemons of the machines the sink applies to, signed in with their own tokens. Each daemon keeps the list in `/var/lib/tender/daemon/logs/sinks.json`, which only the daemon's user can read, so it can go on sending while the hub is down. `last_error` never holds a sink's answer or its URL.

## Machine tokens

Each daemon signs in with its own token. The hub stores only a SHA-256 hash of it. A token works only for its own machine: it can't report for another machine or get another machine's keys.

The daemon reads its token from `/etc/tender/credentials/hub-token`, readable only by root and the `tender` group. If a token leaks, rotate it:

```sh
tender machines rotate-token apps
```

The old token stops working at once, so the daemon stays offline and stops backing up until it has the new one. Save the new token in a file on the machine, then install it:

```sh
sudo tender install daemon --token-file /root/apps-token
```

## What the daemon can do

The daemon, `tenderd`, runs as root. It backs up every file, controls Docker and reads every unit's journal, which made it root-equivalent even as the `tender` user, so Tender says so plainly. The hub, the part on the network, stays the unprivileged `tender` user.

A systemd sandbox keeps a bug in the daemon from changing the system:

1. It holds three capabilities only. `CAP_DAC_READ_SEARCH` reads every file, for backups. `CAP_NET_ADMIN` reads the firewall's rules, and `CAP_SYS_PTRACE` names the process behind each listening socket, for the security checks. Without `CAP_DAC_OVERRIDE` it can't write another user's files.
2. With `ProtectSystem=strict`, it can write only its own folder, `/var/lib/tender/daemon`, and restic's cache, `/var/cache/tender`.
3. `NoNewPrivileges`, the `ProtectKernel` settings and a system call filter keep it from loading modules, changing the clock or gaining privileges.
4. It may lower a process's CPU and IO priority, which it does for restic, but without `CAP_SYS_NICE` it can't raise one. [Backups](backups.md#destinations-settings-and-runs) explains why.

Two jobs must change the whole system, so they run outside the sandbox, as transient units that systemd starts through `systemd-run`. The daemon runs it without `--wait` or `--pipe`, so as root it talks to systemd over its private socket, `/run/systemd/private`, where systemd trusts root by its user id. That needs no D-Bus, no polkit and no `CAP_SYS_ADMIN`. Each job writes its output to a file in `/var/lib/tender/daemon`, and the daemon asks `systemctl show` how it ended:

1. `tenderupdate` installs a Tender release, as [Updates](updates.md#how-an-update-works) explains. It takes only a version, and installs only a release signed with Tender's key.
2. `tenderupgrade` upgrades the system's packages when someone runs `tender machines upgrade`. Its output is `tenderupgrade.log`. Nothing pipes it, so a daemon that restarts never stops apt in the middle: the upgrade runs on, and the next daemon waits for it and reports it instead of starting another.

> [!WARNING]
> Anyone who can reach the hub can ask a daemon to stop a service, read logs, upgrade packages or update Tender. That is one more reason to keep the hub private.

The daemon keeps the last settings it got from the hub, readable only by root, so backups go on when the hub is unreachable.

## Signed releases

Every update checks that the release's `SHA256SUMS` carries an ed25519 signature from a key built into Tender, then checks the binary against `SHA256SUMS`. A release someone changed on GitHub fails the signature and never installs. Updates also refuse prereleases, drafts and older versions. [Updates](updates.md#signed-releases) has the key.

Tender v0.2.2 and earlier can't check signatures, so the one `sudo tender update` from them to v0.3.0 checks only the release's checksums. Every update after it checks the signature.

## Security checks

The daemon checks its machine when it starts and every 15 minutes, and reports what it found to the hub. It only reads, and changes nothing. The checks run on Debian and Ubuntu; on any other system each one says `unsupported` and never alerts.

| Check | What it reads | Alert |
| --- | --- | --- |
| Firewall | `ufw status verbose`, or without ufw, the input chains of `nft -j list chains`, or `iptables -S INPUT`. | `security.firewall_off` when no active firewall denies incoming traffic by default. |
| Listeners | `ss -lntup`: every TCP or UDP socket on an address other than loopback, link-local or the tailnet (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`). Docker's published ports are flagged, because they go around ufw. | `security.port_exposed`, one per port, unless the port is on the machine's allowlist. |
| SSH | `PasswordAuthentication` and `PermitRootLogin` from `sshd -T`. | `security.ssh_weak` when `PasswordAuthentication` is `yes`. |
| Updates | A simulated `apt-get upgrade --with-new-pkgs`, what `tender machines upgrade --all-packages` would install, counting the updates from a security repository, and whether unattended-upgrades is on. | `security.updates_pending` after 3 days, `security.auto_updates_off`. |
| Reboot | `/var/run/reboot-required` and its age. | `security.reboot_required` after 7 days. |

See the latest checks of every machine, or one in full:

```sh
tender security apps
```

```text
Security checks of apps, collected 4 minutes ago.

Firewall: ufw, incoming deny
SSH: keys only
Updates: 3 pending, 1 security
Security updates pending since 2 hours ago.
Reboot: not needed

Listening on a public address:
PORT        ADDRESS   PROCESS     NOTE
41641/udp   0.0.0.0   tailscaled  allowed
8080/tcp    0.0.0.0   -           published by Docker for web, around ufw

Allow an expected port with: tender machines set apps --allow-port PORT
```

Allow the ports that should be reachable, such as Tailscale's:

```sh
tender machines set apps --allow-port 41641/udp
```

```text
apps: updates inherit, allowed ports 41641/udp, security alerts on.
```

Take one off with `--deny-port`, and turn a machine's security notifications off with `--security-alerts off`. [Notifications](notifications.md#security-events) explains when each event fires.

## Upgrade a machine's packages

Install the pending security updates, through the machine's daemon:

```sh
tender machines upgrade apps
```

```text
Upgrading the security packages on apps. This can take a few minutes.
...
Upgraded the security packages on apps.
```

It runs `apt-get update`, then `unattended-upgrade -v`, in the `tenderupgrade` unit. With `--all-packages` it runs `apt-get upgrade --with-new-pkgs` instead, which installs new packages such as a kernel but never removes one, non-interactive and keeping the config files you changed. A service may restart when its package upgrades. Tender never reboots a machine: when an upgrade needs one, `tender security` and the `security.reboot_required` alert say so, and you reboot when it suits you.

## What an exec adapter can do

An exec channel runs a program on the hub for each notification, as [Adapters](adapters.md#write-an-exec-adapter) explains. The API can only name a program in `/etc/tender/adapters`, never give a path or arguments:

1. The hub runs a program only when it is a file in `/etc/tender/adapters`, and the folder and the file are owned by root and not writable by their group or others. Only root on the hub can install or change one. `TENDER_TEST_ADAPTERS_DIR` points the hub at another folder whose owner need not be root, for tests only; the hub logs a warning at startup while it is set.
2. It runs as the hub's `tender` user, inside the hub's systemd sandbox, with no capabilities and no way to gain privileges.
3. Its environment holds only `PATH` and `LANG`: the hub doesn't pass it `TENDER_ENCRYPTION_KEY`, the SMTP password or any other setting.
4. It has 10 seconds per notification, after which the hub kills it and every process it started. Once it exits, the hub stops anything it left running too. The hub keeps at most 64 KiB of its output.

> [!WARNING]
> An exec adapter runs as the hub's own user, so it can read whatever the hub can, such as its database and the hub process's own environment, which holds the encryption key. Install only programs you trust, as you would for root.

## Logs can hold secrets

Service logs and the journal can hold tokens, passwords or customer data that an application printed. Tender treats them that way:

1. Logs come back only as the output of the logs action that asked for them. Like every route, that output is open to anyone who can reach the hub.
2. The hub clears the output of every completed logs action 1 hour after it finishes. A failed logs action holds why it failed, not logs, so it keeps its output.
3. Cleared logs leave no bytes in the hub's database files, `hub.db`, `hub.db-wal` and `hub.db-shm`. The hub runs SQLite with `secure_delete`, which zeroes what a write frees, and after a pass that clears logs it empties the write-ahead log with `wal_checkpoint(TRUNCATE)`. A checkpoint that a reader holds up is tried again by the next minute's pass.
4. The daemon writes logs to disk only to send them to a [log sink](log-shipping.md): each sink's batches wait in `/var/lib/tender/daemon/logs/`, a folder only the daemon's user can read, up to 64 MiB, until the sink takes them. A report waiting for the hub keeps its logs in memory only, as [Services](services.md#when-the-hub-or-the-daemon-restarts) explains.
5. Logs never go into the hub's or the daemon's own logs, into notifications, or into the events that channels get. Logs sent to a sink go from the daemon straight to it and never reach the hub.

An AI client that reads logs sends them to its model provider. Ask for the narrowest logs you need, with `--unit`, `--priority` and `--since`.

## Keys you give an AI client

The MCP tools and routes never return destination keys, passwords or log sink secrets; a secret shows as `"***"`. Keys you pass to `add_destination` or `add_log_sink` go through the AI conversation, though. For real keys, `tender destinations add` is safer: it asks for them with typing hidden. `tender sinks add` reads them from files.

## Next steps

1. [Install](install.md) sets up the hub privately on your tailnet.
2. [Updates](updates.md) explains how Tender updates itself.
3. [Notifications](notifications.md#security-events) explains the security events.
4. [Backups](backups.md) explains how the repository password protects your data.
