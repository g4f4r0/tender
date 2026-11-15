# REST API

The hub's REST API does everything the `tender` command and the MCP tools do, over HTTP and JSON.

## Prerequisites

1. A running hub. See [Install](install.md).
2. The hub's address. The examples use `https://apps.your-tailnet.ts.net`, the hub's name on a tailnet. On the hub machine itself, use `http://127.0.0.1:8080`.
3. `curl`, or any HTTP client.

## Conventions

### Authentication

People and their tools send no token in this version. Anyone who can reach the hub can call every route below, so keep the hub on your tailnet or behind your own access control. See [Security](security.md).

The hub refuses a browser request from another website that would change something, and a request for a host name it doesn't answer to. Both get `403`. [Security](security.md#the-hub-has-no-login) lists the names it answers to.

Daemons sign in with their machine's token, as `Authorization: Bearer <token>`, and a token works only for its own machine. A wrong token gets `401`, another machine's token gets `403`, and a machine that doesn't exist gets `404`. People never need a token, and should send no `Authorization` header: on a route both may call, the hub treats a request with one as a daemon's.

### Requests and responses

1. Every route is under `/v1`. `GET /v1/health` answers `{"status": "ok"}` while the hub can reach its database, and `503` when it can't.
2. JSON fields have the same names as the database columns, such as `retention_days` and `created_at`.
3. Times are RFC 3339 in UTC, such as `2026-11-15T11:27:26Z`.
4. A request body is one JSON object of at most 1 MiB, and a larger one gets `413`. A field the route doesn't know is `400`, and a field of the wrong type is `422`. JSON that isn't an object, such as `[]`, gets `422` with `The request body must be a JSON object.`
5. A collection answers `{"data": [...]}`. A paged collection takes `limit`, from 1 to 500 with a default of 50, and adds `next_cursor` when there are more. Pass it as `cursor` for the next page. The last page has no `next_cursor`.
6. A create answers `201 Created` with the new object and its address in the `Location` header.
7. `PATCH` takes a JSON object with only the fields to change. The hub reads it as JSON whatever its `Content-Type`, so `application/merge-patch+json` and `application/json` both work. A field you leave out, or set to `null`, stays as it is.
8. A `DELETE` answers `204 No Content`.

### Errors

Every error is an RFC 9457 problem, with `Content-Type: application/problem+json` and a `detail` sentence that says what to do:

```http
HTTP/1.1 422 Unprocessable Entity
Content-Type: application/problem+json

{
  "detail": "service is required for stop.",
  "status": 422,
  "title": "Unprocessable Entity",
  "type": "about:blank"
}
```

A route that doesn't exist answers `404`, and a method a route doesn't take answers `405` with an `Allow` header, both as problems. A failure inside the hub answers `500` and the hub logs the cause; the response never shows it.

### Idempotency

Daemons retry their reports, so a daemon's `POST` and `PATCH` with an `Idempotency-Key` header run once. A retry with the same key within 24 hours gets the first answer again: its status, body, `Location` and `Content-Type`. An empty `204` is stored and replayed too. The same key on another route gets `422`. The hub ignores the header on people's requests.

### Actions

Starting, stopping and restarting a service and reading logs happen on the machine, so `POST /v1/machines/{machine}/actions` answers `202 Accepted` at once with the action as `pending`. Poll `GET /v1/machines/{machine}/actions/{id}`, about once a second, until its `status` is `completed` or `failed`. The daemon usually picks an action up within a second. [Services](services.md#how-actions-reach-a-machine) explains the steps, and when an action fails because the daemon didn't pick it up or didn't say how it ended.

## Machines

A machine is a server with a daemon that reports to the hub.

### `GET /v1/machines`

Lists every machine with its latest reading of CPU load, memory, disk and uptime, by name.

`last_seen` and `usage` are `null` until the machine's daemon first reports.

#### Parameters

None.

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "id": 1,
      "name": "apps",
      "created_at": "2026-11-15T11:27:26Z",
      "updated_at": "2026-11-15T11:27:26Z",
      "last_seen": "2026-11-15T11:36:35Z",
      "usage": {
        "id": 11,
        "cpu_load": 0.09,
        "memory_used_percent": 16.06,
        "disk_used_percent": 27.72,
        "uptime_seconds": 222901,
        "created_at": "2026-11-15T11:36:35Z",
        "updated_at": "2026-11-15T11:36:35Z"
      }
    }
  ]
}
```

#### Errors

| Status | When |
| --- | --- |
| `500` | The hub failed to read its database. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_machines` | `tender machines list` |

### `POST /v1/machines`

Adds a machine and returns its token, which is shown only this once.

Install the daemon on the machine with this token. The hub stores only its SHA-256 hash.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | Body. Lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |

#### Example request

```sh
curl -s -X POST https://apps.your-tailnet.ts.net/v1/machines \
  -H 'Content-Type: application/json' \
  -d '{"name": "apps"}'
```

#### Example response

```http
HTTP/1.1 201 Created
Content-Type: application/json
Location: /v1/machines/apps

{
  "id": 1,
  "name": "apps",
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T11:27:26Z",
  "token": "<token>"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `409` | A machine with this name already exists. |
| `422` | The name isn't lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `add_machine` | `tender machines add apps` |

### `GET /v1/machines/{machine}`

Returns one machine with its latest reading.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 1,
  "name": "apps",
  "version": "0.3.1",
  "updates": "inherit",
  "security_allowed_ports": ["41641/udp"],
  "security_alerts": true,
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T11:27:26Z",
  "last_seen": "2026-11-15T11:36:35Z",
  "usage": {
    "id": 11,
    "cpu_load": 0.09,
    "memory_used_percent": 16.06,
    "disk_used_percent": 27.72,
    "uptime_seconds": 222901,
    "created_at": "2026-11-15T11:36:35Z",
    "updated_at": "2026-11-15T11:36:35Z"
  }
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_machines` | `tender machines list` |

### `DELETE /v1/machines/{machine}`

Removes a machine with its usage, backup settings, snapshots, history, services, actions and notifications.

> [!WARNING]
> There is no undo. The machine's token stops working at once. Its backups in the bucket stay where they are.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s -X DELETE https://apps.your-tailnet.ts.net/v1/machines/apps
```

#### Example response

```http
HTTP/1.1 204 No Content
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `remove_machine` | `tender machines remove apps --yes` |

### `POST /v1/machines/{machine}/token`

Replaces a machine's token and returns the new one, shown only this once.

> [!WARNING]
> The old token stops working at once, so the daemon stays offline and stops backing up until you install the new one with `sudo tender install daemon --token-file PATH`.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s -X POST https://apps.your-tailnet.ts.net/v1/machines/apps/token
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "token": "<token>"
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `rotate_machine_token` | `tender machines rotate-token apps` |

### `GET /v1/machines/{machine}/usage`

Lists a machine's readings of CPU load, memory, disk and uptime, newest first. The daemon sends one a minute.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `since` | string | No | Query. Only readings from this RFC 3339 time on. |
| `until` | string | No | Query. Only readings up to this RFC 3339 time. |
| `limit` | integer | No | Query. How many to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | Query. The `next_cursor` of the previous page. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/usage?limit=2
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "id": 11,
      "cpu_load": 0.09,
      "memory_used_percent": 16.06,
      "disk_used_percent": 27.72,
      "uptime_seconds": 222901,
      "created_at": "2026-11-15T11:36:35Z",
      "updated_at": "2026-11-15T11:36:35Z"
    },
    {
      "id": 10,
      "cpu_load": 0.05,
      "memory_used_percent": 15.51,
      "disk_used_percent": 27.72,
      "uptime_seconds": 222841,
      "created_at": "2026-11-15T11:35:35Z",
      "updated_at": "2026-11-15T11:35:35Z"
    }
  ],
  "next_cursor": "MTc5NDc0MjUzNToxMA"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | `since` or `until` isn't an RFC 3339 time, `limit` isn't from 1 to 500, or `cursor` isn't valid. |
| `404` | The machine doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_machine_usage` | None. `tender machines list` shows the latest memory and disk. |

### `PATCH /v1/machines/{machine}`

Changes a machine's settings: its own update mode, the ports that may listen on a public address without an alert, and whether its security alerts are on.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `updates` | string | No | Body. `inherit` follows the hub's update mode, as [Updates](updates.md#choose-auto-or-manual) explains; `auto` or `manual` is the machine's own. |
| `security_allowed_ports` | array of strings | No | Body. The whole allowlist, which replaces the old one: ports with their protocol, such as `["41641/udp", "443/tcp"]`, at most 100. |
| `security_alerts` | boolean | No | Body. Whether the machine's security alerts open. Off, its open ones resolve within a minute. |

#### Example request

```sh
curl -s -X PATCH https://apps.your-tailnet.ts.net/v1/machines/apps \
  -H 'Content-Type: application/merge-patch+json' \
  -d '{"security_allowed_ports": ["41641/udp"]}'
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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

The response is the machine as `GET /v1/machines/{machine}` returns it, with its latest reading in `usage`.

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | The machine doesn't exist. |
| `422` | `updates` isn't `inherit`, `auto` or `manual`, a port isn't a number from 1 to 65535 with `/tcp` or `/udp`, or the list has more than 100 ports. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `update_machine` | `tender machines set apps --updates manual --allow-port 41641/udp` |

### `GET /v1/machines/{machine}/security`

Returns the latest security checks of a machine, which its daemon runs when it starts and every 15 minutes. [Security](security.md#security-checks) explains each check.

Each check has `check`, which is `ok`, `error` (with the reason in `error`) or `unsupported` on a system other than Debian or Ubuntu. A listener on the machine's allowlist has `allowed: true`. `security_updates_since` is since when the reports have found security updates pending, or `null`.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/security
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 1,
  "machine": "apps",
  "firewall": {"check": "ok", "error": null, "tool": "ufw", "active": true, "default_incoming": "deny"},
  "listeners": {
    "check": "ok",
    "error": null,
    "ports": [
      {"protocol": "udp", "address": "0.0.0.0", "port": 41641, "process": "tailscaled", "docker": false, "service": null, "allowed": true},
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

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or its daemon hasn't sent a security report yet. A daemon before v0.3.0 sends none. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_security` | `tender security apps` |

## Backups

A machine's backups are its settings, the snapshots in its repository and the history of its runs.

### `GET /v1/backups`

Lists every machine's backup state: its settings, latest snapshot, last backup, last check and snapshot count.

A part that doesn't exist yet, such as the settings of a machine you haven't set up, is `null`.

#### Parameters

None.

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/backups
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
        "created_at": "2026-11-15T11:27:38Z",
        "updated_at": "2026-11-15T11:27:38Z"
      },
      "latest_snapshot": {
        "id": 10,
        "restic_id": "94401a6d30d1f26aafcbfed18b1f8c11a69db020fe54c5caf5363f82d6bbf1f2",
        "size_bytes": 200006,
        "paths": [
          "/"
        ],
        "created_at": "2026-11-15T11:36:05Z",
        "updated_at": "2026-11-15T11:36:07Z"
      },
      "last_backup": {
        "id": 20,
        "snapshot_id": 10,
        "type": "backup",
        "status": "completed",
        "error": null,
        "created_at": "2026-11-15T11:36:05Z",
        "updated_at": "2026-11-15T11:36:07Z"
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
      "snapshot_count": 10
    }
  ]
}
```

#### Errors

| Status | When |
| --- | --- |
| `500` | The hub failed to read its database. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_backup_status` | `tender backups status` |

### `GET /v1/machines/{machine}/backups`

Returns one machine's backup state.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/backups
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
    "created_at": "2026-11-15T11:27:38Z",
    "updated_at": "2026-11-15T11:27:38Z"
  },
  "latest_snapshot": {
    "id": 10,
    "restic_id": "94401a6d30d1f26aafcbfed18b1f8c11a69db020fe54c5caf5363f82d6bbf1f2",
    "size_bytes": 200006,
    "paths": [
      "/"
    ],
    "created_at": "2026-11-15T11:36:05Z",
    "updated_at": "2026-11-15T11:36:07Z"
  },
  "last_backup": {
    "id": 20,
    "snapshot_id": 10,
    "type": "backup",
    "status": "completed",
    "error": null,
    "created_at": "2026-11-15T11:36:05Z",
    "updated_at": "2026-11-15T11:36:07Z"
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
  "snapshot_count": 10
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_backup_status` | `tender backups status apps` |

### `GET /v1/machines/{machine}/backups/settings`

Returns a machine's destination, schedule and retention.

The response never holds keys or passwords. Only the machine's own daemon, with its token, gets them on this route.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/backups/settings
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist. |
| `404` | The machine has no backup settings yet. This problem's `type` is `https://github.com/g4f4r0/tender#no-backup-settings`, so a client can tell it apart. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_backup_status` | `tender backups status apps` |

### `PUT /v1/machines/{machine}/backups/settings`

Sets a machine's destination, schedule and retention, replacing any earlier settings.

A daemon without settings picks them up within 30 seconds. A daemon that has settings checks for changes every 10 minutes.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `destination` | string | Yes | Body. The destination's name. |
| `schedule` | string | Yes | Body. A standard 5-field cron expression, in UTC, such as `30 3 * * *`. |
| `retention_days` | integer | Yes | Body. How many days of snapshots to keep, at least 1. |

#### Example request

```sh
curl -s -X PUT https://apps.your-tailnet.ts.net/v1/machines/apps/backups/settings \
  -H 'Content-Type: application/json' \
  -d '{"destination": "r2", "schedule": "30 3 * * *", "retention_days": 30}'
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
  "updated_at": "2026-11-15T11:40:10Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | The machine doesn't exist. |
| `422` | The schedule isn't a 5-field cron expression, `retention_days` is below 1, or the destination doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `set_backup` | `tender backups set apps --destination r2 --schedule "30 3 * * *" --retention-days 30` |

### `DELETE /v1/machines/{machine}/backups/settings`

Removes a machine's backup settings, so its daemon stops backing up. Its snapshots and history stay.

The daemon deletes its saved copy of the settings and the repository password. The backups in the bucket stay there.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s -X DELETE https://apps.your-tailnet.ts.net/v1/machines/apps/backups/settings
```

#### Example response

```http
HTTP/1.1 204 No Content
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or has no backup settings. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `unset_backup` | `tender backups unset apps` |

### `GET /v1/machines/{machine}/backups/snapshots`

Lists a machine's snapshots, newest first, as its daemon last read them from the repository.

`restic_id` is the snapshot's ID in restic, which `restic restore` takes. `size_bytes` is `null` when restic didn't say.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `limit` | integer | No | Query. How many to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | Query. The `next_cursor` of the previous page. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/backups/snapshots?limit=2
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "id": 10,
      "restic_id": "94401a6d30d1f26aafcbfed18b1f8c11a69db020fe54c5caf5363f82d6bbf1f2",
      "size_bytes": 200006,
      "paths": [
        "/"
      ],
      "created_at": "2026-11-15T11:36:05Z",
      "updated_at": "2026-11-15T11:36:07Z"
    },
    {
      "id": 9,
      "restic_id": "58db71f28802ee99c9bad66e4c84f3223bfca97bdebe45e5e05ae76ae2cf4dcf",
      "size_bytes": 200006,
      "paths": [
        "/"
      ],
      "created_at": "2026-11-15T11:35:05Z",
      "updated_at": "2026-11-15T11:35:07Z"
    }
  ],
  "next_cursor": "MTc5NDc0MjUwNTo5"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | `limit` isn't a whole number from 1 to 500, or `cursor` isn't valid. |
| `404` | The machine doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_backup_snapshots` | None. `tender backups status apps` shows the count. |

### `GET /v1/machines/{machine}/backups/snapshots/{id}`

Returns one snapshot.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `id` | integer | Yes | Path. The snapshot's `id`, not its `restic_id`. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/backups/snapshots/10
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 10,
  "restic_id": "94401a6d30d1f26aafcbfed18b1f8c11a69db020fe54c5caf5363f82d6bbf1f2",
  "size_bytes": 200006,
  "paths": [
    "/"
  ],
  "created_at": "2026-11-15T11:36:05Z",
  "updated_at": "2026-11-15T11:36:07Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or it has no snapshot with this id. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_backup_snapshots` | None. |

### `GET /v1/machines/{machine}/backups/history`

Lists a machine's backup, cleanup and check runs, newest first.

A failed run has the reason in `error`. A completed backup has the snapshot it made in `snapshot_id`.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `type` | string | No | Query. Only runs of this type: `backup`, `cleanup` or `check`. |
| `status` | string | No | Query. Only runs with this status: `pending`, `in_progress`, `completed` or `failed`. |
| `limit` | integer | No | Query. How many to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | Query. The `next_cursor` of the previous page. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/backups/history?limit=2
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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

| Status | When |
| --- | --- |
| `400` | `limit` isn't a whole number from 1 to 500, or `cursor` isn't valid. |
| `404` | The machine doesn't exist. |
| `422` | `type` or `status` isn't one of its values. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_backup_history` | None. `tender backups status apps` shows the last backup. |

### `GET /v1/machines/{machine}/backups/history/{id}`

Returns one run.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `id` | integer | Yes | Path. The run's id. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/backups/history/20
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 20,
  "snapshot_id": 10,
  "type": "backup",
  "status": "completed",
  "error": null,
  "created_at": "2026-11-15T11:36:05Z",
  "updated_at": "2026-11-15T11:36:07Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or it has no run with this id. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_backup_history` | None. |

## Destinations

A destination is an S3 bucket and a path in it, with the access keys and the repository password that every machine's backups there use.

### `GET /v1/backups/destinations`

Lists every destination, by name. Keys and passwords are never part of the response.

#### Parameters

None.

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/backups/destinations
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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

| Status | When |
| --- | --- |
| `500` | The hub failed to read its database. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_destinations` | `tender destinations list` |

### `POST /v1/backups/destinations`

Adds a destination. Without a password, the hub makes one and returns it this once.

`password` appears in the response only when the hub made it. Save it in a password manager; [Backups](backups.md#encryption-and-the-repository-password) explains why. The CLI asks for the keys with typing hidden, which keeps them out of your shell history.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | Body. Lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |
| `type` | string | No | Body. `s3`, the only type and the default. |
| `endpoint` | string | Yes | Body. The S3 endpoint URL, such as `https://<account-id>.r2.cloudflarestorage.com`. |
| `bucket` | string | Yes | Body. The bucket's name. |
| `path` | string | No | Body. A folder in the bucket. Each machine's repository goes in a folder named after it below this one. |
| `access_key_id` | string | Yes | Body. The access key ID. The hub stores it encrypted. |
| `secret_access_key` | string | Yes | Body. The secret access key. The hub stores it encrypted. |
| `password` | string | No | Body. The repository password. Leave it out and the hub makes a strong one. |

#### Example request

```sh
curl -s -X POST https://apps.your-tailnet.ts.net/v1/backups/destinations \
  -H 'Content-Type: application/json' \
  -d '{"name": "r2", "endpoint": "https://<account-id>.r2.cloudflarestorage.com", "bucket": "my-backups", "path": "tender", "access_key_id": "<access key ID>", "secret_access_key": "<secret access key>"}'
```

#### Example response

```http
HTTP/1.1 201 Created
Content-Type: application/json
Location: /v1/backups/destinations/r2

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

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `409` | A destination with this name, or with this endpoint, bucket and path, already exists. |
| `422` | The name isn't valid, the type isn't `s3`, the endpoint isn't an http or https URL, or the bucket or a key is missing. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `add_destination` | `tender destinations add r2 --endpoint URL --bucket my-backups --path tender` |

### `GET /v1/backups/destinations/{destination}`

Returns one destination, without its keys or password.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | Path. The destination's name. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/backups/destinations/r2
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The destination doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_destinations` | `tender destinations list` |

### `PATCH /v1/backups/destinations/{destination}`

Changes a destination's location or keys. The password can't change, because the repositories are encrypted with it.

Move the data yourself before you point a destination at another bucket or path. Daemons check for the new location and keys every 10 minutes.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | Path. The destination's name. |
| `endpoint` | string | No | Body. The new endpoint URL. |
| `bucket` | string | No | Body. The new bucket. |
| `path` | string | No | Body. The new folder in the bucket. |
| `access_key_id` | string | No | Body. The new access key ID. |
| `secret_access_key` | string | No | Body. The new secret access key. |

#### Example request

```sh
curl -s -X PATCH https://apps.your-tailnet.ts.net/v1/backups/destinations/r2 \
  -H 'Content-Type: application/merge-patch+json' \
  -d '{"access_key_id": "<new access key ID>", "secret_access_key": "<new secret access key>"}'
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 1,
  "name": "r2",
  "type": "s3",
  "endpoint": "https://<account-id>.r2.cloudflarestorage.com",
  "bucket": "my-backups",
  "path": "tender",
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T11:40:02Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | The destination doesn't exist. |
| `409` | Another destination already uses the new endpoint, bucket and path. |
| `422` | The body has `password`, or sets the endpoint, bucket or a key to an empty string, or the endpoint isn't an http or https URL. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `update_destination` | None. |

### `DELETE /v1/backups/destinations/{destination}`

Removes a destination that no machine backs up to.

> [!WARNING]
> The hub forgets the destination's keys and password. The backups already in the bucket stay there, and you need the password from your password manager to restore them.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | Path. The destination's name. |

#### Example request

```sh
curl -s -X DELETE https://apps.your-tailnet.ts.net/v1/backups/destinations/r2
```

#### Example response

```http
HTTP/1.1 204 No Content
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The destination doesn't exist. |
| `409` | A machine still backs up to it. Delete those machines' backup settings first. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `remove_destination` | `tender destinations remove r2` |

## Notifications

A notification is an alert about a machine, its backups or a service, and the emails it sends go to every recipient. See [Alerts](alerts.md) for each type.

### `GET /v1/notifications`

Lists every machine's notifications, newest first.

A notification is `pending`, `sent` or `resolved`, as [Alerts](alerts.md#how-an-alert-works) explains. `machine` is the machine the alert is about, and `service` is `null` for an alert that isn't about one service.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `status` | string | No | Query. Only notifications with this status: `pending`, `sent` or `resolved`. |
| `severity` | string | No | Query. Only notifications with this severity: `info`, `warning` or `critical`. |
| `limit` | integer | No | Query. How many to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | Query. The `next_cursor` of the previous page. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/notifications?limit=2
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
    },
    {
      "id": 2,
      "machine": "apps",
      "service": null,
      "type": "docker_unreachable",
      "severity": "critical",
      "status": "resolved",
      "content": "Docker on apps can't be reached: its socket refuses connections, so Docker is stopped. Its services' states are unknown until it is back.",
      "created_at": "2026-11-15T11:32:35Z",
      "updated_at": "2026-11-15T11:34:35Z"
    }
  ],
  "next_cursor": "MTc5NDc0MjM1NToy"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | `limit` isn't a whole number from 1 to 500, or `cursor` isn't valid. |
| `422` | `status` or `severity` isn't one of its values. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_notifications` | `tender status`, which shows the open ones. |

### `GET /v1/machines/{machine}/notifications`

Lists one machine's notifications, newest first.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `status` | string | No | Query. `pending`, `sent` or `resolved`. |
| `severity` | string | No | Query. `info`, `warning` or `critical`. |
| `limit` | integer | No | Query. How many to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | Query. The `next_cursor` of the previous page. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/notifications?limit=2
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
    },
    {
      "id": 2,
      "machine": "apps",
      "service": null,
      "type": "docker_unreachable",
      "severity": "critical",
      "status": "resolved",
      "content": "Docker on apps can't be reached: its socket refuses connections, so Docker is stopped. Its services' states are unknown until it is back.",
      "created_at": "2026-11-15T11:32:35Z",
      "updated_at": "2026-11-15T11:34:35Z"
    }
  ],
  "next_cursor": "MTc5NDc0MjM1NToy"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | `limit` isn't a whole number from 1 to 500, or `cursor` isn't valid. |
| `404` | The machine doesn't exist. |
| `422` | `status` or `severity` isn't one of its values. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_notifications` | `tender status` |

### `GET /v1/machines/{machine}/notifications/{id}`

Returns one of a machine's notifications.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `id` | integer | Yes | Path. The notification's id. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/notifications/3
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or it has no notification with this id. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_notifications` | None. |

### `GET /v1/notifications/recipients`

Lists the addresses that get every alert email, in the order you added them.

#### Parameters

None.

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/notifications/recipients
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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

| Status | When |
| --- | --- |
| `500` | The hub failed to read its database. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_recipients` | `tender recipients list` |

### `POST /v1/notifications/recipients`

Adds an address that gets every alert email.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string | Yes | Body. A bare address, such as `ops@example.com`, without a display name. |

#### Example request

```sh
curl -s -X POST https://apps.your-tailnet.ts.net/v1/notifications/recipients \
  -H 'Content-Type: application/json' \
  -d '{"email": "ops@example.com"}'
```

#### Example response

```http
HTTP/1.1 201 Created
Content-Type: application/json
Location: /v1/notifications/recipients/1

{
  "id": 1,
  "email": "ops@example.com",
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T11:27:26Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `409` | The address is already a recipient, in any letter case. |
| `422` | The value isn't one bare email address. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `add_recipient` | `tender recipients add ops@example.com` |

### `GET /v1/notifications/recipients/{id}`

Returns one recipient.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | integer | Yes | Path. The recipient's id. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/notifications/recipients/1
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 1,
  "email": "ops@example.com",
  "created_at": "2026-11-15T11:27:26Z",
  "updated_at": "2026-11-15T11:27:26Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | No recipient has this id. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_recipient` | None. `tender recipients list` shows every recipient. |

### `DELETE /v1/notifications/recipients/{id}`

Removes a recipient. Alerts are still recorded when nobody is left.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | integer | Yes | Path. The recipient's id. |

#### Example request

```sh
curl -s -X DELETE https://apps.your-tailnet.ts.net/v1/notifications/recipients/1
```

#### Example response

```http
HTTP/1.1 204 No Content
```

#### Errors

| Status | When |
| --- | --- |
| `404` | No recipient has this id. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `remove_recipient` | `tender recipients remove ops@example.com` |

### `POST /v1/notifications/test`

Sends a test email to every recipient, to check that alerts arrive.

The subject is `Tender test email`.

#### Parameters

None.

#### Example request

```sh
curl -s -X POST https://apps.your-tailnet.ts.net/v1/notifications/test
```

#### Example response

```http
HTTP/1.1 202 Accepted
Content-Type: application/json

{
  "sent_to": [
    "ops@example.com"
  ]
}
```

#### Errors

| Status | When |
| --- | --- |
| `422` | Email is off on this hub, or there are no recipients. |
| `502` | The hub couldn't send the email, such as when the SMTP server is unreachable, refuses the login or refuses the email. The detail has the reason. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `send_test_email` | `tender email test` |

## Services

A service is a Docker container on a machine, as the machine's daemon reported it in its latest report. See [Services](services.md).

### `GET /v1/services`

Lists every machine's services, by machine, with whether its Docker answers.

`docker` is `running` or `unreachable`, from the latest report, or `null` before the first. `docker_reason` says why Docker is unreachable, and is `null` while it runs. [Services](services.md#when-docker-cant-be-reached) lists the reasons and what an unreachable Docker does to each service.

#### Parameters

None.

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/services
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "machine": "apps",
      "docker": "running",
      "docker_reason": null,
      "data": [
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
          "started_at": "2026-11-15T10:32:35Z",
          "memory_bytes": 209715200,
          "memory_limit_bytes": 1073741824,
          "cpu_percent": 12.5,
          "alerts_enabled": true,
          "stopped_by_tender": false,
          "removed_at": null,
          "created_at": "2026-11-15T11:27:27Z",
          "updated_at": "2026-11-15T11:36:35Z"
        }
      ]
    }
  ]
}
```

#### Errors

| Status | When |
| --- | --- |
| `500` | The hub failed to read its database. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_services` | `tender services list` |

### `GET /v1/machines/{machine}/services`

Lists one machine's services, by name.

`cpu_percent` is where 100 is one whole CPU. [Services](services.md#what-a-service-is) explains each field and when usage is `null`.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/services
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
      "updated_at": "2026-11-15T11:36:35Z"
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
      "started_at": "2026-11-15T10:32:35Z",
      "memory_bytes": 209715200,
      "memory_limit_bytes": 1073741824,
      "cpu_percent": 12.5,
      "alerts_enabled": true,
      "stopped_by_tender": false,
      "removed_at": null,
      "created_at": "2026-11-15T11:27:27Z",
      "updated_at": "2026-11-15T11:36:35Z"
    }
  ]
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or it was removed while the hub read its services. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_services` | `tender services list apps` |

### `GET /v1/machines/{machine}/services/{service}`

Returns one service.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `service` | string | Yes | Path. The container's name. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/services/web
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
  "started_at": "2026-11-15T10:32:35Z",
  "memory_bytes": 209715200,
  "memory_limit_bytes": 1073741824,
  "cpu_percent": 12.5,
  "alerts_enabled": true,
  "stopped_by_tender": false,
  "removed_at": null,
  "created_at": "2026-11-15T11:27:27Z",
  "updated_at": "2026-11-15T11:36:35Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or it has no service with this name. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_services` | `tender services list apps` |

### `PATCH /v1/machines/{machine}/services/{service}`

Turns a service's alerts off or on. Turning them off resolves its open alerts.

The setting follows the service's name, as [Services](services.md#what-a-service-is) explains.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `service` | string | Yes | Path. The container's name. |
| `alerts_enabled` | boolean | No | Body. `false` to stop the service's alerts, `true` to start them again. |

#### Example request

```sh
curl -s -X PATCH https://apps.your-tailnet.ts.net/v1/machines/apps/services/web \
  -H 'Content-Type: application/merge-patch+json' \
  -d '{"alerts_enabled": false}'
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
  "started_at": "2026-11-15T10:32:35Z",
  "memory_bytes": 209715200,
  "memory_limit_bytes": 1073741824,
  "cpu_percent": 12.5,
  "alerts_enabled": false,
  "stopped_by_tender": false,
  "removed_at": null,
  "created_at": "2026-11-15T11:27:27Z",
  "updated_at": "2026-11-15T11:36:35Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | The machine doesn't exist, or it has no service with this name. |
| `422` | `alerts_enabled` isn't a boolean. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `set_service_alerts` | `tender services alerts apps web off` |

## Actions

An action is work for a machine's daemon: starting, stopping or restarting a service, reading a service's logs or the machine's journal, upgrading the machine's packages, or updating Tender, which only a rollout queues. The hub keeps each machine's newest 100 finished actions, and every `pending` or `in_progress` one.

### `POST /v1/machines/{machine}/actions`

Queues an action for the machine's daemon and answers at once with the action as `pending`.

To read the machine's journal, send `{"type": "logs", "unit": "tenderd", "priority": "warning", "since": "1h", "lines": 200}`. Logs output is at most 256 KiB, dropping the oldest lines past it.

To upgrade a Debian or Ubuntu machine's packages, send `{"type": "upgrade", "packages": "security"}`, or `"all"` for every package. The daemon runs it in the `tenderupgrade` unit, and the hub gives it 45 minutes instead of 15. Its output is the end of what the upgrade printed. Tender never reboots the machine.

> [!WARNING]
> A `stop` takes the service offline until something starts it again. Logs can hold secrets. The hub returns them only on `GET .../actions/{id}`, clears them 1 hour after the action completes, and never puts them in alerts or emails. See [Security](security.md#logs-can-hold-secrets).

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `type` | string | Yes | Body. `start`, `stop`, `restart`, `logs` or `upgrade`. An `update` comes only from `POST /v1/updates/rollouts`. |
| `service` | string | For `start`, `stop` and `restart` | Body. The container's name. With `logs`, read this service's logs; leave it out to read the machine's journal. |
| `unit` | string | No | Body. Machine logs only. Only this systemd unit, such as `tenderd` or `nginx.service`. |
| `priority` | string | No | Body. Machine logs only. Only this priority and the more severe ones: `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info` or `debug`. |
| `since` | string | No | Body. Logs only. Only entries from this far back, such as `30m`, `1h` or `7d`, at most `7d`. |
| `lines` | integer | No | Body. Logs only. How many of the newest lines, from 1 to 2000. The default is 200. |
| `packages` | string | No | Body. Upgrade only. `security`, the default, or `all`. |

#### Example request

```sh
curl -s -X POST https://apps.your-tailnet.ts.net/v1/machines/apps/actions \
  -H 'Content-Type: application/json' \
  -d '{"type": "restart", "service": "web"}'
```

#### Example response

```http
HTTP/1.1 202 Accepted
Content-Type: application/json
Location: /v1/machines/apps/actions/14

{
  "id": 14,
  "machine": "apps",
  "type": "restart",
  "service": "web",
  "unit": null,
  "priority": null,
  "since": null,
  "lines": null,
  "version": null,
  "packages": null,
  "status": "pending",
  "output_cleared": false,
  "created_at": "2026-11-15T11:40:20Z",
  "updated_at": "2026-11-15T11:40:20Z",
  "output": null
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | The machine doesn't exist, or it has no service with this name. |
| `409` | The service was removed: Docker on the machine no longer has its container. |
| `422` | The type isn't one of the five, a service action has no service or has a logs field, a service's logs action has `unit` or `priority`, a logs field isn't valid, `packages` isn't `security` or `all` or comes without `upgrade`, or the type is `update`. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `start_service`, `stop_service`, `restart_service`, `get_service_logs`, `get_machine_logs` or `upgrade_machine` | `tender services start\|stop\|restart\|logs apps web`, `tender logs apps`, or `tender machines upgrade apps` |

### `GET /v1/machines/{machine}/actions`

Lists a machine's actions, newest first, without their output.

`output_cleared` is `true` once the hub has cleared a completed logs action's output.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/actions
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "id": 7,
      "machine": "apps",
      "type": "logs",
      "service": "web",
      "unit": null,
      "priority": null,
      "since": "10m",
      "lines": 3,
      "version": null,
      "packages": null,
      "status": "completed",
      "output_cleared": true,
      "created_at": "2026-11-15T11:28:14Z",
      "updated_at": "2026-11-15T11:28:35Z"
    },
    {
      "id": 6,
      "machine": "apps",
      "type": "logs",
      "service": null,
      "unit": "tenderd",
      "priority": "warning",
      "since": "1h",
      "lines": 2,
      "version": null,
      "packages": null,
      "status": "completed",
      "output_cleared": true,
      "created_at": "2026-11-15T11:28:13Z",
      "updated_at": "2026-11-15T11:28:34Z"
    },
    {
      "id": 1,
      "machine": "apps",
      "type": "restart",
      "service": "web",
      "unit": null,
      "priority": null,
      "since": null,
      "lines": null,
      "version": null,
      "packages": null,
      "status": "completed",
      "output_cleared": false,
      "created_at": "2026-11-15T11:28:07Z",
      "updated_at": "2026-11-15T11:28:07Z"
    }
  ]
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The request has `wait`, which only the machine's daemon may send. |
| `404` | The machine doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_actions` | `tender actions list apps` |

### `GET /v1/machines/{machine}/actions/{id}`

Returns one action with its output: the logs, or why it failed.

`output` is `null` while the action runs, for a completed start, stop or restart, and once the hub has cleared a completed logs action's output. A failed action keeps why it failed. A machine logs line is the time in UTC, the unit and the message. A service logs line is the time and the message.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `id` | integer | Yes | Path. The action's id. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/machines/apps/actions/8
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 8,
  "machine": "apps",
  "type": "logs",
  "service": null,
  "unit": null,
  "priority": "warning",
  "since": null,
  "lines": 5,
  "version": null,
  "packages": null,
  "status": "completed",
  "output_cleared": false,
  "created_at": "2026-11-15T11:28:15Z",
  "updated_at": "2026-11-15T11:28:15Z",
  "output": "2026-11-15T11:27:15.000000000Z tenderd.service backup took 41s, longer than usual.\n2026-11-15T11:28:15.000000000Z kernel eth0: link up\n"
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or it has no action with this id. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_action` | `tender actions show apps 8` |

## Updates

Tender updates itself from the hub, one machine at a time. [Updates](updates.md) explains how.

### `GET /v1/updates`

Returns the update settings, what the hub last learned from GitHub about the newest stable release, the rollout in progress, and each machine's version.

`latest.auto` is `false` when the release's `tender-release.json` says a person must roll it out. `data[].version` is `null` for a daemon before v0.3.0. `data[].updates` is the machine's own setting, and `data[].mode` the mode that applies to it.

#### Parameters

None.

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/updates
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "mode": "auto",
  "window": "02:00-05:00",
  "created_at": "2026-11-14T08:00:00Z",
  "updated_at": "2026-11-15T09:12:00Z",
  "latest": {"version": "0.3.1", "auto": true, "checked_at": "2026-11-15T09:12:00Z", "error": null},
  "rollout": null,
  "data": [
    {"machine": "apps", "version": "0.3.0", "updates": "inherit", "mode": "auto", "update_available": true},
    {"machine": "db", "version": "0.3.1", "updates": "manual", "mode": "manual", "update_available": false}
  ]
}
```

#### Errors

None of its own.

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_updates` | `tender updates show` |

### `GET /v1/updates/settings`

Returns the hub's update settings.

#### Parameters

None.

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/updates/settings
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "mode": "auto",
  "window": "02:00-05:00",
  "created_at": "2026-11-14T08:00:00Z",
  "updated_at": "2026-11-15T09:12:00Z"
}
```

#### Errors

None of its own.

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_updates` | `tender updates show` |

### `PUT /v1/updates/settings`

Replaces the hub's update settings. The hub looks at GitHub again within 10 seconds.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `mode` | string | Yes | Body. `auto` rolls out each new stable release by itself; `manual` waits for a rollout someone starts. |
| `window` | string | No | Body. When auto mode may start a rollout, two UTC times such as `02:00-05:00`. It may cross midnight. Leave it out, or send `null` or `""`, for any time. |

#### Example request

```sh
curl -s -X PUT https://apps.your-tailnet.ts.net/v1/updates/settings \
  -H 'Content-Type: application/json' \
  -d '{"mode": "auto", "window": "02:00-05:00"}'
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "mode": "auto",
  "window": "02:00-05:00",
  "created_at": "2026-11-14T08:00:00Z",
  "updated_at": "2026-11-15T11:45:00Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `422` | `mode` isn't `auto` or `manual`, or `window` isn't two different times such as `02:00-05:00`. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `set_update_settings` | `tender updates set --mode auto --window 02:00-05:00` |

### `GET /v1/updates/rollouts`

Lists the rollouts the hub keeps, the newest 50, newest first. Each has its machines in the order they update.

#### Parameters

None.

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/updates/rollouts
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "id": 3,
      "version": "0.3.1",
      "status": "completed",
      "auto": true,
      "error": null,
      "machines": [
        {"machine": "db", "position": 1, "status": "completed", "action_id": 21, "error": null,
         "created_at": "2026-11-15T02:00:04Z", "updated_at": "2026-11-15T02:01:31Z"},
        {"machine": "apps", "position": 2, "status": "completed", "action_id": 40, "error": null,
         "created_at": "2026-11-15T02:00:04Z", "updated_at": "2026-11-15T02:03:02Z"}
      ],
      "created_at": "2026-11-15T02:00:04Z",
      "updated_at": "2026-11-15T02:03:12Z"
    }
  ]
}
```

A rollout's `status` is `in_progress`, `completed` or `failed`, and `auto` is `true` when auto mode started it. A machine's `status` is `pending`, `in_progress`, `completed` or `failed`, with the reason in `error`, and `action_id` is its update action on that machine.

#### Errors

None of its own.

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_updates` | `tender updates show` |

### `POST /v1/updates/rollouts`

Starts a rollout of a stable release to the machines named, or to every machine on Tender v0.3.0 or later, and answers `202 Accepted` with the rollout. The hub's own machine goes last. Follow it with `GET /v1/updates/rollouts/{id}`.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | string | No | Body. The release, such as `0.3.1`. Leave it out for the latest. |
| `machines` | array of strings | No | Body. The machines to update, by name. Leave it out, or send `[]`, for every machine. |

#### Example request

```sh
curl -s -X POST https://apps.your-tailnet.ts.net/v1/updates/rollouts \
  -H 'Content-Type: application/json' \
  -d '{"version": "0.3.1", "machines": []}'
```

#### Example response

```http
HTTP/1.1 202 Accepted
Content-Type: application/json
Location: /v1/updates/rollouts/3

{
  "id": 3,
  "version": "0.3.1",
  "status": "in_progress",
  "auto": false,
  "error": null,
  "machines": [
    {"machine": "db", "position": 1, "status": "pending", "action_id": null, "error": null,
     "created_at": "2026-11-15T11:46:00Z", "updated_at": "2026-11-15T11:46:00Z"},
    {"machine": "apps", "position": 2, "status": "pending", "action_id": null, "error": null,
     "created_at": "2026-11-15T11:46:00Z", "updated_at": "2026-11-15T11:46:00Z"}
  ],
  "created_at": "2026-11-15T11:46:00Z",
  "updated_at": "2026-11-15T11:46:00Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | A machine named doesn't exist. |
| `409` | Another rollout is still in progress. |
| `422` | The version isn't a stable release on GitHub, a machine runs a Tender before v0.3.0, or a machine runs a newer version, which an update never downgrades. |
| `502` | The hub couldn't ask GitHub for the latest release. Name a version, or try again. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `start_update` | `tender update --all`, or `tender update apps db` |

### `GET /v1/updates/rollouts/{id}`

Returns one rollout with its machines, as `GET /v1/updates/rollouts` lists it.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | integer | Yes | Path. The rollout's id. |

#### Example request

```sh
curl -s https://apps.your-tailnet.ts.net/v1/updates/rollouts/4
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 4,
  "version": "0.3.2",
  "status": "failed",
  "auto": true,
  "error": "The rollout stopped at db, which failed to update. The machines after it were not updated.",
  "machines": [
    {"machine": "db", "position": 1, "status": "failed", "action_id": 52,
     "error": "Tender v0.3.2 failed its health check, so it rolled back to Tender 0.3.1: after 2m0s, the daemon has not reported to the hub on Tender 0.3.2.",
     "created_at": "2026-11-16T02:00:04Z", "updated_at": "2026-11-16T02:04:40Z"},
    {"machine": "apps", "position": 2, "status": "pending", "action_id": null, "error": null,
     "created_at": "2026-11-16T02:00:04Z", "updated_at": "2026-11-16T02:00:04Z"}
  ],
  "created_at": "2026-11-16T02:00:04Z",
  "updated_at": "2026-11-16T02:04:40Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The rollout doesn't exist. The hub keeps the newest 50. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_updates` | `tender updates show` |

## Daemon routes

The daemons use these routes to report and to take their work. Each needs the machine's own token as `Authorization: Bearer <token>`, and you never call them yourself.

| Route | What the daemon does with it |
| --- | --- |
| `POST /v1/machines/{machine}/usage` | Sends a reading of CPU load, memory, disk and uptime, once a minute, with the Tender `version` it runs. |
| `GET /v1/machines/{machine}/backups/settings` | Gets the machine's settings with the destination's keys and password. People get the same route without them. |
| `PUT /v1/machines/{machine}/backups/snapshots` | Replaces the hub's list of the machine's snapshots after each backup and cleanup. |
| `POST /v1/machines/{machine}/backups/history` | Records a run as it starts, or a finished run it couldn't send while the hub was down. |
| `PATCH /v1/machines/{machine}/backups/history/{id}` | Records how a run ended. |
| `PUT /v1/machines/{machine}/services` | Sends `{"docker": "running", "services": [...]}` with every container, `[]` for none, or `{"docker": "unreachable", "docker_reason": "refused"}` with no list, once a minute and after each start, stop or restart. A running container whose stats couldn't be read has `"stats_unavailable": true` and no usage. |
| `GET /v1/machines/{machine}/actions?wait=25` | Waits up to 25 seconds, at most 30, for the oldest pending action, and gets it without changing its status. |
| `PUT /v1/machines/{machine}/security` | Sends the security checks, when the daemon starts and every 15 minutes, and after an upgrade. Answers `204`. |
| `PATCH /v1/machines/{machine}/actions/{id}` | Claims an action with `{"status": "in_progress"}`, which answers `204`, or `409` when it isn't pending. Then records how it ended, with `completed` or `failed` and its `output`, which only those two take. |

## Next steps

1. [MCP reference](mcp.md) has the same operations as tools for an AI client.
2. [CLI reference](cli.md) has the `tender` commands that call these routes.
3. [Security](security.md) explains who can reach the hub.
