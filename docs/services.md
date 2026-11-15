# Services

Tender lists every Docker container on every machine, with its state, health, memory and CPU, and lets you start, stop and restart it and read its logs.

This page explains what the daemon reports and how actions reach a machine. To do it, see [Control a service](guides/control-a-service.md).

## What a service is

Each Docker container is a service, named by its container name, such as `web` or `shop-web-1` for a Compose service. The daemon reads them from Docker's API once a minute and sends the whole list to the hub. A machine without Docker has no services and works as before.

For each service the hub keeps the latest report only:

| Field | What it holds |
| --- | --- |
| `state` | `created`, `running`, `paused`, `restarting`, `exited`, `removing` or `dead`, as Docker says, or `unknown` while Docker is unreachable. |
| `health` | `healthy`, `unhealthy` or `starting`, or `null` for a container without a health check. |
| `exit_code` | The exit code while the container is exited or dead. |
| `restart_count` and `restart_policy` | How often Docker restarted it, and its policy: `no`, `always`, `unless-stopped` or `on-failure`. |
| `memory_bytes` | The memory it uses, less the page cache the kernel can drop, as `docker stats` shows it. |
| `memory_limit_bytes` | The container's memory limit, or `null` without one. |
| `cpu_percent` | Its CPU use since the previous report, where 100 is one whole CPU. |
| `stopped_by_tender` | `true` when a Tender stop left it stopped, so it doesn't alert as down. |

Memory and CPU are `null` for a stopped container, and when Docker didn't answer in time. CPU is also `null` on a service's first report and after it restarts or its container is made again, because the daemon computes it from its own previous reading instead of waiting a second for a new one.

## How actions reach a machine

The hub never connects to a machine. Each daemon keeps one request open to the hub and waits for work, so actions work through NAT and firewalls without opening a port.

1. You ask for an action, such as a restart, with the CLI, the API or MCP.
2. The hub queues it with the status `pending` and answers `202 Accepted`.
3. The machine's daemon picks it up within a second, marks it `in_progress` and runs it through Docker's API or `journalctl`.
4. The daemon sends back `completed` or `failed`, with the logs or the reason.

The CLI and MCP wait up to 60 seconds for the result. An action the daemon hasn't picked up stays queued for 15 minutes, then fails. The hub keeps each machine's last 100 actions.

After a start, stop or restart, the daemon reports its containers at once, so the list shows the new state without waiting for the next minute.

## Logs

There are two kinds of logs:

1. A service's logs, which the daemon reads from Docker.
2. The machine's systemd journal, which the daemon reads with `journalctl`. Filter it by unit, priority and time.

Both return the newest entries, up to 2000 lines and 256 KiB. When the output is larger, the oldest lines are dropped.

> [!WARNING]
> Logs can hold secrets, such as tokens an application prints at startup. Tender shows logs only to the person or AI client that asked for them. The hub clears the output of every logs action 1 hour after it finishes and keeps only the row that says it ran. Logs never appear in the hub's or the daemon's own logs, in alerts or in emails.

## Docker and journal access

The daemon reaches Docker through `/var/run/docker.sock` as a member of the `docker` group, and reads the journal as a member of the `systemd-journal` group. The installer adds each group to the daemon's service when the machine has it. See [Install](install.md#docker-and-the-journal) for what each group allows.

## Next steps

1. [Control a service](guides/control-a-service.md) to restart a container or read its logs.
2. [Read a machine's logs](guides/read-machine-logs.md) to search the journal.
3. [Alerts](alerts.md) explains when a service alerts.
