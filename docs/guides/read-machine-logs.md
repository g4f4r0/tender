# Read a machine's logs

Print the newest entries of a machine's systemd journal, filtered by unit, priority and time, without logging in to the machine.

## Prerequisites

1. A daemon on the machine, installed or updated with Tender v0.3.0 or later, so it is in the `systemd-journal` group.
2. The `tender` command on a machine that can reach the hub.

## Read the journal

1. Print the newest 200 entries:

   ```sh
   tender logs apps
   ```

   Each line has the time in UTC, the unit and the message, the newest last:

   ```text
   2026-11-15T11:26:13.000000000Z tenderd.service Reported to the hub.
   2026-11-15T11:27:13.000000000Z tenderd.service backup took 41s, longer than usual.
   2026-11-15T11:28:13.000000000Z kernel eth0: link up
   ```

2. Narrow it to one unit's warnings and worse from the last hour:

   ```sh
   tender logs apps --unit tenderd --priority warning --since 1h
   ```

   ```text
   2026-11-15T11:27:13.000000000Z tenderd.service backup took 41s, longer than usual.
   ```

`--lines` sets how many entries, up to 2000, and `--since` goes back at most `7d`. The output stops at 256 KiB and drops the oldest lines past it. The [CLI reference](../cli.md#tender-logs) lists every priority and option.

## Read it again later

The hub keeps each read as an action, and keeps its output for 1 hour after it finished.

1. List the machine's actions:

   ```sh
   tender actions list apps
   ```

   ```text
   ID   TYPE      OF                                                  STATUS      ASKED
   6    logs      journal, unit tenderd, priority warning, since 1h   completed   just now
   1    restart   web                                                 completed   just now
   ```

2. Show one with its output:

   ```sh
   tender actions show apps 6
   ```

   ```text
   Action 6 on apps: logs of journal, unit tenderd, priority warning, since 1h, completed, asked just now.

   2026-11-15T11:27:13.000000000Z tenderd.service backup took 41s, longer than usual.
   ```

After that hour, `actions show` says the logs were cleared. The action stays in the list, so you can still see what was read and when.

> [!WARNING]
> The journal can hold secrets that services print. Anyone who can reach the hub can read it, so keep the hub private. An AI client that reads logs sends them to its model provider.

## If it fails

1. `Could not read the machine's logs: journalctl is not installed on this machine, so Tender can't read its logs.` means the machine has no systemd journal.
2. `apps has no logs.` for a unit you know writes to the journal usually means the daemon isn't in the `systemd-journal` group yet. Run `sudo tender install refresh` on the machine.

## Next steps

1. [Control a service](control-a-service.md) to read one container's logs.
2. [MCP reference](../mcp.md#get_machine_logs) to let an AI client read the journal.
3. [Security](../security.md#logs-can-hold-secrets) explains how Tender handles logs.
