# MCP tools

The hub serves 53 MCP tools at `/mcp`, so an AI client can do everything a person can do with the `tender` command or the REST API.

## Prerequisites

1. A running hub, reachable from the machine where the client runs. See [Install](install.md).
2. An MCP client that supports streamable HTTP, such as Claude Code, or one that runs a local process over stdio on the hub machine.
3. For HTTP, an API token for the client, made with `tender tokens add claude-code`. See [Security](security.md#api-tokens).

## Connect a client

The hub serves MCP over streamable HTTP at `https://apps.your-tailnet.ts.net/mcp`, and the client sends its API token as `Authorization: Bearer <token>`. Without one, or with a wrong one, the hub answers `401`. On the hub machine, a client can also run `tender mcp --stdio` as a local process, which reads the database directly and needs no token. [Connect an AI client](guides/connect-an-ai-client.md) sets up both.

## How the tools behave

1. Every result is JSON. It comes as `structuredContent` and as the same JSON in a text block, for clients that read only text. A list is `{"data": [...]}`, with `next_cursor` when there is another page.
2. A failed call comes back as a tool result with `isError` set and one sentence that says what went wrong and what to do, such as `Machine "nope" doesn't exist. Call list_machines to see the names.` A failure inside the hub says only `The hub failed to read its data.` and the hub logs the cause.
3. Each tool's input schema lists its required inputs and allows no others. A call that leaves out a required input, gives one the wrong type or adds an unknown one fails before the tool runs, with the MCP SDK's `validating "arguments": ...` message. The `... is required` errors below are for an input passed as an empty string.
4. Each tool carries MCP annotations, listed in its entry. `readOnlyHint` is `true` for the tools that only read. Every other tool sets `destructiveHint`, which is `true` for the ones that remove data, cut a daemon off or take a service offline, so the client can ask you first. `idempotentHint` is `true` for the ones you can call again with the same input without changing anything more. Every tool sets `openWorldHint`, and only seven set it to `true`: `send_test_notification`, which sends to the channels, `test_destination`, which reaches a backup destination, `start_update`, which installs a release from GitHub, `upgrade_machine`, which installs packages from the machine's package mirrors, `deploy_service` and `redeploy_service`, which pull images from their registries, and `set_cloudflare_settings`, which checks the token with Cloudflare.
5. The action tools, `deploy_service`, `redeploy_service`, `remove_service`, `start_service`, `stop_service`, `restart_service`, `service_logs`, `get_machine_logs`, `test_destination` and `upgrade_machine`, queue the action for the machine's daemon and wait up to 60 seconds for it. If the daemon hasn't finished by then, the result is the action with the status `pending` or `in_progress`. A `pending` action runs if the daemon picks it up within 15 minutes of being asked, and fails otherwise. An `in_progress` one is running, and fails if the daemon doesn't say how it ended within 15 minutes, or 45 for an upgrade, a deploy or a remove. Check it later with `get_action`.
6. Paged tools take `limit`, from 1 to 500 with a default of 50, and `cursor`. Pass a result's `next_cursor` as `cursor` to get the next, older page.
7. No tool returns destination keys, repository passwords, registry passwords, a service's env values, log sink secrets, the Cloudflare API token or a channel's secret, such as a Slack webhook URL; a secret in a config shows as `"***"`. No tool makes an API token. The only secrets a tool returns are the ones made for you, shown once: a new machine token, a destination password the hub generated, a webhook sink secret the hub generated, and a webhook channel's signing secret.
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
| `POST /v1/backups/destinations/{destination}/test` | `test_destination` |
| `DELETE /v1/backups/destinations/{destination}` | `remove_destination` |
| `GET /v1/notifications` | `list_notifications` |
| `GET /v1/machines/{machine}/notifications` | `list_notifications` |
| `GET /v1/machines/{machine}/notifications/{id}` | `list_notifications` |
| `GET /v1/notifications/channels` | `list_channels` |
| `POST /v1/notifications/channels` | `add_channel` |
| `GET /v1/notifications/channels/{channel}` | `list_channels` |
| `PATCH /v1/notifications/channels/{channel}` | `update_channel` |
| `DELETE /v1/notifications/channels/{channel}` | `remove_channel` |
| `POST /v1/notifications/test` | `send_test_notification` |
| `GET /v1/services` | `list_services` |
| `GET /v1/machines/{machine}/services` | `list_services` |
| `POST /v1/machines/{machine}/services` | `deploy_service` |
| `GET /v1/machines/{machine}/services/{service}` | `get_service` |
| `PATCH /v1/machines/{machine}/services/{service}` | `set_service_alerts`, `deploy_service` |
| `DELETE /v1/machines/{machine}/services/{service}` | `remove_service` |
| `POST /v1/machines/{machine}/services/{service}/deploy` | `redeploy_service` |
| `POST /v1/machines/{machine}/services/{service}/start` | `start_service` |
| `POST /v1/machines/{machine}/services/{service}/stop` | `stop_service` |
| `POST /v1/machines/{machine}/services/{service}/restart` | `restart_service` |
| `GET /v1/registries` | `list_registries` |
| `POST /v1/registries` | `add_registry` |
| `PATCH /v1/registries/{registry}` | `update_registry` |
| `DELETE /v1/registries/{registry}` | `remove_registry` |
| `POST /v1/machines/{machine}/actions` | `start_service`, `stop_service`, `restart_service`, `service_logs`, `get_machine_logs`, `upgrade_machine` |
| `GET /v1/machines/{machine}/actions` | `list_actions` |
| `GET /v1/machines/{machine}/actions/{id}` | `get_action` |
| `GET /v1/logs/sinks` | `list_log_sinks` |
| `POST /v1/logs/sinks` | `add_log_sink` |
| `GET /v1/logs/sinks/{sink}` | `list_log_sinks` |
| `PATCH /v1/logs/sinks/{sink}` | `update_log_sink` |
| `DELETE /v1/logs/sinks/{sink}` | `remove_log_sink` |
| `GET /metrics` | None. It is for Prometheus scrapers, and the typed tools return the same data. |
| `PATCH /v1/machines/{machine}` | `update_machine` |
| `GET /v1/machines/{machine}/security` | `get_security` |
| `GET /v1/updates` | `get_updates` |
| `GET /v1/updates/settings` | `get_updates` |
| `PUT /v1/updates/settings` | `set_update_settings` |
| `GET /v1/updates/rollouts` | `get_updates` |
| `POST /v1/updates/rollouts` | `start_update` |
| `GET /v1/updates/rollouts/{id}` | `get_updates` |
| `GET /v1/tokens` | `list_tokens` |
| `POST /v1/tokens` | None. An AI client may not make itself more access; a person runs `tender tokens add`. |
| `DELETE /v1/tokens/{token}` | `delete_token` |
| `GET /v1/exposures/cloudflare` | `get_cloudflare_settings` |
| `PUT /v1/exposures/cloudflare` | `set_cloudflare_settings` |
| `DELETE /v1/exposures/cloudflare` | `remove_cloudflare_settings` |
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

### `update_machine`

Changes a machine's settings: its own update mode, its security allowlist and whether its security alerts are on.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `updates` | string | No | `inherit` follows the hub's update mode; `auto` or `manual` is the machine's own. |
| `security_allowed_ports` | array of strings | No | The whole allowlist, which replaces the old one: ports such as `41641/udp` that may listen on a public address without a `security.port_exposed` notification. |
| `security_alerts` | boolean | No | Whether the machine's security alerts open. |

#### Returns

The machine, as `list_machines` shows it.

#### Limits

At most 100 allowed ports.

#### Example call

```json
{"name": "update_machine", "arguments": {"machine": "apps", "security_allowed_ports": ["41641/udp"]}}
```

#### Example result

```json
{
  "id": 1,
  "name": "apps",
  "version": "0.3.1",
  "updates": "inherit",
  "security_allowed_ports": ["41641/udp"],
  "security_alerts": true,
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T11:42:10Z",
  "last_seen": "2026-11-15T11:41:35Z",
  "usage": null
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `updates must be inherit, auto or manual.`
3. `"41641" is not a port. Write each port with its protocol, such as 41641/udp or 443/tcp.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `PATCH /v1/machines/{machine}` | `tender machines set NAME --updates MODE --allow-port PORT` |

## Security

### `get_security`

Gets the latest security checks of a machine, which its daemon runs when it starts and every 15 minutes, on Debian and Ubuntu. [Security](security.md#security-checks) explains each check.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |

#### Returns

`id`, `machine`, and one object per check, each with `check` (`ok`, `error` or `unsupported`) and `error`. `firewall` has `tool`, `active` and `default_incoming`. `listeners.ports` has every socket on a public address with `protocol`, `address`, `port`, `process`, `docker`, `service` and `allowed`. `ssh` has `installed`, `password_authentication` and `permit_root_login`. `updates` has `pending`, `security` and `unattended_upgrades`. `reboot` has `required` and `since`. Then `security_updates_since`, `collected_at`, `created_at` and `updated_at`.

#### Limits

None.

#### Example call

```json
{"name": "get_security", "arguments": {"machine": "apps"}}
```

#### Example result

```json
{
  "id": 1,
  "machine": "apps",
  "firewall": {"check": "ok", "error": null, "tool": "ufw", "active": true, "default_incoming": "deny"},
  "listeners": {
    "check": "ok",
    "error": null,
    "ports": [
      {"protocol": "tcp", "address": "0.0.0.0", "port": 8080, "process": null, "docker": true, "service": "web", "allowed": false}
    ]
  },
  "ssh": {"check": "ok", "error": null, "installed": true, "password_authentication": false, "permit_root_login": "prohibit-password"},
  "updates": {"check": "ok", "error": null, "pending": 3, "security": 1, "unattended_upgrades": true},
  "reboot": {"check": "ok", "error": null, "required": false, "since": null},
  "collected_at": "2026-11-15T11:40:00Z",
  "security_updates_since": "2026-11-15T09:40:00Z",
  "created_at": "2026-11-14T08:00:00Z",
  "updated_at": "2026-11-15T11:40:01Z"
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `This machine hasn't sent a security report yet. Its daemon sends one when it starts and every 15 minutes, from Tender v0.3.0 on.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/machines/{machine}/security` | `tender security MACHINE` |

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
          "config": {
            "access_key_id": "***",
            "access_key_id_set": true,
            "bucket": "my-backups",
            "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
            "path": "tender",
            "secret_access_key": "***",
            "secret_access_key_set": true
          },
          "location": "s3:https://<account-id>.r2.cloudflarestorage.com/my-backups/tender",
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
    "config": {
      "access_key_id": "***",
      "access_key_id_set": true,
      "bucket": "my-backups",
      "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
      "path": "tender",
      "secret_access_key": "***",
      "secret_access_key_set": true
    },
    "location": "s3:https://<account-id>.r2.cloudflarestorage.com/my-backups/tender",
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

A destination has a `type` and a `config` with the type's fields. [Backups](backups.md#destination-types) explains each type, and the [API reference](api.md#destinations) lists their fields.

### `list_destinations`

Lists the places backups go, by name.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

None.

#### Returns

`data`, the destinations, each with `id`, `name`, `type` (`s3`, `local`, `sftp`, `rest` or `rclone`), `config`, `location` and the timestamps. In `config`, each secret shows as `"***"`, with `<field>_set` saying whether it is set. `location` is the restic repository; each machine's repository is a folder named after the machine in it. Passwords are never shown.

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
      "config": {
        "access_key_id": "***",
        "access_key_id_set": true,
        "bucket": "my-backups",
        "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
        "path": "tender",
        "secret_access_key": "***",
        "secret_access_key_set": true
      },
      "location": "s3:https://<account-id>.r2.cloudflarestorage.com/my-backups/tender",
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

Adds a place to send backups. Each machine gets its own folder in it.

The keys and the password you pass go through the AI conversation, and so to the client's model provider. For real keys, run `tender destinations add` instead, which asks for them with typing hidden. The hub stores them encrypted, and no tool, route or log shows them again. The `sftp` and `rclone` types take no secrets, because their logins stay on each machine.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `false` | `false` |

A second call with the same name fails.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | The new destination's name: lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |
| `type` | string | No | `s3`, the default, `local`, `sftp`, `rest` or `rclone`. |
| `config` | object | Yes | The type's fields, as the [API reference](api.md#destinations) lists them, such as `endpoint`, `bucket`, `path`, `access_key_id` and `secret_access_key` for `s3`. |
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
    "name": "nas",
    "type": "sftp",
    "config": {"host": "nas.example.com", "user": "tender", "path": "/srv/restic"}
  }
}
```

#### Example result

```json
{
  "id": 2,
  "name": "nas",
  "type": "sftp",
  "config": {
    "host": "nas.example.com",
    "path": "/srv/restic",
    "port": 22,
    "user": "tender"
  },
  "location": "sftp://tender@nas.example.com:22//srv/restic",
  "created_at": "2026-11-15T11:30:02Z",
  "updated_at": "2026-11-15T11:30:02Z",
  "password": "<password>"
}
```

#### Errors

1. `Destination names are lowercase letters, digits and hyphens, up to 63 characters.`
2. `type must be one of local, rclone, rest, s3, sftp.`
3. `config is required: the sftp destination's host, port, user, path.`
4. `config.host is required: ...`, for any required field, with what it holds.
5. `config has region, which a s3 destination doesn't take. It takes endpoint, bucket, path, access_key_id, secret_access_key.`
6. A sentence naming the field that isn't valid, such as `config.endpoint must be a URL such as https://<account>.r2.cloudflarestorage.com.` or `config.username and config.password go together: give both, or neither for a server without logins.`
7. `config.secret_access_key is "***", which is how the hub hides a secret. Give the secret itself.`
8. `A destination with this name or this location (sftp://tender@nas.example.com:22//srv/restic) already exists. Choose another name, or use the existing one.`
9. `This process has no TENDER_ENCRYPTION_KEY, so it can't store or read destination credentials. Set it to the hub's key and run the command again.` This happens only over stdio.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/backups/destinations` | `tender destinations add NAME [--type TYPE] [OPTIONS]` |

### `update_destination`

Changes fields of a destination's config. Anything you leave out stays as it is.

New keys you pass go through the AI conversation. The type and the password can't change, because the repositories depend on them.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | The destination's name, as `list_destinations` shows it. |
| `config` | object | No | The fields to change. `null` removes an optional field, and a secret given as `"***"` stays as it is, so you can pass back what `list_destinations` shows. |
| `password` | string | No | Not accepted. A call with it fails. |
| `type` | string | No | Not accepted. A call with it fails. |

#### Returns

The destination, as `list_destinations` shows it.

#### Limits

None. Daemons pick up the new config within 10 minutes. Move the data yourself before you point a destination at another place.

#### Example call

```json
{"name": "update_destination", "arguments": {"destination": "r2", "config": {"path": "tender-2026"}}}
```

#### Example result

```json
{
  "id": 1,
  "name": "r2",
  "type": "s3",
  "config": {
    "access_key_id": "***",
    "access_key_id_set": true,
    "bucket": "my-backups",
    "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
    "path": "tender-2026",
    "secret_access_key": "***",
    "secret_access_key_set": true
  },
  "location": "s3:https://<account-id>.r2.cloudflarestorage.com/my-backups/tender-2026",
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T12:02:41Z"
}
```

#### Errors

1. `Destination "r2" doesn't exist.`
2. `The password can't change: every repository at this destination is encrypted with it. Add a new destination with the new password and move the machines to it.`
3. `The type can't change: the repositories are where the type put them. Add a new destination of the other type and move the machines to it.`
4. The sentences of `add_destination` for a config that isn't valid, such as `config.bucket is required: ...` after `{"bucket": null}`.
5. `Another destination already uses s3:https://<account-id>.r2.cloudflarestorage.com/my-backups/tender-2026. Use that one, or choose another location.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `PATCH /v1/backups/destinations/{destination}` | `tender destinations update NAME [OPTIONS]` |

### `test_destination`

Tries a destination from one machine, through the daemon there. The daemon opens that machine's repository at the destination with `restic cat config`, which checks the address, the login and the password. When there is no repository yet, it makes one with `restic init`, as the first backup would. Call it after `add_destination` and before `set_backup`.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `true` |

It reaches a server outside the hub, and a second call finds the repository the first one made.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | The destination's name, as `list_destinations` shows it. |
| `machine` | string | Yes | The machine whose daemon tries it, as `list_machines` shows it. |

#### Returns

The action, with the type `test` and the status `completed`, or `pending` or `in_progress` if the daemon hasn't finished within 60 seconds. `output` says which repository the daemon reached and whether it made it.

#### Limits

The tool waits up to 60 seconds. An action the daemon doesn't pick up within 15 minutes fails.

#### Example call

```json
{"name": "test_destination", "arguments": {"destination": "nas", "machine": "apps"}}
```

#### Example result

```json
{
  "id": 12,
  "machine": "apps",
  "type": "test",
  "service": null,
  "container": null,
  "unit": null,
  "priority": null,
  "since": null,
  "lines": null,
  "destination": "nas",
  "version": null,
  "packages": null,
  "delete_data": null,
  "status": "completed",
  "output_cleared": false,
  "created_at": "2026-11-15T12:02:11Z",
  "updated_at": "2026-11-15T12:02:13Z",
  "output": "Reached sftp://tender@nas.example.com:22//srv/restic/apps from apps. It had no repository for apps yet, so the test made one with the destination's password.\n"
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `destination is required. Call list_destinations to see the names.`
3. `Destination "nas" doesn't exist. Call list_destinations to see the names.`
4. `Could not reach the destination nas from apps: ...`, with the reason, when the daemon tried and failed. The reason is restic's, or what the machine lacks, such as `the sftp destination needs /etc/tender/ssh/id_ed25519 on this machine, and it isn't there`.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/backups/destinations/{destination}/test`, then `GET /v1/machines/{machine}/actions/{id}` | `tender destinations test NAME MACHINE` |

### `remove_destination`

Removes a destination that no machine backs up to.

> [!WARNING]
> The hub forgets the destination's keys and password. The backups already at the destination stay there, so keep the password in your password manager to restore them.

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

Lists notifications, newest first, optionally of one machine, status or severity, or only the open problems. A notification is one message: a problem adds one when it starts, such as `service.down`, one when it gets worse, `service.down.escalated`, and one when it clears, `service.down.resolved`.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | No | Only this machine's notifications. Leave it out for every machine. |
| `open` | boolean | No | `true` returns only the open problems: the latest notification of each problem that hasn't resolved. |
| `status` | string | No | Only notifications with this status: `pending`, `sent` or `failed`. |
| `severity` | string | No | Only notifications with this severity: `info`, `warning` or `critical`. |
| `limit` | integer | No | The most notifications to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | `next_cursor` from the previous page. |

#### Returns

`data`, the notifications, each with `id`, `machine` (the machine it is about), `service` (the Docker service it is about, else `null`), `channel` (the channel a `channel.failing` notification is about, else `null`), `event`, `severity`, `message` (what happened, in one sentence), `status` (`pending`; `sent` once a channel delivered it; `failed` once every channel failed), `sent_at`, the timestamps, and `deliveries`: one per channel, with `channel`, `status` (`pending`, `delivered` or `failed`), `attempts` and `error`. [Notifications](notifications.md) explains the statuses and lists the events.

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
      "service": "notes",
      "container": "web",
      "channel": null,
      "event": "service.memory_high",
      "severity": "warning",
      "message": "web in notes on apps uses 950 MiB, 92% of its 1 GiB memory limit.",
      "status": "sent",
      "sent_at": "2026-11-15T11:35:36Z",
      "created_at": "2026-11-15T11:35:35Z",
      "updated_at": "2026-11-15T11:35:36Z",
      "deliveries": [
        {
          "id": 7,
          "notification_id": 3,
          "channel": "ops",
          "status": "delivered",
          "attempts": 1,
          "error": null,
          "created_at": "2026-11-15T11:35:35Z",
          "updated_at": "2026-11-15T11:35:36Z"
        }
      ]
    }
  ],
  "next_cursor": "MTc5NDc0MjUzNToz"
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `status must be pending, sent or failed.`
3. `severity must be info, warning or critical.`
4. `limit must be a whole number from 1 to 500.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/notifications`, `GET /v1/machines/{machine}/notifications` and `GET /v1/machines/{machine}/notifications/{id}` | `tender notifications list`, and `tender status`, which shows the open problems. |

### `list_channels`

Lists the channels notifications go to, in the order they were added.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

None.

#### Returns

`data`, the channels, each with `id`, `name`, `type` (`email`, `slack`, `webhook` or `exec`), `config`, `enabled`, `min_severity` (the least severe notification it gets), `events` (the events it gets, such as `["*"]` or `["service.*", "backup.failed"]`) and the timestamps. A secret in `config` shows as `"***"`, with `<name>_set` set to `true`.

#### Limits

None. The list isn't paged.

#### Example call

```json
{"name": "list_channels", "arguments": {}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 1,
      "name": "ops",
      "type": "email",
      "config": {"to": "ops@example.com"},
      "enabled": true,
      "min_severity": "warning",
      "events": ["*"],
      "created_at": "2026-11-15T11:27:26Z",
      "updated_at": "2026-11-15T11:27:26Z"
    },
    {
      "id": 2,
      "name": "chat",
      "type": "slack",
      "config": {"webhook_url": "***", "webhook_url_set": true},
      "enabled": true,
      "min_severity": "critical",
      "events": ["*"],
      "created_at": "2026-11-15T11:28:02Z",
      "updated_at": "2026-11-15T11:28:02Z"
    }
  ]
}
```

#### Errors

None besides the hub's own failure.

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/notifications/channels` and `GET /v1/notifications/channels/{channel}` | `tender channels list`, `tender channels show NAME` |

### `add_channel`

Adds a channel that gets every notification of its `events` at least as severe as its `min_severity`, from now on.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `false` | `false` |

A second call with the same name fails.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | Lowercase letters, digits and hyphens, such as `ops-slack`. |
| `type` | string | Yes | `email`, `slack`, `webhook` or `exec`. |
| `config` | object | Yes | The type's config, all strings: `{"to": "ops@example.com"}`, `{"webhook_url": "https://hooks.slack.com/services/..."}`, `{"url": "https://..."}` or `{"command": "my-adapter"}`. [Adapters](adapters.md#channel-types) explains each. |
| `enabled` | boolean | No | Whether it gets notifications. The default is `true`. |
| `min_severity` | string | No | `info`, `warning` or `critical`. The default is `warning`. |
| `events` | array | No | The events it gets: `*` for every one, an area such as `service.*`, or an event such as `backup.failed`. The default is `["*"]`. |

#### Returns

The channel, as `list_channels` shows it. For a `webhook` channel, `config.secret` is the signing secret the hub made, shown this once: pass it to whoever runs the receiver. Call `send_test_notification` to check that notifications arrive.

#### Limits

An exec channel's `command` names a program root installed in `/etc/tender/adapters` on the hub. The tool can't supply a path or arguments.

#### Example call

```json
{"name": "add_channel", "arguments": {"name": "pager", "type": "webhook", "config": {"url": "https://pager.example.com/tender"}}}
```

#### Example result

```json
{
  "id": 3,
  "name": "pager",
  "type": "webhook",
  "config": {"secret": "whsec_kq0W2jq7a8Lx3n8vR1tYb5sU9eFh4cGd6pJm0zXwQ2E", "secret_set": true, "url": "https://pager.example.com/tender"},
  "enabled": true,
  "min_severity": "warning",
  "events": ["*"],
  "created_at": "2026-11-15T11:30:11Z",
  "updated_at": "2026-11-15T11:30:11Z"
}
```

#### Errors

1. `A channel called pager already exists. Choose another name, or change that one.`
2. `type must be one of email, exec, slack, webhook.`
3. `to must be one bare address such as ops@example.com.`
4. `command must be the name of a program in /etc/tender/adapters, such as my-adapter: letters, digits, dots, dashes and underscores, with no slash and no "..".`
5. `Leave secret out: the hub makes it and shows it once.`
6. `webhook_url must be a Slack incoming webhook URL, which starts with https://hooks.slack.com/. Copy it from the app's Incoming Webhooks page.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/notifications/channels` | `tender channels add NAME --type TYPE ...` |

### `update_channel`

Changes a channel's config, turns it on or off, or changes its `min_severity` or `events`. Anything left out stays as it is.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `channel` | string | Yes | The channel's name. |
| `config` | object | No | Config keys to change. A secret can be replaced, never read. |
| `enabled` | boolean | No | `true` to turn it on, `false` to turn it off. |
| `min_severity` | string | No | `info`, `warning` or `critical`. |
| `events` | array | No | The events it gets, replacing the list. |

#### Returns

The channel, as `list_channels` shows it. Turning a channel off drops what it still owed.

#### Limits

`name` and `type` can't change. Remove the channel and add it again instead.

#### Example call

```json
{"name": "update_channel", "arguments": {"channel": "chat", "min_severity": "warning"}}
```

#### Example result

```json
{
  "id": 2,
  "name": "chat",
  "type": "slack",
  "config": {"webhook_url": "***", "webhook_url_set": true},
  "enabled": true,
  "min_severity": "warning",
  "events": ["*"],
  "created_at": "2026-11-15T11:28:02Z",
  "updated_at": "2026-11-15T11:41:17Z"
}
```

#### Errors

1. `Channel "chat" doesn't exist. List the channels to see their names.`
2. `config.webhook_url is a secret, which the API never shows. Send its new value, or leave it out to keep it.`
3. `min_severity must be info, warning or critical.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `PATCH /v1/notifications/channels/{channel}` | `tender channels update NAME ...` |

### `remove_channel`

Removes a channel, so it gets no more notifications, with its record of deliveries.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `channel` | string | Yes | The channel's name. |

#### Returns

`removed`, naming the channel that was removed. Notifications are still recorded when no channel is left.

#### Limits

None.

#### Example call

```json
{"name": "remove_channel", "arguments": {"channel": "chat"}}
```

#### Example result

```json
{"removed": "channel chat"}
```

#### Errors

1. `Channel "chat" doesn't exist. List the channels to see their names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `DELETE /v1/notifications/channels/{channel}` | `tender channels remove NAME` |

### `send_test_notification`

Sends a test notification, event `notification.test`, to every enabled channel, or to one, to check that notifications arrive.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `false` | `true` |

It sends to the channels, which reaches outside the hub. Each call sends again.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `channel` | string | No | Only this channel, even if it is off. Leave it out for every enabled channel. |

#### Returns

`data`, one delivery per channel, with `id`, `notification_id` (the test's notification), `channel`, `status` (`delivered` or `failed`), `attempts`, `error`, which says why one failed, and the timestamps. A test that gets through resolves the channel's `channel.failing` problem.

#### Limits

Each channel is tried once, all at once, for at most 10 seconds, or 30 for email.

#### Example call

```json
{"name": "send_test_notification", "arguments": {}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 9,
      "notification_id": 5,
      "channel": "ops",
      "status": "delivered",
      "attempts": 1,
      "error": null,
      "created_at": "2026-11-15T11:42:00Z",
      "updated_at": "2026-11-15T11:42:01Z"
    }
  ]
}
```

#### Errors

1. `There are no enabled channels to test. Add one first, such as an email channel to ops@example.com.`
2. `Channel "pager" doesn't exist. List the channels to see their names.`

A channel that fails is not an error: its delivery says `failed`, with the reason, such as `Email is off on this hub. Set TENDER_SMTP_HOST and TENDER_SMTP_FROM in /etc/tender/hub.env and restart the hub.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/notifications/test` | `tender channels test [NAME]` |

## Services

A service is a compose project on a machine, or a container without one. Each tool that returns a service returns these fields: `id`, `machine`, `name`, `managed` (`true` when Tender deployed it), `state` (`running`, `degraded`, `stopped` or `unknown`), `deploy_status` (`deploying`, `deployed`, `failed` or `removing`, or `null` when unmanaged), `last_error` (why the last deploy or remove failed), `deployed_at`, `action_id`, `compose`, `files`, `env` (every key, each value `"***"`), `routes`, `alerts_enabled`, `containers` and the timestamps. Each route has `container`, `port`, `expose`, `host`, `https_port`, `local_port` and `url`. Each container has `name` (its compose service, such as `web`), `runtime`, `container_id`, `image`, `state`, `health`, `exit_code`, `restart_count`, `restart_policy`, `started_at`, `memory_bytes`, `memory_limit_bytes`, `cpu_percent`, `stopped_by_tender`, `removed_at` and the timestamps. [Services](services.md#what-a-service-is) explains each field, and [Exposure](exposure.md#routes) the routes.

The deploy tools, `deploy_service`, `redeploy_service` and `remove_service`, queue an action for the machine's daemon and wait up to 60 seconds for it. A deploy that takes longer, such as one that pulls large images or waits for health checks, comes back with the action `pending` or `in_progress`; the hub gives it 45 minutes. Check it with `get_action`, and the service with `get_service`.

### `list_services`

Lists the services on every machine or one, with whether each machine's Docker answers.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | No | Only this machine's services. Leave it out for every machine. |

#### Returns

`data`, one entry per machine, with `machine`, `docker` (`running`, `unreachable` when its daemon can't reach Docker, or `null` before any report and once Docker is turned off on the machine), `docker_reason` (while unreachable, `socket_missing`, `permission_denied`, `refused`, `timeout` or `error`, else `null`) and `data`, its services by name. Usage is from the latest report. A `removed` container can't be started, stopped, restarted or read, and goes 7 days after `removed_at`, as [Removed containers](services.md#removed-containers) explains.

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
          "id": 1,
          "machine": "apps",
          "name": "notes",
          "managed": true,
          "state": "running",
          "deploy_status": "deployed",
          "last_error": null,
          "deployed_at": "2026-11-15T10:32:40Z",
          "action_id": 12,
          "compose": "services:\n  web:\n    image: ghcr.io/me/notes:1.4\n    mem_limit: 512m\n  migrate:\n    image: ghcr.io/me/notes:1.4\n    command: migrate\n    restart: \"no\"\n    mem_limit: 256m\n",
          "files": {},
          "env": {
            "SECRET_KEY": "***"
          },
          "routes": [
            {
              "id": 1,
              "container": "web",
              "port": 8080,
              "expose": "tailscale",
              "host": null,
              "https_port": 443,
              "local_port": 20000,
              "url": "https://apps.tail1234.ts.net",
              "created_at": "2026-11-15T10:31:49Z",
              "updated_at": "2026-11-15T10:31:49Z"
            }
          ],
          "alerts_enabled": true,
          "containers": [
            {
              "id": 3,
              "name": "migrate",
              "runtime": "docker",
              "container_id": "c0ffee03",
              "image": "ghcr.io/me/notes:1.4",
              "state": "exited",
              "health": null,
              "exit_code": 0,
              "restart_count": 0,
              "restart_policy": "no",
              "started_at": null,
              "memory_bytes": null,
              "memory_limit_bytes": null,
              "cpu_percent": null,
              "stopped_by_tender": false,
              "removed_at": null,
              "created_at": "2026-11-15T10:33:00Z",
              "updated_at": "2026-11-15T11:36:35Z"
            },
            {
              "id": 1,
              "name": "web",
              "runtime": "docker",
              "container_id": "c0ffee04",
              "image": "ghcr.io/me/notes:1.4",
              "state": "running",
              "health": "healthy",
              "exit_code": null,
              "restart_count": 0,
              "restart_policy": "unless-stopped",
              "started_at": "2026-11-15T10:32:35Z",
              "memory_bytes": 209715200,
              "memory_limit_bytes": 536870912,
              "cpu_percent": 12.5,
              "stopped_by_tender": false,
              "removed_at": null,
              "created_at": "2026-11-15T10:33:00Z",
              "updated_at": "2026-11-15T11:36:35Z"
            }
          ],
          "created_at": "2026-11-15T10:31:49Z",
          "updated_at": "2026-11-15T10:32:40Z"
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
| `GET /v1/services` and `GET /v1/machines/{machine}/services` | `tender services list [MACHINE]` |

### `get_service`

Gets one service on a machine, with its routes and containers.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, as `list_services` shows it. |

#### Returns

The service.

#### Limits

None.

#### Example call

```json
{"name": "get_service", "arguments": {"machine": "apps", "service": "redis"}}
```

#### Example result

```json
{
  "id": 2,
  "machine": "apps",
  "name": "redis",
  "managed": false,
  "state": "running",
  "deploy_status": null,
  "last_error": null,
  "deployed_at": null,
  "action_id": null,
  "compose": null,
  "files": {},
  "env": {},
  "routes": [],
  "alerts_enabled": true,
  "containers": [
    {
      "id": 2,
      "name": "redis",
      "runtime": "docker",
      "container_id": "c0ffee05",
      "image": "redis:7",
      "state": "running",
      "health": null,
      "exit_code": null,
      "restart_count": 0,
      "restart_policy": "always",
      "started_at": "2026-11-15T10:32:35Z",
      "memory_bytes": 8388608,
      "memory_limit_bytes": null,
      "cpu_percent": 0.4,
      "stopped_by_tender": false,
      "removed_at": null,
      "created_at": "2026-11-15T10:33:00Z",
      "updated_at": "2026-11-15T11:36:35Z"
    }
  ],
  "created_at": "2026-11-15T10:33:00Z",
  "updated_at": "2026-11-15T10:33:00Z"
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `service is required. Call list_services to see the names.`
3. `Service "redis" doesn't exist on apps. Call list_services to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/machines/{machine}/services/{service}` | `tender services show MACHINE SERVICE` |

### `deploy_service`

Creates or changes a managed service and deploys it with docker compose, through the machine's daemon. A new service needs `compose`. For one that exists, the inputs given change it and the others stay. The daemon checks the compose file against [the rules](services.md#the-rules) first.

> [!WARNING]
> A deploy recreates the containers that changed, so what they serve is offline while they start.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `false` | `true` |

It pulls images from their registries, which is why `openWorldHint` is `true`.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `name` | string | Yes | The service's name, its compose project: lowercase letters and digits, starting with a letter, at most 32, with no dashes. |
| `compose` | string | For a new service | The text of the docker compose file. Leave it out to keep the stored one. |
| `files` | object | No | Config files the compose file mounts, by path relative to it, such as `config/app.toml`, each with its text. They merge into the stored ones, and `null` removes one. |
| `env` | object | No | Values for the compose file's `${KEY}` interpolation. They merge into the stored ones, and `null` removes one. |
| `routes` | array | No | Every route of the service, each with `container`, `port`, `expose` (`tailscale`, `cloudflare` or `public`), and `host` or `https_port`. They replace the stored ones; leave it out to keep them. |

#### Returns

`service`, the service as it is after the deploy, and `action`, the deploy. Each route's `url` says where it answers.

#### Limits

A service has at most 64 files and 1 MiB of them, 256 env keys, and 64 routes. The compose file is at most 256 KiB. The tool waits up to 60 seconds.

#### Example call

```json
{"name": "deploy_service", "arguments": {"machine": "apps", "name": "blog", "compose": "services:\n  web:\n    image: ghcr.io/me/blog:2\n    mem_limit: 512m\n", "env": {"SECRET_KEY": "s3cret"}, "routes": [{"container": "web", "port": 8080, "expose": "tailscale"}]}}
```

#### Example result

```json
{
  "service": {
    "id": 3,
    "machine": "apps",
    "name": "blog",
    "managed": true,
    "state": "running",
    "deploy_status": "deployed",
    "last_error": null,
    "deployed_at": "2026-11-15T11:41:02Z",
    "action_id": 13,
    "compose": "services:\n  web:\n    image: ghcr.io/me/blog:2\n    mem_limit: 512m\n",
    "files": {},
    "env": {
      "SECRET_KEY": "***"
    },
    "routes": [
      {
        "id": 2,
        "container": "web",
        "port": 8080,
        "expose": "tailscale",
        "host": null,
        "https_port": 10000,
        "local_port": 20001,
        "url": "https://apps.tail1234.ts.net:10000",
        "created_at": "2026-11-15T11:40:20Z",
        "updated_at": "2026-11-15T11:40:20Z"
      }
    ],
    "alerts_enabled": true,
    "containers": [
      {
        "id": 4,
        "name": "web",
        "runtime": "docker",
        "container_id": "c0ffee06",
        "image": "ghcr.io/me/blog:2",
        "state": "running",
        "health": null,
        "exit_code": null,
        "restart_count": 0,
        "restart_policy": "unless-stopped",
        "started_at": "2026-11-15T11:40:58Z",
        "memory_bytes": 104857600,
        "memory_limit_bytes": 536870912,
        "cpu_percent": 1.5,
        "stopped_by_tender": false,
        "removed_at": null,
        "created_at": "2026-11-15T11:41:05Z",
        "updated_at": "2026-11-15T11:41:05Z"
      }
    ],
    "created_at": "2026-11-15T11:40:20Z",
    "updated_at": "2026-11-15T11:41:02Z"
  },
  "action": {
    "id": 13,
    "machine": "apps",
    "type": "deploy",
    "service": "blog",
    "container": null,
    "unit": null,
    "priority": null,
    "since": null,
    "lines": null,
    "destination": null,
    "version": null,
    "packages": null,
    "delete_data": null,
    "status": "completed",
    "output_cleared": false,
    "created_at": "2026-11-15T11:40:20Z",
    "updated_at": "2026-11-15T11:41:02Z",
    "output": " Container blog-web-1  Started\n Container blog-web-1  Healthy\n"
  }
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `compose is required for a new service: the text of its docker compose file.`
3. `name must be lowercase letters and digits, starting with a letter, at most 32, with no dashes: it is the compose project name. Got "my-blog".`
4. `Tender didn't deploy service "redis", so it can't change it: ...` for a service Tender found running.
5. `Host notes.example.com already routes to service notes on apps. A host can route to one service only.`
6. `Could not deploy blog: ...` when the deploy failed, with the rule the compose file broke, such as `service web in the compose file publishes ports, which Tender's routes do instead: remove ports from web and add a route to its port, such as --route web:80:tailscale`, or compose's reason and the end of its output.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/services`, or `PATCH /v1/machines/{machine}/services/{service}` for a service that exists, then `GET /v1/machines/{machine}/actions/{id}` | `tender services deploy MACHINE NAME -f FILE [options]` |

### `redeploy_service`

Deploys a managed service again as it is stored, through the machine's daemon. Compose pulls the images again, so a new image under the same tag runs, and recreates the containers that changed.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `false` | `true` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, as `list_services` shows it. |

#### Returns

`service` and `action`, as `deploy_service` returns them.

#### Limits

The tool waits up to 60 seconds.

#### Example call

```json
{"name": "redeploy_service", "arguments": {"machine": "apps", "service": "blog"}}
```

#### Example result

The same shape as [`deploy_service`](#deploy_service)'s.

#### Errors

1. `Service "blog" doesn't exist on apps. Call list_services to see the names.`
2. `Tender didn't deploy service "redis", so it can't deploy it: ...`
3. `Service "blog" is being removed, so it can't change now. Wait for its remove, action 15, to finish.`
4. `Could not deploy blog: ...` with compose's reason, when the deploy failed.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/services/{service}/deploy`, then `GET /v1/machines/{machine}/actions/{id}` | `tender services redeploy MACHINE SERVICE` |

### `remove_service`

Removes a managed service from a machine, through its daemon. `docker compose down` stops and removes its containers, its routes stop answering, and the hub forgets the service once that succeeded. Its folder, with its named volumes' data, is kept in `/var/lib/tender/services/.removed/` on the machine, unless `delete_data` is `true`.

> [!WARNING]
> `delete_data: true` deletes the service's data, and can't be undone.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `false` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, as `list_services` shows it. |
| `delete_data` | boolean | No | `true` deletes the service's folder and its data. The default, `false`, keeps it aside. |

#### Returns

`service`, as it was before the remove, and `action`, the remove, whose `output` says where the folder was kept.

#### Limits

The tool waits up to 60 seconds.

#### Example call

```json
{"name": "remove_service", "arguments": {"machine": "apps", "service": "blog"}}
```

#### Example result

The same shape as [`deploy_service`](#deploy_service)'s, with an `action` such as:

```json
{
  "id": 15,
  "machine": "apps",
  "type": "remove",
  "service": "blog",
  "container": null,
  "unit": null,
  "priority": null,
  "since": null,
  "lines": null,
  "destination": null,
  "version": null,
  "packages": null,
  "delete_data": null,
  "status": "completed",
  "output_cleared": false,
  "created_at": "2026-11-15T11:50:00Z",
  "updated_at": "2026-11-15T11:50:04Z",
  "output": " Container blog-web-1  Removed\nKept the service's folder, data and all, in /var/lib/tender/services/.removed/blog-1794657004. Delete it when you no longer need it: sudo rm -rf /var/lib/tender/services/.removed/blog-1794657004\n"
}
```

#### Errors

1. `Service "blog" doesn't exist on apps. Call list_services to see the names.`
2. `Tender didn't deploy service "redis", so it can't remove it: ...`
3. `Could not remove blog: ...` with compose's reason, when the remove failed. The hub keeps the service, so you can remove it again.

#### Covers

| REST route | CLI command |
| --- | --- |
| `DELETE /v1/machines/{machine}/services/{service}`, then `GET /v1/machines/{machine}/actions/{id}` | `tender services remove MACHINE SERVICE [--delete-data] --yes` |

### `set_service_alerts`

Turns alerts on or off for one service, all its containers together. Alerts are on for every new service.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, as `list_services` shows it. |
| `enabled` | boolean | Yes | `true` to get alerts for this service's containers, `false` to stop them and resolve its open problems. |

#### Returns

The service. With alerts off, its open problems resolve within a minute. The switch stays when its containers are made again.

#### Limits

None.

#### Example call

```json
{"name": "set_service_alerts", "arguments": {"machine": "apps", "service": "redis", "enabled": false}}
```

#### Example result

The service, as [`get_service`](#get_service) returns it, with `"alerts_enabled": false`.

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `service is required. Call list_services to see the names.`
3. `Service "redis" doesn't exist on apps. Call list_services to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `PATCH /v1/machines/{machine}/services/{service}` | `tender services alerts MACHINE SERVICE on\|off` |

## Registries

A registry is the login of a container registry that managed services pull private images from. A deploy uses the login of the registry whose host an image is on. A registry has `id`, `name`, `host`, `username`, `password` (always `"***"`), `password_set` and the timestamps.

### `list_registries`

Lists the registries, by name.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

None.

#### Returns

`data`, the registries.

#### Limits

None. The list isn't paged.

#### Example call

```json
{"name": "list_registries", "arguments": {}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 1,
      "name": "ghcr",
      "host": "ghcr.io",
      "username": "me",
      "password": "***",
      "password_set": true,
      "created_at": "2026-11-15T09:00:00Z",
      "updated_at": "2026-11-15T09:00:00Z"
    }
  ]
}
```

#### Errors

None besides the hub's own.

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/registries` | `tender registries list` |

### `add_registry`

Adds the login of a container registry, such as `ghcr.io`. The hub stores the password encrypted and gives it only to a daemon that deploys a service.

> [!WARNING]
> The password goes through the conversation, to the client's model provider. For a real one, type it into `tender registries add` instead.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `false` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | Lowercase letters, digits and dashes, at most 63. |
| `host` | string | Yes | The registry's host, as image names start with it, such as `ghcr.io`; `docker.io` for Docker Hub. |
| `username` | string | Yes | The login name. |
| `password` | string | Yes | The password or access token. |

#### Returns

The registry, with `password` as `"***"`.

#### Limits

None.

#### Example call

```json
{"name": "add_registry", "arguments": {"name": "ghcr", "host": "ghcr.io", "username": "me", "password": "ghp_example"}}
```

#### Example result

```json
{
  "id": 1,
  "name": "ghcr",
  "host": "ghcr.io",
  "username": "me",
  "password": "***",
  "password_set": true,
  "created_at": "2026-11-15T09:00:00Z",
  "updated_at": "2026-11-15T09:00:00Z"
}
```

#### Errors

1. `Registry "ghcr" already exists. Change it with tender registries update.`
2. `Registry "ghcr" already has the host ghcr.io; a host has one login.`
3. `password is "***", which is how the hub hides a secret. Give the password itself.`
4. `This process has no TENDER_ENCRYPTION_KEY, ...` when the hub can't store the password.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/registries` | `tender registries add NAME --host HOST --username USER` |

### `update_registry`

Changes a registry's host, username or password. The inputs left out stay.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | The registry's name, as `list_registries` shows it. |
| `host` | string | No | A new host. |
| `username` | string | No | A new login name. |
| `password` | string | No | A new password or access token. |

#### Returns

The registry.

#### Limits

None.

#### Example call

```json
{"name": "update_registry", "arguments": {"name": "ghcr", "username": "ci"}}
```

#### Example result

The registry, as `add_registry` returns it, with the new values.

#### Errors

1. `Registry "ghcr" doesn't exist. Run tender registries list to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `PATCH /v1/registries/{registry}` | `tender registries update NAME [--host HOST] [--username USER] [--password]` |

### `remove_registry`

Removes a registry's login. Services whose images are on it can no longer pull them, unless they are public.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | The registry's name, as `list_registries` shows it. |

#### Returns

What was removed.

#### Limits

None.

#### Example call

```json
{"name": "remove_registry", "arguments": {"name": "ghcr"}}
```

#### Example result

```json
{"removed": "registry ghcr"}
```

#### Errors

1. `Registry "ghcr" doesn't exist. Run tender registries list to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `DELETE /v1/registries/{registry}` | `tender registries remove NAME` |

## Actions

The tools in this section queue an action for a machine's daemon and wait up to 60 seconds for it, or read actions back. [Services](services.md#how-actions-reach-a-machine) explains how an action reaches the machine. Each returns the action: `id`, `machine`, `type` (`start`, `stop`, `restart`, `logs`, `test`, `upgrade`, `update` from a rollout, or `deploy` and `remove` from the [deploy tools](#services)), `service` (`null` for the machine's logs, a test, an upgrade or an update), `container` (the one container of the service it is for, or `null` for all), `unit`, `priority`, `since` and `lines` (logs only), `destination` (tests only, from [`test_destination`](#test_destination)), `packages` (upgrade only), `version` (update only), `delete_data` (remove only), `status` (`pending`, `in_progress`, `completed` or `failed`), `output`, `output_cleared` and the timestamps. The hub keeps each machine's newest 100 finished actions, and every `pending` or `in_progress` one.

### `start_service`

Starts a stopped service, or one of its containers, through the daemon on its machine: a service Tender deployed with `docker compose start`, any other container by container. Starting a running container changes nothing.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, as `list_services` shows it. |
| `container` | string | No | Only this container of the service, such as `web`. Leave it out for every container. |

#### Returns

The action, with the status `completed`, or `pending` or `in_progress` if the daemon hasn't finished within 60 seconds. `output` is `null`. A start clears its containers' `stopped_by_tender`.

#### Limits

The tool waits up to 60 seconds. An action the daemon doesn't pick up within 15 minutes fails.

#### Example call

```json
{"name": "start_service", "arguments": {"machine": "apps", "service": "notes", "container": "web"}}
```

#### Example result

```json
{
  "id": 3,
  "machine": "apps",
  "type": "start",
  "service": "notes",
  "container": "web",
  "unit": null,
  "priority": null,
  "since": null,
  "lines": null,
  "destination": null,
  "version": null,
  "packages": null,
  "delete_data": null,
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
3. `Service "notes" doesn't exist on apps. Call list_services to see the names.`
4. `Container "nope" isn't in service notes. Its containers are: web, worker.`
5. `Container web in notes was removed: Docker on this machine no longer has it.`
6. `Could not start web in notes: ...`, with Docker's or compose's reason, when the daemon tried and failed.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/services/{service}/start`, or `POST /v1/machines/{machine}/actions` with `{"type": "start", "service": "notes"}`, then `GET /v1/machines/{machine}/actions/{id}` | `tender services start MACHINE SERVICE [--container C]` |

### `stop_service`

Stops a running service, or one of its containers, through the daemon on its machine, with each container's own stop timeout.

> [!WARNING]
> Whatever the service serves goes offline until it is started again. A container Tender stopped doesn't alert as down, so nobody gets an email about it.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, as `list_services` shows it. |
| `container` | string | No | Only this container of the service, such as `web`. Leave it out for every container. |

#### Returns

The action, with the status `completed`, or `pending` or `in_progress` if the daemon hasn't finished within 60 seconds. `output` is `null`. Each container it stopped shows `stopped_by_tender: true` until something starts it.

#### Limits

The tool waits up to 60 seconds. An action the daemon doesn't pick up within 15 minutes fails.

#### Example call

```json
{"name": "stop_service", "arguments": {"machine": "apps", "service": "notes", "container": "web"}}
```

#### Example result

```json
{
  "id": 2,
  "machine": "apps",
  "type": "stop",
  "service": "notes",
  "container": "web",
  "unit": null,
  "priority": null,
  "since": null,
  "lines": null,
  "destination": null,
  "version": null,
  "packages": null,
  "delete_data": null,
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
3. `Service "notes" doesn't exist on apps. Call list_services to see the names.`
4. `Container "nope" isn't in service notes. Its containers are: web, worker.`
5. `Container web in notes was removed: Docker on this machine no longer has it.`
6. `Could not stop web in notes: ...`, with Docker's or compose's reason, when the daemon tried and failed.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/services/{service}/stop`, or `POST /v1/machines/{machine}/actions` with `{"type": "stop", "service": "notes"}`, then `GET /v1/machines/{machine}/actions/{id}` | `tender services stop MACHINE SERVICE [--container C]` |

### `restart_service`

Restarts a service, or one of its containers, through the daemon on its machine.

> [!WARNING]
> Whatever the service serves is offline while it restarts.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `false` | `false` |

Each call restarts the containers again.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `service` | string | Yes | The service's name, as `list_services` shows it. |
| `container` | string | No | Only this container of the service, such as `web`. Leave it out for every container. |

#### Returns

The action, with the status `completed`, or `pending` or `in_progress` if the daemon hasn't finished within 60 seconds. `output` is `null`.

#### Limits

The tool waits up to 60 seconds. An action the daemon doesn't pick up within 15 minutes fails.

#### Example call

```json
{"name": "restart_service", "arguments": {"machine": "apps", "service": "notes", "container": "web"}}
```

#### Example result

```json
{
  "id": 1,
  "machine": "apps",
  "type": "restart",
  "service": "notes",
  "container": "web",
  "unit": null,
  "priority": null,
  "since": null,
  "lines": null,
  "destination": null,
  "version": null,
  "packages": null,
  "delete_data": null,
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
3. `Service "notes" doesn't exist on apps. Call list_services to see the names.`
4. `Container "nope" isn't in service notes. Its containers are: web, worker.`
5. `Container web in notes was removed: Docker on this machine no longer has it.`
6. `Could not restart web in notes: ...`, with Docker's or compose's reason, when the daemon tried and failed.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/services/{service}/restart`, or `POST /v1/machines/{machine}/actions` with `{"type": "restart", "service": "notes"}`, then `GET /v1/machines/{machine}/actions/{id}` | `tender services restart MACHINE SERVICE [--container C]` |

### `service_logs`

Gets the newest lines of output of a service's containers, or of one of them, stdout and stderr together, each line starting with Docker's timestamp. With several containers, their lines are merged by time, each with its container's name and `|` after the timestamp.

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
| `service` | string | Yes | The service's name, as `list_services` shows it. |
| `container` | string | No | Only this container of the service, such as `web`. Leave it out for every container. |
| `lines` | integer | No | How many of the newest lines, from 1 to 2000. The default is 200. |
| `since` | string | No | Only lines from this far back, such as `30m`, `1h` or `7d`, at most `7d`. Leave it out for no limit. |

#### Returns

The action, with the logs in `output`, the oldest line first. An hour after the action completed, `output` is `null` and `output_cleared` is `true`.

#### Limits

At most 2000 lines and 256 KiB. Past 256 KiB, the oldest lines are dropped. The tool waits up to 60 seconds.

#### Example call

```json
{"name": "service_logs", "arguments": {"machine": "apps", "service": "notes", "lines": 2}}
```

#### Example result

```json
{
  "id": 5,
  "machine": "apps",
  "type": "logs",
  "service": "notes",
  "container": null,
  "unit": null,
  "priority": null,
  "since": null,
  "lines": 2,
  "destination": null,
  "version": null,
  "packages": null,
  "delete_data": null,
  "status": "completed",
  "output_cleared": false,
  "created_at": "2026-11-15T11:28:12Z",
  "updated_at": "2026-11-15T11:28:12Z",
  "output": "2026-11-15T11:28:12.339011345Z web | listening on :8080\n2026-11-15T11:28:12.339015823Z worker | job 41 done\n"
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `Service "notes" doesn't exist on apps. Call list_services to see the names.`
3. `Container web in notes was removed: Docker on this machine no longer has it.`
4. `lines must be a whole number from 1 to 2000.`
5. `since must be a duration such as 30m, 1h or 7d, at most 7d.`
6. `Could not read the logs of notes: ...`, with Docker's reason, when the daemon tried and failed.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/actions` with `{"type": "logs", "service": "notes"}`, then `GET /v1/machines/{machine}/actions/{id}` | `tender services logs MACHINE SERVICE [--container C] [--lines N] [--since D]` |

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
  "container": null,
  "unit": null,
  "priority": "warning",
  "since": null,
  "lines": 5,
  "destination": null,
  "version": null,
  "packages": null,
  "delete_data": null,
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

### `upgrade_machine`

Upgrades a Debian or Ubuntu machine's system packages through its daemon, in the `tenderupgrade` unit: after `apt-get update`, the security updates with `unattended-upgrade`, or every package with `apt-get upgrade --with-new-pkgs`, keeping the config files that were changed.

> [!WARNING]
> A service may restart when its package upgrades. Tender never reboots the machine; `get_security` says when it needs one.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `false` | `true` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | The machine's name, as `list_machines` shows it. |
| `packages` | string | No | `security`, the default, or `all`. |

#### Returns

The action, with the status `completed`, or `in_progress` if the upgrade hasn't finished within 60 seconds. `output` is the end of what the upgrade printed.

#### Limits

The tool waits up to 60 seconds. The hub fails an upgrade the daemon doesn't finish within 45 minutes. The output is at most 256 KiB, the newest kept.

#### Example call

```json
{"name": "upgrade_machine", "arguments": {"machine": "apps"}}
```

#### Example result

```json
{
  "id": 9,
  "machine": "apps",
  "type": "upgrade",
  "service": null,
  "container": null,
  "unit": null,
  "priority": null,
  "since": null,
  "lines": null,
  "version": null,
  "packages": "security",
  "delete_data": null,
  "status": "in_progress",
  "output_cleared": false,
  "created_at": "2026-11-15T11:50:00Z",
  "updated_at": "2026-11-15T11:50:01Z",
  "output": null
}
```

#### Errors

1. `Machine "apps" doesn't exist. Call list_machines to see the names.`
2. `packages must be security or all.`
3. `Could not upgrade the security packages: ...`, or `Could not upgrade every package: ...` for `all`, with the upgrade's last line, when it failed.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/machines/{machine}/actions` with `{"type": "upgrade", "packages": "security"}`, then `GET /v1/machines/{machine}/actions/{id}` | `tender machines upgrade MACHINE [--all-packages]` |

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
  "container": null,
  "unit": "tenderd",
  "priority": "warning",
  "since": "1h",
  "lines": 2,
  "destination": null,
  "version": null,
  "packages": null,
  "delete_data": null,
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
      "container": null,
      "unit": null,
      "priority": "warning",
      "since": null,
      "lines": 5,
      "destination": null,
      "version": null,
      "packages": null,
      "delete_data": null,
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
      "container": null,
      "unit": null,
      "priority": null,
      "since": "10m",
      "lines": 3,
      "destination": null,
      "version": null,
      "packages": null,
      "delete_data": null,
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

## Updates

[Updates](updates.md) explains how Tender updates itself from the hub.

### `get_updates`

Gets how Tender updates itself: the settings, the newest stable release the hub found on GitHub, each machine's version, the rollout in progress and the last 50 rollouts.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

None.

#### Returns

`mode` and `window` of the hub's settings with their `created_at` and `updated_at`; `latest`, with `version`, `auto` (whether auto mode may roll it out), `checked_at` and `error`; `data`, each machine with `machine`, `version`, `updates` (its own setting), `mode` (what applies) and `update_available`; `rollout`, the one in progress or `null`; and `rollouts`, newest first. A rollout has `id`, `version`, `status`, `auto`, `error`, `machines` (each with `machine`, `position`, `status`, `action_id` and `error`), `created_at` and `updated_at`.

#### Limits

The hub keeps the newest 50 rollouts. It checks GitHub every 6 hours.

#### Example call

```json
{"name": "get_updates", "arguments": {}}
```

#### Example result

```json
{
  "mode": "auto",
  "window": "02:00-05:00",
  "created_at": "2026-11-14T08:00:00Z",
  "updated_at": "2026-11-15T09:12:00Z",
  "latest": {"version": "0.3.1", "auto": true, "checked_at": "2026-11-15T09:12:00Z", "error": null},
  "rollout": null,
  "data": [
    {"machine": "apps", "version": "0.3.1", "updates": "inherit", "mode": "auto", "update_available": false}
  ],
  "rollouts": [
    {
      "id": 3,
      "version": "0.3.1",
      "status": "completed",
      "auto": true,
      "error": null,
      "machines": [
        {"machine": "apps", "position": 1, "status": "completed", "action_id": 40, "error": null,
         "created_at": "2026-11-15T02:00:04Z", "updated_at": "2026-11-15T02:03:02Z"}
      ],
      "created_at": "2026-11-15T02:00:04Z",
      "updated_at": "2026-11-15T02:03:12Z"
    }
  ]
}
```

#### Errors

None of its own.

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/updates`, `GET /v1/updates/settings`, `GET /v1/updates/rollouts` and `GET /v1/updates/rollouts/{id}` | `tender updates show` |

### `set_update_settings`

Sets how Tender updates itself on every machine that inherits the hub's mode.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `mode` | string | Yes | `auto` rolls out each new stable release by itself; `manual` waits for `start_update`. |
| `window` | string | No | When auto mode may start a rollout, two UTC times such as `02:00-05:00`. Leave it out for any time. |

#### Returns

The settings: `mode`, `window`, `created_at` and `updated_at`.

#### Limits

None.

#### Example call

```json
{"name": "set_update_settings", "arguments": {"mode": "auto", "window": "02:00-05:00"}}
```

#### Example result

```json
{"mode": "auto", "window": "02:00-05:00", "created_at": "2026-11-14T08:00:00Z", "updated_at": "2026-11-15T11:45:00Z"}
```

#### Errors

1. `mode must be auto or manual.`
2. `window must be two UTC times, such as 02:00-05:00.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `PUT /v1/updates/settings` | `tender updates set --mode MODE --window HH:MM-HH:MM` |

### `start_update`

Starts a rollout that updates Tender to a stable release, the latest by default, on the machines named or on every machine, one at a time with the hub's own machine first. Each machine checks the release's signature, waits for a running backup to finish, however long it takes, swaps the binary and restarts. If it isn't healthy within 2 minutes it rolls back, and the rollout stops there with an `update.failed` alert.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `false` | `true` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | string | No | The stable release, such as `0.3.1`. Leave it out for the latest. |
| `machines` | array of strings | No | The machines to update, by name. Leave it out for every machine. |

#### Returns

The rollout, as `get_updates` describes it. It goes on after the tool returns: follow it with `get_updates`.

#### Limits

One rollout at a time. It refuses prereleases, downgrades and machines before v0.3.0.

#### Example call

```json
{"name": "start_update", "arguments": {"version": "0.3.1"}}
```

#### Example result

```json
{
  "id": 3,
  "version": "0.3.1",
  "status": "in_progress",
  "auto": false,
  "error": null,
  "machines": [
    {"machine": "apps", "position": 1, "status": "pending", "action_id": null, "error": null,
     "created_at": "2026-11-15T11:46:00Z", "updated_at": "2026-11-15T11:46:00Z"}
  ],
  "created_at": "2026-11-15T11:46:00Z",
  "updated_at": "2026-11-15T11:46:00Z"
}
```

#### Errors

1. `Rollout 3 is still in progress. Wait for it to end; it updates one machine at a time.`
2. `Tender v0.3.2-rc.1 is a prerelease, and Tender only updates to stable releases. Name a stable release, such as 0.3.1.`
3. `apps runs Tender 0.3.2, newer than 0.3.1, and an update never downgrades. ...`
4. `db runs a Tender before v0.3.0, which can't update through the hub. ...`

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/updates/rollouts` | `tender update MACHINE ...` or `tender update --all` |

## Log sinks

A log sink is a place each machine's daemon sends its journal and its Docker containers' logs to, straight from the machine. The logs never reach the hub, so no tool returns them from a sink. [Log shipping](log-shipping.md) explains the sources, the batches and the buffer, and the [API reference](api.md#log-sinks) lists each type's fields.

### `list_log_sinks`

Lists the log sinks, by name, with how sending to each goes on each machine.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

None.

#### Returns

`data`, the sinks, each with `id`, `name`, `type` (`otlp` or `webhook`), `config`, `enabled`, `sources`, `machines`, `status` and the timestamps. In `config`, each secret shows as `"***"`, with `<field>_set` saying whether it is set, and the query string of `url` or `endpoint` shows as `?***`. `machines` is empty for every machine. `status` has one entry per machine that reported on the sink, with `machine`, `records_sent`, `records_dropped`, `last_sent_at`, `last_error`, `failing_since`, `last_dropped_at`, `created_at` (its first report) and `updated_at` (its latest). `last_error` and `failing_since` are `null` while sending works.

#### Limits

None. The list isn't paged. Daemons report every minute, so a status can be up to a minute old.

#### Example call

```json
{"name": "list_log_sinks", "arguments": {}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 1,
      "name": "otel",
      "type": "otlp",
      "config": {
        "endpoint": "https://otel.example.com:4318/v1/logs",
        "headers": "***",
        "headers_set": true
      },
      "enabled": true,
      "sources": {
        "journal": {"units": [], "min_priority": "info"},
        "services": {"names": []}
      },
      "machines": [],
      "status": [
        {
          "machine": "apps",
          "records_sent": 18342,
          "records_dropped": 0,
          "last_sent_at": "2026-11-15T12:04:10Z",
          "last_error": null,
          "failing_since": null,
          "last_dropped_at": null,
          "created_at": "2026-11-15T11:31:02Z",
          "updated_at": "2026-11-15T12:04:12Z"
        }
      ],
      "created_at": "2026-11-15T11:30:41Z",
      "updated_at": "2026-11-15T11:30:41Z"
    }
  ]
}
```

#### Errors

None besides the hub's own failure.

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/logs/sinks` and `GET /v1/logs/sinks/{sink}` | `tender sinks list` and `tender sinks show NAME` |

### `add_log_sink`

Adds a place for every machine's daemon, or the named machines', to send logs to. Daemons start sending within a minute, with the logs from then on.

The secrets you pass go through the AI conversation, and so to the client's model provider. For real keys, run `tender sinks add` instead, which reads them from files. The hub stores them encrypted, and no tool, route or log shows them again.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `false` | `false` |

A second call with the same name fails.

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | The new sink's name: lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |
| `type` | string | Yes | `otlp` or `webhook`. |
| `config` | object | Yes | For `otlp`, `endpoint`, the collector's OTLP/HTTP logs URL, and `headers`, a secret object of HTTP headers. For `webhook`, `url` and `secret`, at least 16 characters; leave `secret` out and the hub makes one. |
| `enabled` | boolean | No | Whether daemons send to it. The default is `true`. |
| `sources` | object | No | Which logs go: `journal` with `units` and `min_priority`, and `services` with `names`. An empty or left-out list means all, and a source left out sends nothing. Leave `sources` out for every unit's journal from `info` up and every container. |
| `machines` | array | No | The machines that send to it, by name. Leave it out for every machine. |

#### Returns

The sink, as `list_log_sinks` shows it, with an empty `status`. For a `webhook` sink without a `secret` input, the result also has `secret`, the one the hub made, shown only this once. Give it to whoever runs the receiver, which checks each batch's signature with it.

#### Limits

None. Logs from before the sink was added aren't sent.

#### Example call

```json
{
  "name": "add_log_sink",
  "arguments": {
    "name": "hook",
    "type": "webhook",
    "config": {"url": "https://logs.example.com/tender"},
    "sources": {"journal": {"units": ["nginx.service"], "min_priority": "warning"}, "services": {"names": ["web"]}},
    "machines": ["apps"]
  }
}
```

#### Example result

```json
{
  "id": 2,
  "name": "hook",
  "type": "webhook",
  "config": {
    "secret": "***",
    "secret_set": true,
    "url": "https://logs.example.com/tender"
  },
  "enabled": true,
  "sources": {
    "journal": {"units": ["nginx.service"], "min_priority": "warning"},
    "services": {"names": ["web"]}
  },
  "machines": ["apps"],
  "status": [],
  "created_at": "2026-11-15T11:32:08Z",
  "updated_at": "2026-11-15T11:32:08Z",
  "secret": "<secret>"
}
```

#### Errors

1. `Log sink names are lowercase letters, digits and hyphens, up to 63 characters.`
2. `type must be one of otlp, webhook.`
3. `config is required: the otlp sink's endpoint, headers.`
4. `config.endpoint is required: an http or https URL, such as https://otel.example.com:4318/v1/logs.`
5. `config has token, which a otlp sink doesn't take. It takes endpoint, headers.`
6. A sentence naming the field that isn't valid, such as `config.secret must be at least 16 characters. Leave it out and the hub makes one.` or `config.headers can't set Content-Type: Tender sets it.`
7. `config.secret is "***", which is how the hub hides a secret. Give the secret itself.`
8. `sources.journal.min_priority must be one of emerg, alert, crit, err, warning, notice, info, debug.`
9. `machines has "db", which is not a machine. Run tender machines list to see the names, or leave machines empty for all.`
10. `A log sink called hook already exists. Choose another name, or change that one.`
11. `This process has no TENDER_ENCRYPTION_KEY, so it can't store or read log sink secrets. Set it to the hub's key and run the command again.` This happens only over stdio.

#### Covers

| REST route | CLI command |
| --- | --- |
| `POST /v1/logs/sinks` | `tender sinks add NAME --type TYPE [OPTIONS]` |

### `update_log_sink`

Changes a sink's config, whether it is enabled, its sources or its machines. Anything you leave out stays as it is.

New secrets you pass go through the AI conversation. The name and the type can't change.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `sink` | string | Yes | The sink's name, as `list_log_sinks` shows it. |
| `config` | object | No | The config fields to change. `null` removes an optional field, and a secret given as `"***"` stays as it is, so you can pass back what `list_log_sinks` shows. |
| `enabled` | boolean | No | Whether daemons send to it. |
| `sources` | object | No | The sources, as for `add_log_sink`. They replace the old ones as a whole, so a source left out sends nothing. |
| `machines` | array | No | The machines that send to it, replacing the list. An empty list is every machine. |

#### Returns

The sink, as `list_log_sinks` shows it.

#### Limits

None. Daemons pick the change up within a minute.

#### Example call

```json
{"name": "update_log_sink", "arguments": {"sink": "otel", "sources": {"journal": {"min_priority": "warning"}}}}
```

#### Example result

```json
{
  "id": 1,
  "name": "otel",
  "type": "otlp",
  "config": {
    "endpoint": "https://otel.example.com:4318/v1/logs",
    "headers": "***",
    "headers_set": true
  },
  "enabled": true,
  "sources": {
    "journal": {"units": [], "min_priority": "warning"},
    "services": null
  },
  "machines": [],
  "status": [],
  "created_at": "2026-11-15T11:30:41Z",
  "updated_at": "2026-11-15T12:30:02Z"
}
```

#### Errors

1. `Log sink "otel" doesn't exist.`
2. `Give config, enabled, sources or machines to change.`
3. The sentences of `add_log_sink` for a sink that isn't valid, such as `config.endpoint is required: ...` after `{"endpoint": null}`.

#### Covers

| REST route | CLI command |
| --- | --- |
| `PATCH /v1/logs/sinks/{sink}` | `tender sinks update NAME [OPTIONS]` |

### `remove_log_sink`

Removes a sink.

> [!WARNING]
> Daemons stop sending to the sink within a minute. They keep the logs they buffered for it for 7 days, then delete them, and nothing sends them anywhere: a sink added again is a new sink. The logs already sent stay at the sink.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `sink` | string | Yes | The sink's name, as `list_log_sinks` shows it. |

#### Returns

`removed`, the name of the sink that was removed.

#### Limits

None.

#### Example call

```json
{"name": "remove_log_sink", "arguments": {"sink": "otel"}}
```

#### Example result

```json
{"removed": "otel"}
```

#### Errors

1. `Log sink "otel" doesn't exist.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `DELETE /v1/logs/sinks/{sink}` | `tender sinks remove NAME` |

## Tokens

### `list_tokens`

Lists the API tokens that may use this hub's REST API and MCP endpoint, with when each was last used.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

None.

#### Returns

`data`, one entry per token, by name. Each has `id`, `name`, `last_used_at` (the last request with it, to the minute, `null` before the first) and the timestamps. The tokens themselves are never shown.

#### Limits

None. The list isn't paged.

#### Example call

```json
{"name": "list_tokens", "arguments": {}}
```

#### Example result

```json
{
  "data": [
    {
      "id": 2,
      "name": "claude-code",
      "last_used_at": "2026-11-15T12:05:00Z",
      "created_at": "2026-11-15T12:04:10Z",
      "updated_at": "2026-11-15T12:04:10Z"
    }
  ]
}
```

#### Errors

None beyond the ones every tool has.

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/tokens` | `tender tokens list` |

### `delete_token`

Deletes an API token by name. Every client that sends it, which may be the one calling, is refused from its next request on.

No tool makes a token, so an AI client can't give itself more access. A person makes one with `tender tokens add NAME`.

> [!WARNING]
> Deleting the last token locks everyone out of the REST API and `/mcp` until the owner runs `sudo tender tokens add NAME` on the hub machine.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | The token's name, as `list_tokens` shows it. |

#### Returns

`removed`, the name of the token that was deleted.

#### Limits

None.

#### Example call

```json
{"name": "delete_token", "arguments": {"name": "claude-code"}}
```

#### Example result

```json
{"removed": "claude-code"}
```

#### Errors

1. `name is required. Call list_tokens to see the names.`
2. `Token "claude-code" doesn't exist. Call list_tokens to see the names.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `DELETE /v1/tokens/{token}` | `tender tokens remove NAME --yes` |

## Exposures

### `get_cloudflare_settings`

Gets the hub's Cloudflare settings, which services' `cloudflare` routes need. The hub keeps one tunnel per machine in that account, named `tender-<machine>`, and a proxied DNS record per route.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `true` | not set | `true` | `false` |

#### Inputs

None.

#### Returns

The settings: `account_id`, `api_token` (always `"***"`), `api_token_set`, `created_at` and `updated_at`.

#### Limits

None.

#### Example call

```json
{"name": "get_cloudflare_settings", "arguments": {}}
```

#### Example result

```json
{"account_id": "0123456789abcdef0123456789abcdef", "api_token": "***", "api_token_set": true, "created_at": "2026-11-15T10:00:00Z", "updated_at": "2026-11-15T10:00:00Z"}
```

#### Errors

1. `Cloudflare isn't set up on this hub. Set it up with: tender exposures cloudflare set --account-id ID --api-token-file PATH`

#### Covers

| REST route | CLI command |
| --- | --- |
| `GET /v1/exposures/cloudflare` | `tender exposures cloudflare show` |

### `set_cloudflare_settings`

Sets the Cloudflare account and API token that services' `cloudflare` routes use. The hub first asks Cloudflare whether the token reaches the account's tunnels and zones, and stores nothing if it doesn't.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `false` | `true` | `true` |

#### Inputs

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `account_id` | string | Yes | The Cloudflare account ID, 32 lowercase hex digits. |
| `api_token` | string | No | The API token, with Account Cloudflare Tunnel Edit, Zone DNS Edit and Zone Read. Required the first time; leave it out to keep the stored one. |

#### Returns

The settings, as `get_cloudflare_settings` returns them.

#### Limits

A token passed here goes through the AI conversation. `tender exposures cloudflare set` asks for it with typing hidden instead.

#### Example call

```json
{"name": "set_cloudflare_settings", "arguments": {"account_id": "0123456789abcdef0123456789abcdef"}}
```

#### Example result

```json
{"account_id": "0123456789abcdef0123456789abcdef", "api_token": "***", "api_token_set": true, "created_at": "2026-11-15T10:00:00Z", "updated_at": "2026-11-15T11:20:00Z"}
```

#### Errors

1. `account_id must be a Cloudflare account ID, 32 lowercase hex digits, as the dashboard shows under Account home.`
2. `api_token is required: the hub has no Cloudflare API token yet.`
3. `Cloudflare refused GET /accounts/.../cfd_tunnel: 403 Forbidden, Authentication error (10000). Check the account ID, and that the API token has Account Cloudflare Tunnel Edit, Zone DNS Edit and Zone Read.`
4. `The hub could not check the settings with Cloudflare: ...`, when Cloudflare is down. Try again later.

#### Covers

| REST route | CLI command |
| --- | --- |
| `PUT /v1/exposures/cloudflare` | `tender exposures cloudflare set --account-id ID --api-token-file PATH` |

### `remove_cloudflare_settings`

Removes the hub's Cloudflare account and API token. The tunnels and DNS records already in Cloudflare stay.

#### Annotations

| `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
| --- | --- | --- | --- |
| `false` | `true` | `true` | `false` |

#### Inputs

None.

#### Returns

`removed`, naming what was removed.

#### Limits

It refuses while a service has a `cloudflare` route.

#### Example call

```json
{"name": "remove_cloudflare_settings", "arguments": {}}
```

#### Example result

```json
{"removed": "the Cloudflare settings"}
```

#### Errors

1. `Cloudflare isn't set up on this hub. There is nothing to remove.`
2. `2 cloudflare routes need these settings. Take the routes off their services first, or redeploy them with another expose.`

#### Covers

| REST route | CLI command |
| --- | --- |
| `DELETE /v1/exposures/cloudflare` | `tender exposures cloudflare remove` |

## Next steps

1. [API reference](api.md) describes the same operations over REST.
2. [CLI reference](cli.md) lists every `tender` command.
3. [Connect an AI client](guides/connect-an-ai-client.md) walks through connecting Claude Code.
4. [Security](security.md) explains what the hub protects, its API tokens and how it handles logs.
