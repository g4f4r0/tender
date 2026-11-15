# Services

Tender lists every Docker container on every machine, with its state, health, memory and CPU, and lets you start, stop and restart it and read its logs.

This page explains what the daemon reports and how actions reach a machine. To do it, see [Control a service](guides/control-a-service.md).

## What a service is

Each Docker container is a service, named by its container name, such as `web` or `shop-web-1` for a Compose service. The daemon reads them from Docker's API once a minute and sends the whole list to the hub, `[]` when there are none. A machine without Docker has no services; its backups and usage reports work the same.

For each service the hub keeps the latest report only:

| Field | What it holds |
| --- | --- |
| `state` | `created`, `running`, `paused`, `restarting`, `exited`, `removing` or `dead`, as Docker says. The hub sets `unknown` while Docker is unreachable, and `removed` once Docker no longer has the container. |
| `health` | `healthy`, `unhealthy` or `starting`, or `null` for a container without a health check. |
| `exit_code` | The exit code while the container is exited or dead, else `null`. It is `null` while Docker is unreachable too. |
| `restart_count` and `restart_policy` | How often Docker restarted it, and its policy: `no`, `always`, `unless-stopped` or `on-failure`. |
| `memory_bytes` | The memory it uses, less the page cache the kernel can drop, as `docker stats` shows it. |
| `memory_limit_bytes` | The container's memory limit, or `null` without one. |
| `cpu_percent` | Its CPU use since the previous report, where 100 is one whole CPU. |
| `alerts_enabled` | Whether the service alerts. It is `true` for every new service. |
| `stopped_by_tender` | `true` when a Tender stop left it stopped, so it doesn't alert as down. |
| `removed_at` | When the first report without the container arrived, while the state is `removed`, else `null`. |

Memory and CPU are `null` for a stopped container. CPU is also `null` on a service's first report, after the container restarts or is made again, and after the daemon itself restarts, because the daemon computes it from its own previous reading instead of waiting a second for a new one.

When the daemon can't read a running container's stats in time, it sends the container with `stats_unavailable: true` and no usage. Its usage is unknown this time, not gone, so the hub keeps the last `memory_bytes` and `memory_limit_bytes` while the `container_id` is the same, and `cpu_percent` is `null`.

`alerts_enabled` belongs to the service's name, so it stays when the container is made again under the same name.

### Removed services

When a report no longer has a container, the hub keeps the service with the state `removed` and sets `removed_at`. Its counts of reports in a row start again, so its open alerts resolve. A start, stop, restart or logs action on it gets `409` with `Service "web" was removed: Docker on this machine no longer has its container.` The lists show it as removed, with when.

A container back under the same name takes the service back, with its settings such as `alerts_enabled`. The hub deletes a service 7 days after `removed_at`.

## When Docker can't be reached

When the daemon can't reach Docker, it reports `{"docker": "unreachable"}` with no list and a `docker_reason`:

| `docker_reason` | What the daemon found |
| --- | --- |
| `socket_missing` | `/var/run/docker.sock` is gone, so Docker was stopped or removed. |
| `permission_denied` | The daemon may not use the socket, such as on a daemon before v0.3.0 when Docker was installed after Tender. Update the machine, and the daemon runs as root. See [The daemon runs as root](install.md#the-daemon-runs-as-root). |
| `refused` | The socket refuses connections, so Docker is stopped. |
| `timeout` | Docker didn't answer within 5 seconds. |
| `error` | Anything else. |

Until the daemon has seen Docker answer on this machine, only a socket that refuses connections or doesn't answer counts as unreachable; a missing socket or any other failure means the machine has no Docker, and the daemon reports nothing. The daemon remembers that it saw Docker in the file `docker-seen` in its state folder, `/var/lib/tender/daemon`, so a restart doesn't forget it.

The hub keeps the last list and sets the `state` of every service that isn't removed to `unknown`, with `exit_code`, memory and CPU `null`, until Docker answers again. `GET /v1/services`, `GET /v1/machines/{machine}/services` and `list_services` return `docker_reason`, which is `null` while Docker runs. [Alerts](alerts.md#service-alerts) explains the `docker_unreachable` alert.

## How actions reach a machine

The hub never connects to a machine. Each daemon keeps one request open to the hub and waits for work, so actions work through NAT and firewalls without opening a port.

1. You ask for an action, such as a restart, with the CLI, the API or MCP.
2. The hub queues it with the status `pending` and answers `202 Accepted`.
3. The daemon's open request gets the machine's oldest pending action, usually within a second. The action stays `pending`.
4. The daemon claims it with `PATCH {"status": "in_progress"}`, and runs it only when the hub answers `204`. The hub hands an action that isn't claimed within 30 seconds out again, and answers `409` to a claim of an action that isn't pending.
5. The daemon runs it through Docker's API or `journalctl`, one action at a time.
6. The daemon sends back `completed` or `failed`, with the logs or the reason.

The CLI and MCP wait up to 60 seconds for the result. A `pending` action fails if the daemon hasn't claimed it 15 minutes after it was asked for, and an `in_progress` one fails if the daemon hasn't said how it ended within 15 minutes. The hub keeps each machine's newest 100 finished actions, and never deletes a `pending` or `in_progress` one.

After a start, stop or restart, the daemon reports its containers at once, so the list shows the new state without waiting for the next minute.

### When the hub or the daemon restarts

The daemon retries a claim for up to 25 seconds with one `Idempotency-Key`, so a claim whose answer was lost gets the same answer again and the action runs once. If no answer comes, the daemon doesn't run the action and reports it failed, with `The daemon could not confirm with the hub that it may run this, so it did not run it.`

Each report waits in the daemon's state folder, as one JSON file per action under `/var/lib/tender/daemon/actions/`, readable only by the daemon. The daemon sends it again until the hub has it, across hub and daemon restarts. While reports wait, the daemon polls the hub without waiting and tries them again every minute.

A daemon restart can leave a report without its result. Logs never go to disk, so they are lost too:

1. A logs report that waited across a daemon restart is sent as failed, with `The daemon restarted before the hub had these logs, and it never keeps logs on disk. Ask for them again.`
2. An action the daemon was running when it stopped is sent as failed, with `The daemon stopped while it ran this, so it can't say how it ended.`

A report that arrives after the hub failed the action with `the daemon started this but did not say how it ended; is the machine online?` replaces that failure. A late completed stop still marks the service `stopped_by_tender`.

## Logs

There are two kinds of logs:

1. A service's logs, which the daemon reads from Docker.
2. The machine's systemd journal, which the daemon reads with `journalctl`. Filter it by unit, priority and time.

Both return the newest entries, up to 2000 lines and 256 KiB. When the output is larger, the oldest lines are dropped.

> [!WARNING]
> Logs can hold secrets, such as tokens an application prints at startup. The hub clears the output of every completed logs action 1 hour after it finishes. See [Security](security.md#logs-can-hold-secrets).

## Docker and journal access

The daemon runs as root, so it reaches Docker's socket and reads every unit's journal. [Security](security.md#what-the-daemon-can-do) explains what its sandbox allows.

## Next steps

1. [Control a service](guides/control-a-service.md) to restart a container or read its logs.
2. [Read a machine's logs](guides/read-machine-logs.md) to search the journal.
3. [Alerts](alerts.md) explains when a service alerts.
