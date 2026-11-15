# Control a service

Restart, stop or start a Docker container on any machine, and read its logs, from wherever you run `tender`.

## Prerequisites

1. A daemon on the machine, reporting its services. `tender services list apps` lists them.
2. The `tender` command on a machine that can reach the hub. Off the hub machine, set `TENDER_HUB` or pass `--hub`.

## Restart a service

1. Find the service's name:

   ```sh
   tender services list apps
   ```

   ```text
   MACHINE   SERVICE   STATE     HEALTH    MEM               CPU     RESTARTS   STARTED      ALERTS
   apps      web       running   healthy   200 MiB / 1 GiB   12.5%   0          1 hour ago   on
   ```

2. Restart it:

   ```sh
   tender services restart apps web
   ```

   ```text
   Restarted web on apps.
   ```

The command waits up to 60 seconds for the daemon. If the daemon hasn't finished by then, the command prints the action's id and what happens next: a `pending` action runs if the daemon picks it up within 15 minutes of being asked, and an `in_progress` one fails if the daemon doesn't say how it ended within 15 minutes. Check on it with `tender actions show apps ID`.

## Stop and start a service

> [!WARNING]
> Stopping a service takes it offline until you start it again. Docker gives the container its own stop timeout to shut down, then kills it.

1. Stop it:

   ```sh
   tender services stop apps web
   ```

   ```text
   Stopped web on apps.
   ```

2. Check that it stopped:

   ```sh
   tender services list apps
   ```

   ```text
   MACHINE   SERVICE   STATE                   HEALTH    MEM   CPU   RESTARTS   STARTED      ALERTS
   apps      web       exited (0), by Tender   healthy   -     -     0          1 hour ago   on
   ```

   `by Tender` means a Tender stop left it stopped, so it doesn't alert as down.

3. Start it again:

   ```sh
   tender services start apps web
   ```

   ```text
   Started web on apps.
   ```

## Read a service's logs

1. Print the newest 200 lines from the last 10 minutes:

   ```sh
   tender services logs apps web --since 10m
   ```

   ```text
   2026-11-15T11:28:12.339011345Z web listening on :3000
   ```

`--lines` asks for up to 2000 lines. The output stops at 256 KiB and drops the oldest lines past it.

> [!WARNING]
> Logs can hold secrets. The hub clears them 1 hour after the action finished and never puts them in alerts or emails. See [Security](../security.md#logs-can-hold-secrets).

## Next steps

1. [Read a machine's logs](read-machine-logs.md) from its systemd journal.
2. [Services](../services.md) explains how actions reach a machine.
3. [CLI reference](../cli.md#tender-services) lists every option.
