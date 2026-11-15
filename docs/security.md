# Security

Learn who can reach the hub, how Tender stores your keys and passwords, and what the daemon can do on each machine.

## The hub has no login

The hub has no login in this version. Anyone who can reach it can add, change and remove machines, destinations, log sinks and alert recipients, stop and restart services, and read service and journal logs. Keep it on your tailnet or behind your own access control, and never expose it to the internet.

The installer publishes the hub on your tailnet with Tailscale Serve, so only devices on your tailnet reach it. The hub itself listens on `127.0.0.1:8080` only.

Web pages can't use the hub through your browser:

1. The hub refuses a browser request from another website that would change something, by its `Origin` and `Sec-Fetch-Site` headers. It sends no CORS headers, so such a page can't read its answers either.
2. It answers only to a `Host` that is an IP address, a name without a dot such as `localhost`, a Tailscale name under `.ts.net`, or a name in `TENDER_HOSTS`. Any other name, as a DNS rebinding attack uses, gets `403`.

## How Tender stores secrets

The hub keeps destination keys and repository passwords in its SQLite database, encrypted with AES-256-GCM under `TENDER_ENCRYPTION_KEY` from `/etc/tender/hub.env`. The hub refuses to start with a key that doesn't open them.

The hub sends a destination's keys and password only to a machine's daemon, signed in with its own token: the daemon of a machine that backs up to that destination, or of the machine you named in a [destination test](backups.md#test-a-destination), with that test. No other route, MCP tool, CLI command or log line shows them. The SFTP key and the rclone remotes stay on each machine and never reach the hub.

Back up `/etc/tender/hub.env`. Without its key, nobody can read the secrets in the database, and the hub won't start while it stores any destination or log sink.

## Log sink secrets

A log sink's secrets are an OTLP sink's `headers`, a webhook sink's signing `secret`, and the query string of either one's URL, which can carry a key and shows as `?***`. The hub stores them in its database encrypted under the same key, and every route, MCP tool and CLI command shows them as `"***"`. The only time one is shown is a webhook secret the hub made, once, in the answer that added the sink.

The hub sends a sink's whole config, secrets included, only to the daemons of the machines the sink applies to, signed in with their own tokens. Each daemon keeps the list in `/var/lib/tender/daemon/logs/sinks.json`, which only the daemon's user can read, so it can go on sending while the hub is down. `last_error` never holds a sink's answer or its URL.

## Machine tokens

Each daemon signs in with its own token. The hub stores only a SHA-256 hash of it. A token works only for its own machine: it can't report for another machine or get another machine's keys.

The daemon reads its token from `/etc/tender/credentials/hub-token`, readable only by root and the `tender` user. If a token leaks, rotate it:

```sh
tender machines rotate-token apps
```

The old token stops working at once, so the daemon stays offline and stops backing up until it has the new one. Save the new token in a file on the machine, then install it:

```sh
sudo tender install daemon --token-file /root/apps-token
```

## What the daemon can do

The daemon runs as the `tender` system user under a systemd sandbox:

1. It can read every file on the machine through the `CAP_DAC_READ_SEARCH` capability, so it can back them up.
2. It can write only to its own folder, `/var/lib/tender/daemon`, and to restic's cache, `/var/cache/tender`.
3. On a machine with Docker, it is in the `docker` group, so it can list, start, stop and restart containers and read their logs.
4. On a machine with systemd's journal, it is in the `systemd-journal` group, so it can read every unit's logs.
5. It may lower a process's CPU and IO priority, which it does for restic, but without `CAP_SYS_NICE` it can't raise one. [Backups](backups.md#destinations-settings-and-runs) explains why.

> [!WARNING]
> The `docker` group is root-equivalent: whoever can use Docker's socket controls the whole machine. The daemon uses it only for the things above, but anyone who can reach the hub can ask it to. That is one more reason to keep the hub private.

The daemon keeps the last settings it got from the hub, readable only by itself, so backups go on when the hub is unreachable.

## Logs can hold secrets

Service logs and the journal can hold tokens, passwords or customer data that an application printed. Tender treats them that way:

1. Logs come back only as the output of the logs action that asked for them. Like every route, that output is open to anyone who can reach the hub.
2. The hub clears the output of every completed logs action 1 hour after it finishes. A failed logs action holds why it failed, not logs, so it keeps its output.
3. Cleared logs leave no bytes in the hub's database files, `hub.db`, `hub.db-wal` and `hub.db-shm`. The hub runs SQLite with `secure_delete`, which zeroes what a write frees, and after a pass that clears logs it empties the write-ahead log with `wal_checkpoint(TRUNCATE)`. A checkpoint that a reader holds up is tried again by the next minute's pass.
4. The daemon writes logs to disk only to send them to a [log sink](log-shipping.md): each sink's batches wait in `/var/lib/tender/daemon/logs/`, a folder only the daemon's user can read, up to 64 MiB, until the sink takes them. A report waiting for the hub keeps its logs in memory only, as [Services](services.md#when-the-hub-or-the-daemon-restarts) explains.
5. Logs never go into the hub's or the daemon's own logs, into alerts or into emails. Logs sent to a sink go from the daemon straight to it and never reach the hub.

An AI client that reads logs sends them to its model provider. Ask for the narrowest logs you need, with `--unit`, `--priority` and `--since`.

## Keys you give an AI client

The MCP tools and routes never return destination keys, passwords or log sink secrets; a secret shows as `"***"`. Keys you pass to `add_destination` or `add_log_sink` go through the AI conversation, though. For real keys, `tender destinations add` is safer: it asks for them with typing hidden. `tender sinks add` reads them from files.

## Next steps

1. [Install](install.md) sets up the hub privately on your tailnet.
2. [Backups](backups.md) explains how the repository password protects your data.
