# MCP tools

The hub serves 29 MCP tools at `/mcp`, so an AI client can do everything a person can do with the `tender` command or the REST API.

## Prerequisites

1. A running hub, reachable from the machine where the client runs. See [Install](install.md).
2. An MCP client that supports streamable HTTP, such as Claude Code, or one that runs a local process over stdio on the hub machine.

## Connect a client

The hub serves MCP over streamable HTTP at `https://apps.your-tailnet.ts.net/mcp`. On the hub machine, a client can also run `tender mcp --stdio` as a local process. [Connect an AI client](guides/connect-an-ai-client.md) sets up both.

## How the tools behave

1. Every result is JSON. It comes as `structuredContent` and as the same JSON in a text block, for clients that read only text. A list is `{"data": [...]}`, with `next_cursor` when there is another page.
2. A failed call comes back as a tool result with `isError` set and one sentence that says what went wrong and what to do, such as `Machine "nope" doesn't exist. Call list_machines to see the names.` A failure inside the hub says only `The hub failed to read its data.` and the hub logs the cause.
3. Each tool's input schema lists its required inputs and allows no others. A call that leaves out a required input, gives one the wrong type or adds an unknown one fails before the tool runs, with the MCP SDK's `validating "arguments": ...` message. The `... is required` errors below are for an input passed as an empty string.
4. Each tool carries MCP annotations, listed in its entry. `readOnlyHint` is `true` for the tools that only read. Every other tool sets `destructiveHint`, which is `true` for the ones that remove data, cut a daemon off or take a service offline, so the client can ask you first. `idempotentHint` is `true` for the ones you can call again with the same input without changing anything more. Every tool sets `openWorldHint`, and only `send_test_email`, which sends email, sets it to `true`.
5. The action tools, `start_service`, `stop_service`, `restart_service`, `get_service_logs` and `get_machine_logs`, queue the action for the machine's daemon and wait up to 60 seconds for it. If the daemon hasn't finished by then, the result is the action with the status `pending` or `in_progress`. A `pending` action runs if the daemon picks it up within 15 minutes of being asked, and fails otherwise. An `in_progress` one is running, and fails if the daemon doesn't say how it ended within 15 minutes. Check it later with `get_action`.
6. Paged tools take `limit`, from 1 to 500 with a default of 50, and `cursor`. Pass a result's `next_cursor` as `cursor` to get the next, older page.
7. No tool returns bucket keys or repository passwords. The only secrets a tool returns are the ones made for you, shown once: a new machine token, and a destination password the hub generated.
8. Times are RFC 3339 in UTC, such as `2026-11-15T11:28:10Z`.

## Coverage

Every route a person may call has a tool that does the same. A route that reads one item is covered by the tool that lists the same objects. A test in `cmd/tender/parity_test.go` fails when a route has no tool, or when this table is missing a row.

| Route | Tool |
| --- | --- |
| `GET /v1/machines` | `list_machines` |
| `POST /v1/machines` | `add_machine` |
| `GET /v1/machines/{machine}` | `list_machines` |
| `DELETE /v1/machines/{machine}` | `remove_machine` |
| `POST /v1/machines/{machine}/token` | `rotate_machine_token` |
| `GET /v1/machines/{machine}/usage` | `get_machine_usage` |
| `GET /v1/backups` | `get_backup_status` |
| `GET /v1/machines/{machine}/backups` | `get_backup_status` |
| `GET /v1/machines/{machine}/backups/settings` | `get_backup_status` |
| `PUT /v1/machines/{machine}/backups/settings` | `set_backup` |
| `DELETE /v1/machines/{machine}/backups/settings` | `unset_backup` |
| `GET /v1/machines/{machine}/backups/snapshots` | `list_backup_snapshots` |
| `GET /v1/machines/{machine}/backups/snapshots/{id}` | `list_backup_snapshots` |
| `GET /v1/machines/{machine}/backups/history` | `list_backup_history` |
| `GET /v1/machines/{machine}/backups/history/{id}` | `list_backup_history` |
| `GET /v1/backups/destinations` | `list_destinations` |
| `POST /v1/backups/destinations` | `add_destination` |
| `GET /v1/backups/destinations/{destination}` | `list_destinations` |
| `PATCH /v1/backups/destinations/{destination}` | `update_destination` |
| `DELETE /v1/backups/destinations/{destination}` | `remove_destination` |
| `GET /v1/notifications` | `list_notifications` |
| `GET /v1/machines/{machine}/notifications` | `list_notifications` |
| `GET /v1/machines/{machine}/notifications/{id}` | `list_notifications` |
| `GET /v1/notifications/recipients` | `list_recipients` |
| `POST /v1/notifications/recipients` | `add_recipient` |
| `GET /v1/notifications/recipients/{id}` | `get_recipient` |
| `DELETE /v1/notifications/recipients/{id}` | `remove_recipient` |
| `POST /v1/notifications/test` | `send_test_email` |
| `GET /v1/services` | `list_services` |
| `GET /v1/machines/{machine}/services` | `list_services` |
| `GET /v1/machines/{machine}/services/{service}` | `list_services` |
| `PATCH /v1/machines/{machine}/services/{service}` | `set_service_alerts` |
| `POST /v1/machines/{machine}/actions` | `start_service`, `stop_service`, `restart_service`, `get_service_logs`, `get_machine_logs` |
| `GET /v1/machines/{machine}/actions` | `list_actions` |
| `GET /v1/machines/{machine}/actions/{id}` | `get_action` |
| `POST /mcp` | None. It is the MCP endpoint itself. |

The routes only a machine's daemon calls with its token, such as usage reports and backup runs, have no tools. They are listed in the [API reference](api.md).

## Machines

### `list_machines`

Lists every machine that reports to this hub, with its latest usage reading.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

None.

#### Returns

`data`, one entry per machine, by name. Each has `id`, `name`, `last_seen` (when its latest reading was taken, `null` before the first), `usage` and the timestamps. `usage` is the latest reading: `cpu_load` (the 1-minute load average), `memory_used_percent` and `disk_used_percent` (0 to 100) and `uptime_seconds`.

#### Limits

None. The list isn't paged.

#### Example call

```json
{"name": "list_machines", "arguments": {}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 1,
      "name": "apps",
      "last_seen": "2026-11-15T11:27:27Z",
      "usage": {
        "id": 1,
        "cpu_load": 0.08,
        "memory_used_percent": 17,
        "disk_used_percent": 27.71,
        "uptime_seconds": 222353,
        "created_at": "2026-11-15T11:27:27Z",
        "updated_at": "2026-11-15T11:27:27Z"
      },
      "created_at": "2026-11-15T11:27:26Z",
      "updated_at": "2026-11-15T11:27:26Z"
    }
  ]
}
```

#### Errors

None besides the hub's own failure.

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/machines` and `GET /v1/machines/{machine}` | `tender machines list` |

### `get_machine_usage`

Gets one machine's usage readings, newest first, optionally between two times. The daemon sends one a minute.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `since` | string | No | Only readings at or after this RFC 3339 time, such as `2026-11-01T00:00:00Z`. |
| `until` | string | No | Only readings at or before this RFC 3339 time. |
| `limit` | integer | No | The most readings to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | `next_cursor` from the previous page. |

#### Returns

`data`, the readings, each with `id`, `cpu_load`, `memory_used_percent`, `disk_used_percent`, `uptime_seconds` and `created_at` (when it was taken). `next_cursor` is there when older readings follow.

#### Limits

At most 500 readings a page.

#### Example call

```json
{"name": "get_machine_usage", "arguments": {"machine": "apps", "limit": 2}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 9,
      "cpu_load": 0.03,
      "memory_used_percent": 15.45,
      "disk_used_percent": 27.72,
      "uptime_seconds": 222781,
      "created_at": "2026-11-15T11:34:35Z",
      "updated_at": "2026-11-15T11:34:35Z"
    },
    {
      "id": 8,
      "cpu_load": 0.09,
      "memory_used_percent": 15.38,
      "disk_used_percent": 27.71,
      "uptime_seconds": 222721,
      "created_at": "2026-11-15T11:33:35Z",
      "updated_at": "2026-11-15T11:33:35Z"
    }
  ],
  "next_cursor": "MTc5NDc0MjQxNTo4"
}
```

#### Errors

1. `machine is required. Call list_machines to see the names.`
2. `Machine "apps" doesn't exist. Call list_machines to see the names.`
3. `since must be an RFC 3339 time, such as 2026-11-01T00:00:00Z.` The same goes for `until`.
4. `limit must be a whole number from 1 to 500.`
5. `cursor is not valid.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/machines/{machine}/usage` | `tender machines list`, which shows the latest reading only. |

### `add_machine`

Adds a machine to the hub and returns its token, shown only this once.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `false` | `false` |

A second call with the same name fails.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | The new machine's name: lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |

#### Returns

The machine, with `id`, `name`, the timestamps and `token`. The hub stores only a hash of the token, so nobody can read it again. Give it to the machine's owner to install with `tender install daemon`.

#### Limits

None.

#### Example call

```json
{"name": "add_machine", "arguments": {"name": "apps"}}
```

#### Example result

```json
{
  "id": 1,
  "name": "apps",
  "token": "<token>",
  "created_at": "2026-11-15T11:27:35Z",
  "updated_at": "2026-11-15T11:27:35Z"
}
```

#### Errors

1. `Machine names are lowercase letters, digits and hyphens, up to 63 characters.`
2. `Machine "apps" already exists. Choose another name, or rotate its token to get a new one.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines` | `tender machines add NAME` |

### `rotate_machine_token`

Replaces a machine's token and returns the new one, shown only this once.

> [!WARNING]
> The old token stops working at once. The machine's daemon can't report or back up until the new token is installed on it with `sudo tender install daemon --token-file PATH`.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `false` | `false` |

It is destructive because it cuts the machine's daemon off at once. Each call makes a new token.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |

#### Returns

`token`, the new token.

#### Limits

None.

#### Example call

```json
{"name": "rotate_machine_token", "arguments": {"machine": "apps"}}
```

#### Example result

```json
{"token": "<token>"}
```

#### Errors

1. `machine is required. Call list_machines to see the names.`
2. `Machine "apps" doesn't exist. Call list_machines to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/token` | `tender machines rotate-token NAME` |

### `remove_machine`

Removes a machine and everything the hub knows about it: usage, backup settings, snapshots, history, services, actions and notifications.

> [!WARNING]
> This can't be undone. The machine's token stops working, so its daemon can no longer report. Its backups in the bucket stay where they are.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |

#### Returns

`removed`, the name of the machine that was removed.

#### Limits

None.

#### Example call

```json
{"name": "remove_machine", "arguments": {"machine": "apps"}}
```

#### Example result

```json
{"removed": "apps"}
```

#### Errors

1. `machine is required. Call list_machines to see the names.`
2. `Machine "apps" doesn't exist. Call list_machines to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `DELETE /v1/machines/{machine}` | `tender machines remove NAME --yes` |

## Backups

### `get_backup_status`

Gets the backup state of one machine, or of every machine when you leave `machine` out.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | No | The machine's name. Leave it out to get every machine. |

#### Returns

`data`, one entry per machine, with `machine`, `settings` (the destination, the cron `schedule` in UTC and `retention_days`), `latest_snapshot` (`restic_id`, `size_bytes`, `paths`, `created_at`), `last_backup` and `last_check` (the newest run of that type, with its `status` and `error`) and `snapshot_count`. A part that doesn't exist yet is `null`.

#### Limits

None.

#### Example call

```json
{"name": "get_backup_status", "arguments": {"machine": "apps"}}
```

#### Example result

```json
{
  "data": [
    {
      "machine": "apps",
      "settings": {
        "destination": {
          "id": 1,
          "name": "r2",
          "type": "s3",
          "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
          "bucket": "my-backups",
          "path": "tender",
          "created_at": "2026-11-15T11:27:26Z",
          "updated_at": "2026-11-15T11:27:26Z"
        },
        "schedule": "30 3 * * *",
        "retention_days": 30,
        "created_at": "2026-11-15T11:27:26Z",
        "updated_at": "2026-11-15T11:27:26Z"
      },
      "latest_snapshot": {
        "id": 1,
        "restic_id": "1c88a1ca2f519eaf2e30d3ebe3ce206ad50229944189e8db433b08010ff02ab6",
        "size_bytes": 200006,
        "paths": ["/"],
        "created_at": "2026-11-15T11:27:30Z",
        "updated_at": "2026-11-15T11:27:31Z"
      },
      "last_backup": {
        "id": 1,
        "snapshot_id": 1,
        "type": "backup",
        "status": "completed",
        "error": null,
        "created_at": "2026-11-15T11:27:27Z",
        "updated_at": "2026-11-15T11:27:31Z"
      },
      "last_check": {
        "id": 3,
        "snapshot_id": null,
        "type": "check",
        "status": "completed",
        "error": null,
        "created_at": "2026-11-15T11:27:32Z",
        "updated_at": "2026-11-15T11:27:33Z"
      },
      "snapshot_count": 1
    }
  ]
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/backups`, `GET /v1/machines/{machine}/backups` and `GET /v1/machines/{machine}/backups/settings` | `tender backups status [MACHINE]` |

### `list_backup_snapshots`

Lists one machine's restic snapshots, newest first.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `limit` | integer | No | The most snapshots to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | `next_cursor` from the previous page. |

#### Returns

`data`, the snapshots, each with `id`, `restic_id` (the ID to pass to restic), `size_bytes` (`null` when unknown), the backed-up `paths` and `created_at` (the snapshot time). `next_cursor` is there when older snapshots follow.

#### Limits

At most 500 snapshots a page.

#### Example call

```json
{"name": "list_backup_snapshots", "arguments": {"machine": "apps", "limit": 1}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 9,
      "restic_id": "58db71f28802ee99c9bad66e4c84f3223bfca97bdebe45e5e05ae76ae2cf4dcf",
      "size_bytes": 200006,
      "paths": ["/"],
      "created_at": "2026-11-15T11:35:05Z",
      "updated_at": "2026-11-15T11:35:07Z"
    }
  ],
  "next_cursor": "MTc5NDc0MjUwNTo5"
}
```

#### Errors

1. `machine is required. Call list_machines to see the names.`
2. `Machine "apps" doesn't exist. Call list_machines to see the names.`
3. `limit must be a whole number from 1 to 500.`
4. `cursor is not valid.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/machines/{machine}/backups/snapshots` and `GET /v1/machines/{machine}/backups/snapshots/{id}` | `tender backups status MACHINE`, which shows the snapshot count only. |

### `list_backup_history`

Lists one machine's backup, cleanup and check runs, newest first, optionally of one type or status.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `type` | string | No | Only runs of this type: `backup`, `cleanup` or `check`. |
| `status` | string | No | Only runs with this status: `pending`, `in_progress`, `completed` or `failed`. |
| `limit` | integer | No | The most runs to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | `next_cursor` from the previous page. |

#### Returns

`data`, the runs, each with `id`, `snapshot_id` (the snapshot a backup made, else `null`), `type`, `status`, `error` (`null` unless it failed) and `created_at` (when it started). `next_cursor` is there when older runs follow.

#### Limits

At most 500 runs a page.

#### Example call

```json
{"name": "list_backup_history", "arguments": {"machine": "apps", "limit": 2}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 21,
      "snapshot_id": null,
      "type": "cleanup",
      "status": "completed",
      "error": null,
      "created_at": "2026-11-15T11:36:07Z",
      "updated_at": "2026-11-15T11:36:08Z"
    },
    {
      "id": 20,
      "snapshot_id": 10,
      "type": "backup",
      "status": "completed",
      "error": null,
      "created_at": "2026-11-15T11:36:05Z",
      "updated_at": "2026-11-15T11:36:07Z"
    }
  ],
  "next_cursor": "MTc5NDc0MjU2NToyMA"
}
```

#### Errors

1. `machine is required. Call list_machines to see the names.`
2. `Machine "apps" doesn't exist. Call list_machines to see the names.`
3. `type must be backup, cleanup or check.`
4. `status must be pending, in_progress, completed or failed.`
5. `limit must be a whole number from 1 to 500.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/machines/{machine}/backups/history` and `GET /v1/machines/{machine}/backups/history/{id}` | `tender backups status MACHINE`, which shows the last backup only. |

### `set_backup`

Sets where, when and for how long a machine's backups are kept. It replaces any earlier settings.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `destination` | string | Yes | The destination's name, as `list_destinations` shows it. |
| `schedule` | string | Yes | A standard 5-field cron expression in UTC, such as `30 3 * * *`. |
| `retention_days` | integer | Yes | The days to keep snapshots, at least 1. |

#### Returns

The settings, with `destination` (without its keys), `schedule`, `retention_days` and the timestamps. A daemon without settings picks them up within 30 seconds. A daemon that already has settings checks for changes every 10 minutes.

#### Limits

None.

#### Example call

```json
{"name": "set_backup", "arguments": {"machine": "apps", "destination": "r2", "schedule": "30 3 * * *", "retention_days": 30}}
```

#### Example result

```json
{
  "destination": {
    "id": 1,
    "name": "r2",
    "type": "s3",
    "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
    "bucket": "my-backups",
    "path": "tender",
    "created_at": "2026-11-15T11:27:26Z",
    "updated_at": "2026-11-15T11:27:26Z"
  },
  "schedule": "30 3 * * *",
  "retention_days": 30,
  "created_at": "2026-11-15T11:27:38Z",
  "updated_at": "2026-11-15T11:27:38Z"
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `Destination "r2" doesn't exist.`
3. `schedule must be a standard 5-field cron expression: ...`, followed by the parser's reason.
4. `retention_days must be at least 1.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `PUT /v1/machines/{machine}/backups/settings` | `tender backups set MACHINE --destination NAME --schedule CRON --retention-days N` |

### `unset_backup`

Removes a machine's backup settings, so it stops backing up. Its snapshots and history stay.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |

#### Returns

`removed`, naming the settings that were removed. The daemon stops backing up and deletes its saved copy of the settings and password. Call `set_backup` to start again.

#### Limits

None.

#### Example call

```json
{"name": "unset_backup", "arguments": {"machine": "apps"}}
```

#### Example result

```json
{"removed": "backup settings of apps"}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `This machine has no backup settings. There is nothing to remove.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `DELETE /v1/machines/{machine}/backups/settings` | `tender backups unset MACHINE` |

## Destinations

### `list_destinations`

Lists the places backups go, by name.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

None.

#### Returns

`data`, the destinations, each with `id`, `name`, `type` (`s3`), `endpoint`, `bucket`, `path` (the folder inside the bucket, empty for its top) and the timestamps. Each machine's repository is a folder named after the machine under `path`. Keys and passwords are never shown.

#### Limits

None. The list isn't paged.

#### Example call

```json
{"name": "list_destinations", "arguments": {}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 1,
      "name": "r2",
      "type": "s3",
      "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
      "bucket": "my-backups",
      "path": "tender",
      "created_at": "2026-11-15T11:27:26Z",
      "updated_at": "2026-11-15T11:27:26Z"
    }
  ]
}
```

#### Errors

None besides the hub's own failure.

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/backups/destinations` and `GET /v1/backups/destinations/{destination}` | `tender destinations list` |

### `add_destination`

Adds a place to send backups: an S3-compatible bucket, such as Cloudflare R2. Each machine gets its own folder in it.

The keys and the password you pass go through the AI conversation, and so to the client's model provider. For real keys, run `tender destinations add` instead, which asks for them with typing hidden. The hub stores them encrypted, and no tool, route or log shows them again.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `false` | `false` |

A second call with the same name fails.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | The new destination's name: lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |
| `type` | string | No | Only `s3`, the default, for any S3-compatible bucket. |
| `endpoint` | string | Yes | The S3 endpoint URL, such as `https://<account-id>.r2.cloudflarestorage.com`. |
| `bucket` | string | Yes | The bucket's name. |
| `path` | string | No | The folder inside the bucket. Leave it out for the bucket's top. |
| `access_key_id` | string | Yes | The S3 access key ID. |
| `secret_access_key` | string | Yes | The S3 secret access key. |
| `password` | string | No | The restic repository password for every machine at this destination. Leave it out and the hub makes one. |

#### Returns

The destination, as `list_destinations` shows it. Without a `password` input, the result also has `password`, the one the hub made, shown only this once. Tell the owner to save it in a password manager, because nobody can restore the backups without it. A password you pass is not returned.

#### Limits

None.

#### Example call

```json
{
  "name": "add_destination",
  "arguments": {
    "name": "r2",
    "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
    "bucket": "my-backups",
    "path": "tender",
    "access_key_id": "<access key ID>",
    "secret_access_key": "<secret access key>"
  }
}
```

#### Example result

```json
{
  "id": 1,
  "name": "r2",
  "type": "s3",
  "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
  "bucket": "my-backups",
  "path": "tender",
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T11:27:26Z",
  "password": "<password>"
}
```

#### Errors

1. `Destination names are lowercase letters, digits and hyphens, up to 63 characters.`
2. `type must be s3.`
3. `endpoint must be a URL such as https://<account>.r2.cloudflarestorage.com.`
4. `bucket is required.`
5. `access_key_id is required.` The same goes for `secret_access_key`.
6. `A destination with this name or this endpoint, bucket and path already exists. Choose another name, or use the existing one.`
7. `This process has no TENDER_ENCRYPTION_KEY, so it can't store or read destination credentials. Set it to the hub's key and run the command again.` This happens only over stdio.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/backups/destinations` | `tender destinations add NAME --endpoint URL --bucket BUCKET [--path PATH]` |

### `update_destination`

Changes a destination's endpoint, bucket, path or keys. Anything you leave out stays as it is.

New keys you pass go through the AI conversation. The password can't change, because every repository at the destination is encrypted with it.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | The destination's name, as `list_destinations` shows it. |
| `endpoint` | string | No | The new S3 endpoint URL. |
| `bucket` | string | No | The new bucket's name. |
| `path` | string | No | The new folder inside the bucket. |
| `access_key_id` | string | No | The new S3 access key ID. |
| `secret_access_key` | string | No | The new S3 secret access key. |
| `password` | string | No | Not accepted. A call with it fails. |

#### Returns

The destination, as `list_destinations` shows it, without its keys.

#### Limits

None.

#### Example call

```json
{"name": "update_destination", "arguments": {"destination": "r2", "path": "tender-2026"}}
```

#### Example result

```json
{
  "id": 1,
  "name": "r2",
  "type": "s3",
  "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
  "bucket": "my-backups",
  "path": "tender-2026",
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T12:02:41Z"
}
```

#### Errors

1. `Destination "r2" doesn't exist.`
2. `The password can't change: every repository at this destination is encrypted with it. Add a new destination with the new password and move the machines to it.`
3. `endpoint and bucket can't be empty.`
4. `access_key_id and secret_access_key can't be empty. Leave a key out to keep it.`
5. `Another destination already uses this endpoint, bucket and path. Use that one, or choose another location.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `PATCH /v1/backups/destinations/{destination}` | None. The CLI can't change a destination yet. |

### `remove_destination`

Removes a destination that no machine backs up to.

> [!WARNING]
> The hub forgets the destination's keys and password. The backups already in the bucket stay there, so keep the password in your password manager to restore them.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | The destination's name, as `list_destinations` shows it. |

#### Returns

`removed`, the name of the destination that was removed.

#### Limits

None.

#### Example call

```json
{"name": "remove_destination", "arguments": {"destination": "r2"}}
```

#### Example result

```json
{"removed": "r2"}
```

#### Errors

1. `Destination "r2" doesn't exist.`
2. `Destination "r2" is used by apps. Stop their backups first (delete their backup settings), then remove the destination.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `DELETE /v1/backups/destinations/{destination}` | `tender destinations remove NAME` |

## Notifications

### `list_notifications`

Lists alerts, newest first, optionally of one machine, status or severity.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | No | Only this machine's notifications. Leave it out for every machine. |
| `status` | string | No | Only notifications with this status: `pending`, `sent` or `resolved`. |
| `severity` | string | No | Only notifications with this severity: `info`, `warning` or `critical`. |
| `limit` | integer | No | The most notifications to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | `next_cursor` from the previous page. |

#### Returns

`data`, the notifications, each with `id`, `machine` (the machine it is about), `service` (the Docker service it is about, else `null`), `type`, `severity`, `status`, `content` (the alert in one sentence) and the timestamps. `pending` and `sent` are open, and `resolved` is closed. [Alerts](alerts.md) explains the statuses and lists the types.

#### Limits

At most 500 notifications a page.

#### Example call

```json
{"name": "list_notifications", "arguments": {"machine": "apps", "limit": 1}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 3,
      "machine": "apps",
      "service": "web",
      "type": "service_memory_high",
      "severity": "warning",
      "status": "resolved",
      "content": "The service web on apps uses 950 MiB, 92% of its 1 GiB memory limit.",
      "created_at": "2026-11-15T11:35:35Z",
      "updated_at": "2026-11-15T11:36:35Z"
    }
  ],
  "next_cursor": "MTc5NDc0MjUzNToz"
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `status must be pending, sent or resolved.`
3. `severity must be info, warning or critical.`
4. `limit must be a whole number from 1 to 500.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/notifications`, `GET /v1/machines/{machine}/notifications` and `GET /v1/machines/{machine}/notifications/{id}` | `tender status`, which shows the open alerts. |

### `list_recipients`

Lists the email addresses that get every alert email, in the order they were added.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

None.

#### Returns

`data`, the recipients, each with `id` (for `remove_recipient`), `email` and the timestamps.

#### Limits

None. The list isn't paged.

#### Example call

```json
{"name": "list_recipients", "arguments": {}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 1,
      "email": "ops@example.com",
      "created_at": "2026-11-15T11:27:26Z",
      "updated_at": "2026-11-15T11:27:26Z"
    }
  ]
}
```

#### Errors

None besides the hub's own failure.

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/notifications/recipients` | `tender recipients list` |

### `get_recipient`

Gets one recipient by id.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | integer | Yes | The recipient's id, as `list_recipients` shows it. |

#### Returns

The recipient, with `id`, `email` and the timestamps.

#### Limits

None.

#### Example call

```json
{"name": "get_recipient", "arguments": {"id": 1}}
```

#### Example result

```json
{
  "id": 1,
  "email": "ops@example.com",
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T11:27:26Z"
}
```

#### Errors

1. `Recipient 1 doesn't exist. List the recipients to see their ids.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/notifications/recipients/{id}` | `tender recipients list`, which shows every recipient. |

### `add_recipient`

Adds an email address that gets every alert email from now on.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `false` | `false` |

A second call with the same address fails.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string | Yes | A bare email address, such as `ops@example.com`, without a display name. |

#### Returns

The recipient, with `id`, `email` and the timestamps. Call `send_test_email` to check that mail reaches it.

#### Limits

None.

#### Example call

```json
{"name": "add_recipient", "arguments": {"email": "ops@example.com"}}
```

#### Example result

```json
{
  "id": 1,
  "email": "ops@example.com",
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T11:27:26Z"
}
```

#### Errors

1. `email must be a bare address such as alerts@example.com.`
2. `ops@example.com is already a recipient.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/notifications/recipients` | `tender recipients add EMAIL` |

### `remove_recipient`

Removes a recipient by id, so they stop getting alert emails.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | integer | Yes | The recipient's id, as `list_recipients` shows it. |

#### Returns

`removed`, naming the recipient that was removed. Alerts are still recorded when nobody is left to email.

#### Limits

None.

#### Example call

```json
{"name": "remove_recipient", "arguments": {"id": 1}}
```

#### Example result

```json
{"removed": "recipient 1"}
```

#### Errors

1. `Recipient 1 doesn't exist. List the recipients to see their ids.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `DELETE /v1/notifications/recipients/{id}` | `tender recipients remove EMAIL`, which takes the address instead of the id. |

### `send_test_email`

Sends the test email to every recipient, to check that alert emails arrive.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `false` | `true` |

It changes nothing on the hub, but it sends email, which reaches outside the hub. Each call sends again.

#### Inputs

None.

#### Returns

`sent_to`, the addresses the email went to.

#### Limits

None.

#### Example call

```json
{"name": "send_test_email", "arguments": {}}
```

#### Example result

```json
{"sent_to": ["ops@example.com"]}
```

#### Errors

1. `Email is off on this hub. Set TENDER_SMTP_HOST and TENDER_SMTP_FROM in /etc/tender/hub.env and restart the hub.`
2. `There are no recipients to send to. Add one first, such as alerts@example.com.`
3. `The hub couldn't send the email: ...`, with the SMTP server's reason, followed by `Check the TENDER_SMTP_ settings in /etc/tender/hub.env.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/notifications/test` | `tender email test` |

## Services

### `list_services`

Lists the Docker containers each machine's daemon reports once a minute, for every machine or one.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | No | Only this machine's services. Leave it out for every machine. |

#### Returns

`data`, one entry per machine, with `machine`, `docker` (`running`, `unreachable` when its daemon can't reach Docker, or `null` before any report), `docker_reason` (while unreachable, `socket_missing`, `permission_denied`, `refused`, `timeout` or `error`, else `null`) and `data`, its services. Each service is one container, by its container name, with `project` (the Compose project, or `null`), `container_id`, `image`, `state`, `health` (`null` without a health check), `exit_code` (while exited or dead), `restart_count`, `restart_policy`, `started_at`, `memory_bytes`, `memory_limit_bytes` (`null` without a limit), `cpu_percent` (100 is one whole CPU), `alerts_enabled`, `stopped_by_tender`, `removed_at` (when its container was gone, else `null`) and the timestamps. Usage is from the latest report. A `removed` service can't be started, stopped, restarted or read, and goes 7 days after `removed_at`, as [Removed services](services.md#removed-services) explains. [Services](services.md#what-a-service-is) explains each field and when usage is `null`.

#### Limits

None. A machine without Docker has no services.

#### Example call

```json
{"name": "list_services", "arguments": {"machine": "apps"}}
```

#### Example result

```json
{
  "data": [
    {
      "machine": "apps",
      "docker": "running",
      "docker_reason": null,
      "data": [
        {
          "id": 4,
          "machine": "apps",
          "name": "migrate",
          "project": null,
          "container_id": "c0ffee03",
          "image": "migrate:1",
          "state": "exited",
          "health": null,
          "exit_code": 0,
          "restart_count": 0,
          "restart_policy": "no",
          "started_at": null,
          "memory_bytes": null,
          "memory_limit_bytes": null,
          "cpu_percent": null,
          "alerts_enabled": true,
          "stopped_by_tender": false,
          "removed_at": null,
          "created_at": "2026-11-15T11:27:27Z",
          "updated_at": "2026-11-15T11:28:10Z"
        },
        {
          "id": 3,
          "machine": "apps",
          "name": "web",
          "project": "apps",
          "container_id": "c0ffee04",
          "image": "ghcr.io/example/web:2.4",
          "state": "running",
          "health": "healthy",
          "exit_code": null,
          "restart_count": 0,
          "restart_policy": "unless-stopped",
          "started_at": "2026-11-15T10:27:26Z",
          "memory_bytes": 209715200,
          "memory_limit_bytes": 1073741824,
          "cpu_percent": 12.5,
          "alerts_enabled": true,
          "stopped_by_tender": false,
          "removed_at": null,
          "created_at": "2026-11-15T11:27:27Z",
          "updated_at": "2026-11-15T11:28:10Z"
        }
      ]
    }
  ]
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/services`, `GET /v1/machines/{machine}/services` and `GET /v1/machines/{machine}/services/{service}` | `tender services list [MACHINE]` |

### `set_service_alerts`

Turns alerts on or off for one service. Alerts are on for every new service.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, its container name, as `list_services` shows it. |
| `enabled` | boolean | Yes | `true` to get alerts for this service, `false` to stop them and resolve its open alerts. |

#### Returns

The service, as `list_services` shows it. With alerts off, its open alerts resolve within a minute, or once their resolved email goes out when sending it is retried. The switch follows the service's name, as [Services](services.md#what-a-service-is) explains.

#### Limits

None.

#### Example call

```json
{"name": "set_service_alerts", "arguments": {"machine": "apps", "service": "web", "enabled": false}}
```

#### Example result

```json
{
  "id": 3,
  "machine": "apps",
  "name": "web",
  "project": "apps",
  "container_id": "c0ffee04",
  "image": "ghcr.io/example/web:2.4",
  "state": "running",
  "health": "healthy",
  "exit_code": null,
  "restart_count": 0,
  "restart_policy": "unless-stopped",
  "started_at": "2026-11-15T10:27:26Z",
  "memory_bytes": 209715200,
  "memory_limit_bytes": 1073741824,
  "cpu_percent": 12.5,
  "alerts_enabled": false,
  "stopped_by_tender": false,
  "removed_at": null,
  "created_at": "2026-11-15T11:27:27Z",
  "updated_at": "2026-11-15T11:29:02Z"
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `service is required. Call list_services to see the names.`
3. `Service "web" doesn't exist on apps. Call list_services to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `PATCH /v1/machines/{machine}/services/{service}` | `tender services alerts MACHINE SERVICE on\|off` |

## Actions

The tools in this section queue an action for a machine's daemon and wait up to 60 seconds for it, or read actions back. [Services](services.md#how-actions-reach-a-machine) explains how an action reaches the machine. Each returns the action: `id`, `machine`, `type` (`start`, `stop`, `restart` or `logs`), `service` (`null` for the machine's logs), `unit`, `priority`, `since` and `lines` (logs only), `status` (`pending`, `in_progress`, `completed` or `failed`), `output`, `output_cleared` and the timestamps. The hub keeps each machine's newest 100 finished actions, and every `pending` or `in_progress` one.

### `start_service`

Starts a stopped service through the daemon on its machine. Starting a running service changes nothing.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, its container name, as `list_services` shows it. |

#### Returns

The action, with the status `completed`, or `pending` or `in_progress` if the daemon hasn't finished within 60 seconds. `output` is `null`. A start clears the service's `stopped_by_tender`.

#### Limits

The tool waits up to 60 seconds. An action the daemon doesn't pick up within 15 minutes fails.

#### Example call

```json
{"name": "start_service", "arguments": {"machine": "apps", "service": "web"}}
```

#### Example result

```json
{
  "id": 3,
  "machine": "apps",
  "type": "start",
  "service": "web",
  "unit": null,
  "priority": null,
  "since": null,
  "lines": null,
  "status": "completed",
  "output_cleared": false,
  "created_at": "2026-11-15T11:28:09Z",
  "updated_at": "2026-11-15T11:28:09Z",
  "output": null
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `service is required. Call list_services to see the names.`
3. `Service "web" doesn't exist on apps. Call list_services to see the names.`
4. `Service "web" was removed: Docker on this machine no longer has its container.`
5. `Could not start web: ...`, with Docker's reason, when the daemon tried and failed.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/actions` with `{"type": "start", "service": "web"}`, then `GET /v1/machines/{machine}/actions/{id}` | `tender services start MACHINE SERVICE` |

### `stop_service`

Stops a running service through the daemon on its machine, with the container's own stop timeout.

> [!WARNING]
> Whatever the service serves goes offline until it is started again. A service Tender stopped doesn't alert as down, so nobody gets an email about it.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, its container name, as `list_services` shows it. |

#### Returns

The action, with the status `completed`, or `pending` or `in_progress` if the daemon hasn't finished within 60 seconds. `output` is `null`. The service shows `stopped_by_tender: true` until something starts it.

#### Limits

The tool waits up to 60 seconds. An action the daemon doesn't pick up within 15 minutes fails.

#### Example call

```json
{"name": "stop_service", "arguments": {"machine": "apps", "service": "web"}}
```

#### Example result

```json
{
  "id": 2,
  "machine": "apps",
  "type": "stop",
  "service": "web",
  "unit": null,
  "priority": null,
  "since": null,
  "lines": null,
  "status": "completed",
  "output_cleared": false,
  "created_at": "2026-11-15T11:28:08Z",
  "updated_at": "2026-11-15T11:28:08Z",
  "output": null
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `service is required. Call list_services to see the names.`
3. `Service "web" doesn't exist on apps. Call list_services to see the names.`
4. `Service "web" was removed: Docker on this machine no longer has its container.`
5. `Could not stop web: ...`, with Docker's reason, when the daemon tried and failed.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/actions` with `{"type": "stop", "service": "web"}`, then `GET /v1/machines/{machine}/actions/{id}` | `tender services stop MACHINE SERVICE` |

### `restart_service`

Restarts a service through the daemon on its machine.

> [!WARNING]
> Whatever the service serves is offline while it restarts.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `false` | `false` |

Each call restarts the service again.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, its container name, as `list_services` shows it. |

#### Returns

The action, with the status `completed`, or `pending` or `in_progress` if the daemon hasn't finished within 60 seconds. `output` is `null`.

#### Limits

The tool waits up to 60 seconds. An action the daemon doesn't pick up within 15 minutes fails.

#### Example call

```json
{"name": "restart_service", "arguments": {"machine": "apps", "service": "web"}}
```

#### Example result

```json
{
  "id": 1,
  "machine": "apps",
  "type": "restart",
  "service": "web",
  "unit": null,
  "priority": null,
  "since": null,
  "lines": null,
  "status": "completed",
  "output_cleared": false,
  "created_at": "2026-11-15T11:28:07Z",
  "updated_at": "2026-11-15T11:28:07Z",
  "output": null
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `service is required. Call list_services to see the names.`
3. `Service "web" doesn't exist on apps. Call list_services to see the names.`
4. `Service "web" was removed: Docker on this machine no longer has its container.`
5. `Could not restart web: ...`, with Docker's reason, when the daemon tried and failed.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/actions` with `{"type": "restart", "service": "web"}`, then `GET /v1/machines/{machine}/actions/{id}` | `tender services restart MACHINE SERVICE` |

### `get_service_logs`

Gets a service's newest lines of output, stdout and stderr together, each line starting with Docker's timestamp.

> [!WARNING]
> Logs can hold secrets, and the result goes to the client's model provider. Ask for the fewest lines you need. The hub clears the output 1 hour after the action finished, and logs never go into alerts or emails.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

It changes nothing on the machine, but the hub records the read as an action, which counts toward the 100 finished actions it keeps.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, its container name, as `list_services` shows it. |
| `lines` | integer | No | How many of the newest lines, from 1 to 2000. The default is 200. |
| `since` | string | No | Only lines from this far back, such as `30m`, `1h` or `7d`, at most `7d`. Leave it out for no limit. |

#### Returns

The action, with the logs in `output`, the oldest line first. An hour after the action completed, `output` is `null` and `output_cleared` is `true`.

#### Limits

At most 2000 lines and 256 KiB. Past 256 KiB, the oldest lines are dropped. The tool waits up to 60 seconds.

#### Example call

```json
{"name": "get_service_logs", "arguments": {"machine": "apps", "service": "web", "lines": 2}}
```

#### Example result

```json
{
  "id": 5,
  "machine": "apps",
  "type": "logs",
  "service": "web",
  "unit": null,
  "priority": null,
  "since": null,
  "lines": 2,
  "status": "completed",
  "output_cleared": false,
  "created_at": "2026-11-15T11:28:12Z",
  "updated_at": "2026-11-15T11:28:12Z",
  "output": "2026-11-15T11:28:12.339011345Z web listening on :3000\n2026-11-15T11:28:12.339015823Z started by request\n"
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `Service "web" doesn't exist on apps. Call list_services to see the names.`
3. `Service "web" was removed: Docker on this machine no longer has its container.`
4. `lines must be a whole number from 1 to 2000.`
5. `since must be a duration such as 30m, 1h or 7d, at most 7d.`
6. `Could not read the logs of web: ...`, with Docker's reason, when the daemon tried and failed.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/actions` with `{"type": "logs", "service": "web"}`, then `GET /v1/machines/{machine}/actions/{id}` | `tender services logs MACHINE SERVICE [--lines N] [--since D]` |

### `get_machine_logs`

Gets the newest entries of a machine's systemd journal, which the daemon reads with `journalctl`. Each line has the time in UTC, the unit or program, and the message.

> [!WARNING]
> The journal can hold secrets that services print, and the result goes to the client's model provider. Filter by `unit`, `priority` and `since` to get only what you need. The hub clears the output 1 hour after the action finished, and logs never go into alerts or emails.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

It changes nothing on the machine, but the hub records the read as an action, which counts toward the 100 finished actions it keeps.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `unit` | string | No | Only this systemd unit, such as `tenderd` or `nginx.service`. Leave it out for every unit. |
| `priority` | string | No | Only this priority and the more severe ones: `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info` or `debug`. |
| `lines` | integer | No | How many of the newest entries, from 1 to 2000. The default is 200. |
| `since` | string | No | Only entries from this far back, such as `30m`, `1h` or `7d`, at most `7d`. Leave it out for no limit. |

#### Returns

The action, with the journal in `output`, the oldest entry first. An hour after the action completed, `output` is `null` and `output_cleared` is `true`.

#### Limits

At most 2000 entries and 256 KiB. Past 256 KiB, the oldest entries are dropped. The tool waits up to 60 seconds.

#### Example call

```json
{"name": "get_machine_logs", "arguments": {"machine": "apps", "priority": "warning", "lines": 5}}
```

#### Example result

```json
{
  "id": 8,
  "machine": "apps",
  "type": "logs",
  "service": null,
  "unit": null,
  "priority": "warning",
  "since": null,
  "lines": 5,
  "status": "completed",
  "output_cleared": false,
  "created_at": "2026-11-15T11:28:15Z",
  "updated_at": "2026-11-15T11:28:15Z",
  "output": "2026-11-15T11:27:15.000000000Z tenderd.service backup took 41s, longer than usual.\n2026-11-15T11:28:15.000000000Z kernel eth0: link up\n"
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `unit must be a systemd unit name such as tenderd or nginx.service.`
3. `priority must be one of emerg, alert, crit, err, warning, notice, info, debug.`
4. `lines must be a whole number from 1 to 2000.`
5. `since must be a duration such as 30m, 1h or 7d, at most 7d.`
6. `Could not read the machine's logs: journalctl is not installed on this machine, so Tender can't read its logs.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/actions` with `{"type": "logs"}` and no `service`, then `GET /v1/machines/{machine}/actions/{id}` | `tender logs MACHINE [--unit UNIT] [--priority P] [--since D] [--lines N]` |

### `get_action`

Gets one action on a machine by id, with its output. Use it to check an action that was still `pending` or `in_progress` when a tool stopped waiting.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `id` | integer | Yes | The action's id, from the tool that queued it or from `list_actions`. |

#### Returns

The action, with `output`: the logs, the reason it failed, or `null`. A completed logs action's output is cleared 1 hour after it finished, and a failed one keeps its reason.

#### Limits

None.

#### Example call

```json
{"name": "get_action", "arguments": {"machine": "apps", "id": 6}}
```

#### Example result

```json
{
  "id": 6,
  "machine": "apps",
  "type": "logs",
  "service": null,
  "unit": "tenderd",
  "priority": "warning",
  "since": "1h",
  "lines": 2,
  "status": "completed",
  "output_cleared": true,
  "created_at": "2026-11-15T11:28:13Z",
  "updated_at": "2026-11-15T11:28:34Z",
  "output": null
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `Action 6 doesn't exist on this machine.`

An action that never finished shows the status `failed` with the `output` `the daemon did not pick this up; is the machine online?`, or `the daemon started this but did not say how it ended; is the machine online?`.

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/machines/{machine}/actions/{id}` | `tender actions show MACHINE ID` |

### `list_actions`

Lists a machine's actions, newest first, without their output.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |

#### Returns

`data`, the actions, each with every field but `output`. Call `get_action` for the output.

#### Limits

The hub keeps each machine's newest 100 finished actions, and every `pending` or `in_progress` one. The list isn't paged.

#### Example call

```json
{"name": "list_actions", "arguments": {"machine": "apps"}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 8,
      "machine": "apps",
      "type": "logs",
      "service": null,
      "unit": null,
      "priority": "warning",
      "since": null,
      "lines": 5,
      "status": "completed",
      "output_cleared": false,
      "created_at": "2026-11-15T11:28:15Z",
      "updated_at": "2026-11-15T11:28:15Z"
    },
    {
      "id": 7,
      "machine": "apps",
      "type": "logs",
      "service": "web",
      "unit": null,
      "priority": null,
      "since": "10m",
      "lines": 3,
      "status": "completed",
      "output_cleared": false,
      "created_at": "2026-11-15T11:28:14Z",
      "updated_at": "2026-11-15T11:28:14Z"
    }
  ]
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/machines/{machine}/actions` | `tender actions list MACHINE` |

## Next steps

1. [API reference](api.md) describes the same operations over REST.
2. [CLI reference](cli.md) lists every `tender` command.
3. [Connect an AI client](guides/connect-an-ai-client.md) walks through connecting Claude Code.
4. [Security](security.md) explains what the hub protects and how it handles logs.
