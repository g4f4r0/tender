# Control a service

Restart, stop or start a service, or one of its containers, on any machine, and read its logs, from wherever you run `tender`.

## Prerequisites

1. A daemon on the machine, reporting its services. `tender services list apps` lists them.
2. The `tender` command on a machine that can reach the hub, with your API token in `TENDER_TOKEN`. Off the hub machine, set `TENDER_HUB` or pass `--hub`.

## Restart a service

1. Find the service's name:

   ```sh
   tender services list apps
   ```

   ```text
   MACHINE   SERVICE   STATE     RUNNING   MEM       CPU     DEPLOY     ALERTS
   apps      notes     running   2/2       1.7 GiB   15.0%   deployed   on
   apps      redis     running   1/1       200 MiB   12.5%   -          on
   ```

2. See its containers:

   ```sh
   tender services show apps notes
   ```

   The last table lists each container by its name in the service, such as `web` and `worker`.

3. Restart the whole service, or one container with `--container`:

   ```sh
   tender services restart apps notes --container web
   ```

   ```text
   Restarted web in notes on apps.
   ```

The command waits up to 60 seconds for the daemon. If the daemon hasn't finished by then, the command prints the action's id and what happens next: a `pending` action runs if the daemon picks it up within 15 minutes of being asked, and an `in_progress` one fails if the daemon doesn't say how it ended within 15 minutes. Check on it with `tender actions show apps ID`.

## Stop and start a service

> [!WARNING]
> Stopping a service takes it offline until you start it again. Docker gives each container its own stop timeout to shut down, then kills it.

1. Stop it:

   ```sh
   tender services stop apps notes
   ```

   ```text
   Stopped notes on apps.
   ```

2. Check that it stopped:

   ```sh
   tender services show apps notes
   ```

   ```text
   Service notes on apps: stopped.
   ...
   CONTAINER   IMAGE                STATE                   HEALTH   MEM   CPU   RESTARTS   STARTED
   web         ghcr.io/me/notes:1   exited (0), by Tender   -        -     -     0          1 hour ago
   worker      ghcr.io/me/notes:1   exited (0), by Tender   -        -     -     0          1 hour ago
   ```

   `by Tender` means a Tender stop left the container stopped, so it doesn't alert as down.

3. Start it again:

   ```sh
   tender services start apps notes
   ```

   ```text
   Started notes on apps.
   ```

## Read a service's logs

1. Print the newest 200 lines from the last 10 minutes, of every container of the service:

   ```sh
   tender services logs apps notes --since 10m
   ```

   ```text
   2026-11-15T11:28:12.120541873Z worker | job 41 done
   2026-11-15T11:28:12.339011345Z web | listening on :8080
   ```

   Each line has its container's name after the timestamp. `--container web` prints only that container's lines, without the name.

`--lines` asks for up to 2000 lines. The output stops at 256 KiB and drops the oldest lines past it.

> [!WARNING]
> Logs can hold secrets. The hub clears them 1 hour after the action finished and never puts them in alerts or emails. See [Security](../security.md#logs-can-hold-secrets).

## Next steps

1. [Services](../services.md#deploy-a-service) explains how to deploy a service from a compose file.
2. [Read a machine's logs](read-machine-logs.md) from its systemd journal.
3. [CLI reference](../cli.md#tender-services) lists every option.
