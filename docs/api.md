# REST API

The hub's REST API does everything the `tender` command and the MCP tools do, over HTTP and JSON.

## Prerequisites

1. A running hub. See [Install](install.md).
2. The hub's address. The examples use `https://apps.your-tailnet.ts.net`, the hub's name on a tailnet. On the hub machine itself, use `http://127.0.0.1:8080`.
3. An API token in `TENDER_TOKEN`. Make the first one on the hub machine with `sudo tender tokens add NAME`, as [Tokens](#tokens) explains.
4. `curl`, or any HTTP client.

## Conventions

### Authentication

Every route below needs an API token, as `Authorization: Bearer <token>`, except `GET /v1/health`. `POST /mcp` needs it too. An API token starts with `tender_`, and [Tokens](#tokens) makes, lists and deletes them. The examples send it from `TENDER_TOKEN`:

```sh
export TENDER_TOKEN=tender_...
```

A request without a token, or with a wrong one, gets `401`:

```http
HTTP/1.1 401 Unauthorized
Content-Type: application/problem+json

{
  "detail": "Send an API token as \"Authorization: Bearer <token>\"; the tender command sends TENDER_TOKEN. Make the first one on the hub machine with: sudo tender tokens add NAME",
  "status": 401,
  "title": "Unauthorized",
  "type": "about:blank"
}
```

A hub updated to v0.4 has no API token until you make one, so until then it refuses every request but health and its daemons'.

The hub refuses a browser request from another website that would change something, and a request for a host name it doesn't answer to. Both get `403`. [Security](security.md#who-can-reach-the-hub) lists the names it answers to.

Daemons sign in with their machine's token, as `Authorization: Bearer <token>`, and a token works only for its own machine. A wrong token gets `401`, another machine's token gets `403`, and a machine that doesn't exist gets `404`. A machine token never works as an API token, and an API token never works as a machine's: on a route both may call, the hub tells them apart by looking the token up.

### Requests and responses

1. Every route is under `/v1`. `GET /v1/health` answers `{"status": "ok"}` while the hub can reach its database, and `503` when it can't.
2. JSON fields have the same names as the database columns, such as `retention_days` and `created_at`.
3. Times are RFC 3339 in UTC, such as `2026-11-15T11:27:26Z`.
4. A request body is one JSON object of at most 1 MiB, and a larger one gets `413`. A field the route doesn't know is `400`, and a field of the wrong type is `422`. JSON that isn't an object, such as `[]`, gets `422` with `The request body must be a JSON object.`
5. A collection answers `{"data": [...]}`. A paged collection takes `limit`, from 1 to 500 with a default of 50, and adds `next_cursor` when there are more. Pass it as `cursor` for the next page. The last page has no `next_cursor`.
6. A create answers `201 Created` with the new object and its address in the `Location` header.
7. `PATCH` takes a JSON object with only the fields to change. The hub reads it as JSON whatever its `Content-Type`, so `application/merge-patch+json` and `application/json` both work. A field you leave out stays as it is. Inside a `config`, `null` removes an optional field and a secret sent as `"***"` stays as it is.
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

Starting, stopping and restarting a service, reading logs and testing a destination happen on the machine, so `POST /v1/machines/{machine}/actions` and `POST /v1/backups/destinations/{destination}/test` answer `202 Accepted` at once with the action as `pending`. Poll `GET /v1/machines/{machine}/actions/{id}`, about once a second, until its `status` is `completed` or `failed`. The daemon usually picks an action up within a second. [Services](services.md#how-actions-reach-a-machine) explains the steps, and when an action fails because the daemon didn't pick it up or didn't say how it ended.

## Tokens

An API token lets a person, a script or an AI client use the REST API and `POST /mcp`. The hub stores only its SHA-256 hash, and shows the token once, when it is made.

### `GET /v1/tokens`

Lists the API tokens by name, with when each was last used. The tokens themselves are never shown.

`last_used_at` is the last request with the token, to the minute, and `null` before the first.

#### Parameters

None.

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/tokens
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "id": 2,
      "name": "claude-code",
      "last_used_at": null,
      "created_at": "2026-11-15T12:04:10Z",
      "updated_at": "2026-11-15T12:04:10Z"
    },
    {
      "id": 1,
      "name": "laptop",
      "last_used_at": "2026-11-15T12:05:00Z",
      "created_at": "2026-11-15T12:00:00Z",
      "updated_at": "2026-11-15T12:00:00Z"
    }
  ]
}
```

#### Errors

| Status | When |
| --- | --- |
| `401` | The request has no valid API token. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_tokens` | `tender tokens list` |

### `POST /v1/tokens`

Makes an API token and returns it, shown only this once.

The token is `tender_` and 32 random bytes in base64url. This route needs a token itself, so the first one comes from `sudo tender tokens add NAME` on the hub machine, which writes to the hub's database directly. No MCP tool makes a token, so an AI client can't give itself more access.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | Body. Lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters, such as `laptop` or `claude-code`. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/tokens \
  -H 'Content-Type: application/json' \
  -d '{"name": "claude-code"}'
```

#### Example response

```http
HTTP/1.1 201 Created
Content-Type: application/json
Location: /v1/tokens/claude-code

{
  "id": 2,
  "name": "claude-code",
  "last_used_at": null,
  "created_at": "2026-11-15T12:04:10Z",
  "updated_at": "2026-11-15T12:04:10Z",
  "token": "tender_<43 characters>"
}
```

#### Errors

| Status | When |
| --- | --- |
| `401` | The request has no valid API token. |
| `409` | A token has this name. Choose another, or delete it first. |
| `422` | The name breaks the naming rule. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| None, so an AI client can't make itself more access. | `tender tokens add claude-code` |

### `DELETE /v1/tokens/{token}`

Deletes an API token by name. Every client that sends it is refused from its next request on.

> [!WARNING]
> Deleting the last token locks everyone out of the REST API and `POST /mcp` until you make a new one with `sudo tender tokens add NAME` on the hub machine. The daemons keep working.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `token` | string | Yes | Path. The token's name. |

#### Example request

```sh
curl -s -X DELETE -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/tokens/claude-code
```

#### Example response

```http
HTTP/1.1 204 No Content
```

#### Errors

| Status | When |
| --- | --- |
| `401` | The request has no valid API token. |
| `404` | No token has this name. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `delete_token` | `tender tokens remove claude-code --yes` |

## Machines

A machine is a server with a daemon that reports to the hub.

### `GET /v1/machines`

Lists every machine with its latest reading of CPU load, memory, disk and uptime, by name.

`last_seen` and `usage` are `null` until the machine's daemon first reports.

#### Parameters

None.

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines
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
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines \
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps
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
curl -s -X DELETE -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps
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
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/token
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/usage?limit=2
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

Changes a machine's settings: its own update mode, the ports that may listen on a public address without a notification, and whether its security alerts are on.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `updates` | string | No | Body. `inherit` follows the hub's update mode, as [Updates](updates.md#choose-auto-or-manual) explains; `auto` or `manual` is the machine's own. |
| `security_allowed_ports` | array of strings | No | Body. The whole allowlist, which replaces the old one: ports with their protocol, such as `["41641/udp", "443/tcp"]`, and programs as `ss` names them, such as `"cloudflared"`, whose every port is allowed, at most 100 in all. A program's name is 1 to 32 letters, digits, dots, dashes and underscores, not digits only. |
| `security_alerts` | boolean | No | Body. Whether the machine's security alerts open. Off, its open ones resolve within a minute. |

#### Example request

```sh
curl -s -X PATCH -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps \
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/security
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/backups
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/backups
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/backups/settings
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
curl -s -X PUT -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/backups/settings \
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
curl -s -X DELETE -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/backups/settings
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/backups/snapshots?limit=2
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/backups/snapshots/10
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/backups/history?limit=2
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/backups/history/20
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

A destination is a place backups go: a `type` and a `config` with the type's fields, and the repository password that every machine's backups there use. [Backups](backups.md#destination-types) explains each type.

A destination's JSON has `id`, `name`, `type`, `config`, `location`, `created_at` and `updated_at`. In `config`, each secret shows as `"***"`, with `<field>_set` saying whether it is set. `location` is the restic repository; each machine's repository is a folder named after the machine in it.

| Type | `config` fields | Secrets |
| --- | --- | --- |
| `s3` | `endpoint`, `bucket`, `path` (optional) | `access_key_id`, `secret_access_key` |
| `local` | `path`, absolute | None. |
| `sftp` | `host`, `port` (default 22), `user`, `path` | None. The SSH key stays on each machine. |
| `rest` | `url`, `username` (optional) | `password`, with `username` |
| `rclone` | `remote`, such as `gdrive:backups` | None. The remote's login stays on each machine. |

Every path, the folder in a `rest` URL and the folder after an rclone remote's colon is made of folder names of letters, digits, `.`, `_` and `-`, without `.` or `..` parts. A `?`, `#`, `%` or space is refused, because restic would read it as another repository than the one the hub checked. `bucket` follows the S3 rules: 3 to 63 lowercase letters, digits, dots and hyphens, starting and ending with a letter or digit. `endpoint` is the server's address alone, without a path or a query.

### `GET /v1/backups/destinations`

Lists every destination, by name. Secrets show as `"***"`, and the password is never part of the response.

#### Parameters

None.

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/backups/destinations
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

| Status | When |
| --- | --- |
| `500` | The hub failed to read its database. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_destinations` | `tender destinations list` |

### `POST /v1/backups/destinations`

Adds a destination. Without a password, the hub makes one and returns it this once.

`password` appears in the response only when the hub made it. Save it in a password manager; [Backups](backups.md#encryption-and-the-repository-password) explains why. The CLI asks for the keys with typing hidden, which keeps them out of your shell history. Try the destination from a machine with [`POST /v1/backups/destinations/{destination}/test`](#post-v1backupsdestinationsdestinationtest) before you back up to it.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | Body. Lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |
| `type` | string | No | Body. `s3`, the default, `local`, `sftp`, `rest` or `rclone`. |
| `config` | object | Yes | Body. The type's fields, from the table above. The hub stores the secrets encrypted. |
| `password` | string | No | Body. The repository password. Leave it out and the hub makes a strong one. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/backups/destinations \
  -H 'Content-Type: application/json' \
  -d '{"name": "r2", "config": {"endpoint": "https://<account-id>.r2.cloudflarestorage.com", "bucket": "my-backups", "path": "tender", "access_key_id": "<access key ID>", "secret_access_key": "<secret access key>"}}'
```

An SFTP destination:

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/backups/destinations \
  -H 'Content-Type: application/json' \
  -d '{"name": "nas", "type": "sftp", "config": {"host": "nas.example.com", "user": "tender", "path": "/srv/restic"}}'
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
  "updated_at": "2026-11-15T11:27:26Z",
  "password": "<password>"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `409` | A destination with this name, or at this location, already exists. |
| `422` | The name or the type isn't valid, `config` is missing, or a config field is missing, unknown for the type, or not valid, such as an endpoint that isn't an http or https URL, or a secret given as `"***"`. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `add_destination` | `tender destinations add r2 --endpoint URL --bucket my-backups --path tender` |

### `GET /v1/backups/destinations/{destination}`

Returns one destination, with its secrets shown as `"***"` and without its password.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | Path. The destination's name. |

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/backups/destinations/r2
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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

Changes fields of a destination's config, as a JSON merge patch: a field with a value replaces it, `null` removes an optional one, and a secret sent as `"***"` stays as it is, so you can send back what a read gave you. A PATCH can replace a secret but never read it. The type and the password can't change, because the repositories depend on them.

Move the data yourself before you point a destination at another place. Daemons check for the new config every 10 minutes.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | Path. The destination's name. |
| `config` | object | No | Body. The fields to change. |

#### Example request

```sh
curl -s -X PATCH -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/backups/destinations/r2 \
  -H 'Content-Type: application/merge-patch+json' \
  -d '{"config": {"access_key_id": "<new access key ID>", "secret_access_key": "<new secret access key>"}}'
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
  "updated_at": "2026-11-15T11:40:02Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | The destination doesn't exist. |
| `409` | Another destination already uses the new location. |
| `422` | The body has `password` or `type`, or the config that results isn't valid for the type, such as one without a bucket. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `update_destination` | `tender destinations update r2 --access-key-id-file PATH --secret-access-key-file PATH` |

### `POST /v1/backups/destinations/{destination}/test`

Tries a destination from one machine. Answers `202` with an [action](#actions): the machine's daemon opens that machine's repository at the destination with `restic cat config`, which checks the address, the login and the password, or makes the repository with `restic init` when there is none yet, as the first backup would.

The hub hands the destination's config, with its secrets, to that daemon with the action, and never stores them with the action. Read the result with [`GET /v1/machines/{machine}/actions/{id}`](#get-v1machinesmachineactionsid): `output` says which repository the daemon reached and whether it made it, or why it failed.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | Path. The destination's name. |
| `machine` | string | Yes | Body. The machine whose daemon tries it. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/backups/destinations/nas/test \
  -H 'Content-Type: application/json' \
  -d '{"machine": "apps"}'
```

#### Example response

```http
HTTP/1.1 202 Accepted
Content-Type: application/json
Location: /v1/machines/apps/actions/12

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
  "status": "pending",
  "output_cleared": false,
  "created_at": "2026-11-15T12:02:11Z",
  "updated_at": "2026-11-15T12:02:11Z",
  "output": null
}
```

Once it finished, the action's `output` is a sentence such as `Reached sftp://tender@nas.example.com:22//srv/restic/apps from apps. It had no repository for apps yet, so the test made one with the destination's password.` A failed test has the status `failed` and restic's reason, such as `Could not reach the destination nas from apps: the sftp destination needs /etc/tender/ssh/id_ed25519 on this machine, and it isn't there.`

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | The destination doesn't exist. |
| `422` | `machine` is missing, or no machine has that name. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `test_destination` | `tender destinations test nas apps` |

### `DELETE /v1/backups/destinations/{destination}`

Removes a destination that no machine backs up to.

> [!WARNING]
> The hub forgets the destination's keys and password. The backups already at the destination stay there, and you need the password from your password manager to restore them.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `destination` | string | Yes | Path. The destination's name. |

#### Example request

```sh
curl -s -X DELETE -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/backups/destinations/r2
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

A notification is one message about a machine, its backups, a service or a notification channel. A problem adds one when it starts, such as `service.down`, one the first time it gets worse, `service.down.escalated`, and one when it clears, `service.down.resolved`. Each goes to every enabled channel whose `events` and `min_severity` take it, as one delivery per channel. See [Notifications](notifications.md) for each event and [Adapters](adapters.md) for the channel types.

### `GET /v1/notifications`

Lists every machine's notifications, newest first, or with `open=true`, the open problems.

A notification has an `event`, a `severity` and a `message`. Its `status` is `pending` until a channel delivers it, `sent` once one did, or `failed` once every channel's delivery failed, as [Notifications](notifications.md#how-a-notification-works) explains. `sent_at` is when a channel first delivered it, and `null` until then. `machine` is the machine it is about, and `service` is `null` for one that isn't about one service. `channel` names the channel a `channel.failing` notification is about, and is `null` for every other. `deliveries` lists its delivery to each channel, oldest first: its `channel`, `status` (`pending`, `delivered` or `failed`), `attempts` and `error`, which says why the last try failed.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `open` | boolean | No | Query. `true` lists only the open problems: the latest notification of each problem whose latest isn't a `.resolved` one. The default is `false`. |
| `status` | string | No | Query. Only notifications with this status: `pending`, `sent` or `failed`. |
| `severity` | string | No | Query. Only notifications with this severity: `info`, `warning` or `critical`. |
| `limit` | integer | No | Query. How many to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | Query. The `next_cursor` of the previous page. |

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/notifications?limit=1
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "id": 4,
      "machine": "apps",
      "service": "notes",
      "container": "web",
      "channel": null,
      "event": "service.memory_high.resolved",
      "severity": "warning",
      "message": "Resolved: web in notes on apps uses 950 MiB, 92% of its 1 GiB memory limit.",
      "status": "sent",
      "sent_at": "2026-11-15T11:36:36Z",
      "created_at": "2026-11-15T11:36:35Z",
      "updated_at": "2026-11-15T11:36:36Z",
      "deliveries": [
        {
          "id": 8,
          "notification_id": 4,
          "channel": "ops",
          "status": "delivered",
          "attempts": 1,
          "error": null,
          "created_at": "2026-11-15T11:36:35Z",
          "updated_at": "2026-11-15T11:36:36Z"
        }
      ]
    }
  ],
  "next_cursor": "MTc5NDc0MjU5NTo0"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | `limit` isn't a whole number from 1 to 500, or `cursor` isn't valid. |
| `422` | `open`, `status` or `severity` isn't one of its values. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_notifications` | `tender notifications list`, and `tender status`, which shows the open problems. |

### `GET /v1/machines/{machine}/notifications`

Lists one machine's notifications, newest first, in the same shape as [`GET /v1/notifications`](#get-v1notifications).

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `open` | boolean | No | Query. `true` lists only the machine's open problems. |
| `status` | string | No | Query. `pending`, `sent` or `failed`. |
| `severity` | string | No | Query. `info`, `warning` or `critical`. |
| `limit` | integer | No | Query. How many to return, from 1 to 500. The default is 50. |
| `cursor` | string | No | Query. The `next_cursor` of the previous page. |

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" 'https://apps.your-tailnet.ts.net/v1/machines/apps/notifications?open=true&limit=1'
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "id": 2,
      "machine": "apps",
      "service": null,
      "container": null,
      "channel": null,
      "event": "docker.unreachable",
      "severity": "critical",
      "message": "Docker on apps can't be reached: its socket refuses connections, so Docker is stopped. Its services' states are unknown until it is back.",
      "status": "sent",
      "sent_at": "2026-11-15T11:32:36Z",
      "created_at": "2026-11-15T11:32:35Z",
      "updated_at": "2026-11-15T11:32:36Z",
      "deliveries": [
        {
          "id": 4,
          "notification_id": 2,
          "channel": "ops",
          "status": "delivered",
          "attempts": 1,
          "error": null,
          "created_at": "2026-11-15T11:32:35Z",
          "updated_at": "2026-11-15T11:32:36Z"
        },
        {
          "id": 5,
          "notification_id": 2,
          "channel": "pager",
          "status": "pending",
          "attempts": 2,
          "error": "HTTP 503 Service Unavailable",
          "created_at": "2026-11-15T11:32:35Z",
          "updated_at": "2026-11-15T11:33:50Z"
        }
      ]
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
| `422` | `open`, `status` or `severity` isn't one of its values. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_notifications` | `tender notifications list MACHINE` |

### `GET /v1/machines/{machine}/notifications/{id}`

Returns one of a machine's notifications, with its deliveries.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `id` | integer | Yes | Path. The notification's id. |

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/notifications/3
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or it has no notification with this id. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_notifications` | `tender notifications list MACHINE` |

### `GET /v1/notifications/channels`

Lists the channels notifications go to, in the order you added them.

A channel has a `type` (`email`, `slack`, `webhook` or `exec`), a `config` whose keys depend on the type, `enabled`, `min_severity`, the least severe notification it gets, and `events`, the patterns of the events it gets, such as `["*"]` or `["service.*", "backup.failed"]`. A secret in `config` shows as `"***"`, with `<name>_set` set to `true`. [Adapters](adapters.md#channel-types) lists each type's config.

#### Parameters

None.

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/notifications/channels
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "id": 1,
      "name": "ops",
      "type": "email",
      "config": {
        "to": "ops@example.com"
      },
      "enabled": true,
      "min_severity": "warning",
      "events": [
        "*"
      ],
      "created_at": "2026-11-15T11:27:26Z",
      "updated_at": "2026-11-15T11:27:26Z"
    },
    {
      "id": 2,
      "name": "chat",
      "type": "slack",
      "config": {
        "webhook_url": "***",
        "webhook_url_set": true
      },
      "enabled": true,
      "min_severity": "critical",
      "events": [
        "*"
      ],
      "created_at": "2026-11-15T11:28:02Z",
      "updated_at": "2026-11-15T11:28:02Z"
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
| `list_channels` | `tender channels list` |

### `POST /v1/notifications/channels`

Adds a channel. It gets every notification of its `events` at least as severe as its `min_severity`, from now on, and the latest notification of an open problem that no channel got.

For a `webhook` channel the hub makes the signing secret. This answer is the only time it shows it: give it to the receiver, which checks the `Tender-Signature` header with it, as [Adapters](adapters.md#verify-a-webhook) shows.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | Body. Lowercase letters, digits and hyphens, up to 63 characters, such as `ops-slack`. |
| `type` | string | Yes | Body. `email`, `slack`, `webhook` or `exec`. |
| `config` | object | Yes | Body. The type's config, all strings. `email`: `{"to": "ops@example.com"}`. `slack`: `{"webhook_url": "https://hooks.slack.com/services/..."}`. `webhook`: `{"url": "https://..."}`. `exec`: `{"command": "my-adapter"}`. |
| `enabled` | boolean | No | Body. Whether it gets notifications. The default is `true`. |
| `min_severity` | string | No | Body. `info`, `warning` or `critical`. The default is `warning`. |
| `events` | array | No | Body. The events it gets, each `*`, an area such as `service.*`, or an event such as `backup.failed`. An escalated or resolved notification follows its base event. The default is `["*"]`. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/notifications/channels \
  -H 'Content-Type: application/json' \
  -d '{"name": "pager", "type": "webhook", "config": {"url": "https://pager.example.com/tender"}}'
```

#### Example response

```http
HTTP/1.1 201 Created
Content-Type: application/json
Location: /v1/notifications/channels/pager

{
  "id": 3,
  "name": "pager",
  "type": "webhook",
  "config": {
    "secret": "whsec_kq0W2jq7a8Lx3n8vR1tYb5sU9eFh4cGd6pJm0zXwQ2E",
    "secret_set": true,
    "url": "https://pager.example.com/tender"
  },
  "enabled": true,
  "min_severity": "warning",
  "events": [
    "*"
  ],
  "created_at": "2026-11-15T11:30:11Z",
  "updated_at": "2026-11-15T11:30:11Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `409` | A channel with this name exists. |
| `422` | The name, type, severity, events or config isn't valid: a config key the type doesn't take or lacks, an address that isn't one bare address, a URL that isn't http or https or leads to an address the hub doesn't send to, a Slack URL not on `https://hooks.slack.com/`, a `secret` in a webhook's create, an exec `command` that isn't an executable in `/etc/tender/adapters`, or an events pattern that takes no event. The detail says which. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `add_channel` | `tender channels add pager --type webhook --url https://pager.example.com/tender` |

### `GET /v1/notifications/channels/{channel}`

Returns one channel, with its secrets hidden.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `channel` | string | Yes | Path. The channel's name. |

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/notifications/channels/pager
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 3,
  "name": "pager",
  "type": "webhook",
  "config": {
    "secret": "***",
    "secret_set": true,
    "url": "https://pager.example.com/tender"
  },
  "enabled": true,
  "min_severity": "warning",
  "events": [
    "*"
  ],
  "created_at": "2026-11-15T11:30:11Z",
  "updated_at": "2026-11-15T11:30:11Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | No channel has this name. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_channels` | `tender channels show pager` |

### `PATCH /v1/notifications/channels/{channel}`

Changes a channel's config, turns it on or off, or changes its `min_severity` or `events`. Fields left out stay as they are. A `config` key replaces that value; a secret can be replaced but never read.

Turning a channel off fails what it still owed, with the error `The channel was turned off before this was delivered.`, so it sends nothing stale when it is turned on again. Turning it on or off also starts its count toward `channel.failing` again.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `channel` | string | Yes | Path. The channel's name. |
| `config` | object | No | Body. Config keys to change. `null` removes a key, which only an optional one allows. A webhook's new `secret` must be at least 24 characters. |
| `enabled` | boolean | No | Body. `true` or `false`. |
| `min_severity` | string | No | Body. `info`, `warning` or `critical`. |
| `events` | array | No | Body. The events it gets, replacing the list. |

#### Example request

```sh
curl -s -X PATCH -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/notifications/channels/chat \
  -H 'Content-Type: application/merge-patch+json' \
  -d '{"config": {"webhook_url": "<new Slack webhook URL>"}, "min_severity": "warning"}'
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 2,
  "name": "chat",
  "type": "slack",
  "config": {
    "webhook_url": "***",
    "webhook_url_set": true
  },
  "enabled": true,
  "min_severity": "warning",
  "events": [
    "*"
  ],
  "created_at": "2026-11-15T11:28:02Z",
  "updated_at": "2026-11-15T11:41:17Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know, such as `name` or `type`, which can't change. |
| `404` | No channel has this name. |
| `422` | A value isn't valid, a required key is removed, or a secret is set to `"***"`. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `update_channel` | `tender channels update chat --webhook-url-file PATH --min-severity warning` |

### `DELETE /v1/notifications/channels/{channel}`

Removes a channel and its record of deliveries. Notifications are still recorded when no channel is left.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `channel` | string | Yes | Path. The channel's name. |

#### Example request

```sh
curl -s -X DELETE -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/notifications/channels/chat
```

#### Example response

```http
HTTP/1.1 204 No Content
```

#### Errors

| Status | When |
| --- | --- |
| `404` | No channel has this name. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `remove_channel` | `tender channels remove chat` |

### `POST /v1/notifications/test`

Sends a test notification, event `notification.test`, to every enabled channel, or to the one you name even when it is off, once each and all at once. It answers when every channel has answered, after at most 10 seconds, or 30 for email.

The test is one notification, with one delivery per channel, and `GET /v1/notifications` lists it. A test that gets through also resolves the channel's `channel.failing` problem. How each went is recorded even when the request ends first. An email channel's subject is `Tender test email`.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `channel` | string | No | Body. Only this channel. Leave the body out to test every enabled channel. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/notifications/test
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
    },
    {
      "id": 10,
      "notification_id": 5,
      "channel": "pager",
      "status": "failed",
      "attempts": 1,
      "error": "HTTP 404 Not Found",
      "created_at": "2026-11-15T11:42:00Z",
      "updated_at": "2026-11-15T11:42:00Z"
    }
  ]
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | No channel has the name you gave. |
| `422` | No channel is enabled, and you named none. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `send_test_notification` | `tender channels test`, or `tender channels test pager` |

## Services

A service is a compose project on a machine, made of containers, or a container without a compose project. A managed service is one Tender deployed from a compose file; an unmanaged one is one Tender found running. See [Services](services.md), and [Exposure](exposure.md) for routes.

A service has these fields:

| Field | What it holds |
| --- | --- |
| `name` | The compose project name, or the lone container's name. |
| `managed` | `true` when Tender deployed it. |
| `state` | `running`, `degraded`, `stopped` or `unknown`, from its containers. See [What a service is](services.md#what-a-service-is). |
| `deploy_status` | `deploying`, `deployed`, `failed` or `removing`, or `null` when unmanaged. |
| `last_error` | Why the last deploy or remove failed: its first line, then the end of compose's output. |
| `deployed_at` | When a deploy last succeeded. |
| `action_id` | The service's latest deploy or remove, in the machine's actions. |
| `compose`, `files` and `env` | The compose file, its files by path, and every env key with its value as `"***"`. `compose` is `null` and the others `{}` when unmanaged. |
| `routes` | Each route with its `url`. See [Routes](exposure.md#routes). |
| `alerts_enabled` | One switch for every container of the service. |
| `containers` | Each container by name, with the fields in [What the daemon reports](services.md#what-the-daemon-reports). |

The routes that queue work for the machine answer `202 Accepted` with `{"service": ..., "action": ...}` and the action's `Location`. Follow the action with [`GET /v1/machines/{machine}/actions/{id}`](#get-v1machinesmachineactionsid).

### `GET /v1/services`

Lists every machine's services, by machine, with whether its Docker answers.

`docker` is `running` or `unreachable`, from the latest report, or `null` before the first and once Docker is turned off on the machine. `docker_reason` says why Docker is unreachable, and is `null` while it runs. [Services](services.md#when-docker-cant-be-reached) lists the reasons and what an unreachable Docker does to each container, and [When Docker is turned off](services.md#when-docker-is-turned-off) explains `null` after a report of `off`.

#### Parameters

None.

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/services
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

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/services
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
          "access": false,
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
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or it was removed while the hub read its services. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_services` | `tender services list apps` |

### `POST /v1/machines/{machine}/services`

Stores a new managed service and queues its deploy. The daemon checks the compose file against [the rules](services.md#the-rules), and the deploy fails with the rule's sentence when one fails. The body may be up to 4 MiB.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `name` | string | Yes | Body. The compose project name: lowercase letters, digits and dashes, starting with a letter and not ending with a dash, at most 32. |
| `compose` | string | Yes | Body. The text of the docker compose file, at most 256 KiB. |
| `files` | object | No | Body. Config files the compose file mounts, by path relative to it, such as `config/app.toml`, each with its text. At most 64 files and 1 MiB in all. |
| `env` | object | No | Body. Values for the compose file's `${KEY}` interpolation. Keys are capital letters, digits and underscores, not starting with `COMPOSE_` or `DOCKER_`; a value can't hold a single quote or a line break. |
| `routes` | array | No | Body. Each with `container`, `port`, `expose` (`tailscale`, `cloudflare` or `public`), and `host` for `cloudflare` and `public` or an optional `https_port` for `tailscale`. A `cloudflare` route can also have `access`, `true` to require Cloudflare Access; it defaults to `false`. See [Routes](exposure.md#routes). |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/services \
  -H 'Content-Type: application/json' \
  -d '{"name": "blog", "compose": "services:\n  web:\n    image: ghcr.io/me/blog:2\n    mem_limit: 512m\n",
       "env": {"SECRET_KEY": "s3cret"}, "routes": [{"container": "web", "port": 8080, "expose": "tailscale"}]}'
```

#### Example response

```http
HTTP/1.1 202 Accepted
Content-Type: application/json
Location: /v1/machines/apps/actions/13

{
  "service": {
    "id": 3,
    "machine": "apps",
    "name": "blog",
    "managed": true,
    "state": "stopped",
    "deploy_status": "deploying",
    "last_error": null,
    "deployed_at": null,
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
        "access": false,
        "local_port": 20001,
        "url": "https://apps.tail1234.ts.net:10000",
        "created_at": "2026-11-15T11:40:20Z",
        "updated_at": "2026-11-15T11:40:20Z"
      }
    ],
    "alerts_enabled": true,
    "containers": [],
    "created_at": "2026-11-15T11:40:20Z",
    "updated_at": "2026-11-15T11:40:20Z"
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
    "status": "pending",
    "output_cleared": false,
    "created_at": "2026-11-15T11:40:20Z",
    "updated_at": "2026-11-15T11:40:20Z",
    "output": null
  }
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | The machine doesn't exist. |
| `409` | The machine has a service with this name: a managed one, which `PATCH` changes, or one Tender didn't deploy, whose containers you take down first with `docker compose -p NAME down`. Or a route's host or `https_port` is taken. |
| `413` | The body is larger than 4 MiB. |
| `422` | A field isn't valid, such as a name with a dash, a file outside the folder, an env value of `"***"`, a `public` route without a host, or `access` on a route that isn't `cloudflare`. A `cloudflare` route on a hub without the Cloudflare settings. |
| `503` | The hub has no `TENDER_ENCRYPTION_KEY` to store the env with. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `deploy_service` | `tender services deploy apps blog -f compose.yaml --env-file .env --route web:8080:tailscale` |

### `GET /v1/machines/{machine}/services/{service}`

Returns one service, with its routes and containers.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `service` | string | Yes | Path. The service's name. |

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/services/redis
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or it has no service with this name. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_service` | `tender services show apps redis` |

### `PATCH /v1/machines/{machine}/services/{service}`

Changes a service, as a JSON merge patch. `alerts_enabled` turns its alerts off or on, for any service, and answers `200` with the service; turning them off resolves its open problems. A change of `compose`, `files`, `env` or `routes`, for a managed service only, is stored and queues a deploy, and answers `202`.

`files` and `env` merge into the stored ones: a key with `null` removes it, and `null` for the whole object removes every key. An env value of `"***"` keeps the stored one. `routes` replaces every route; a route the service keeps keeps its ports. A change of a route's `access` alone is a route change too: the hub syncs the machine's tunnel again.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `service` | string | Yes | Path. The service's name. |
| `alerts_enabled` | boolean | No | Body. `false` to stop the service's alerts, `true` to start them again. |
| `compose` | string | No | Body. A new compose file. It can't be `null`. |
| `files` | object | No | Body. Files to add, change or, with `null`, remove. |
| `env` | object | No | Body. Env values to add, change or, with `null`, remove. |
| `routes` | array | No | Body. Every route of the service, replacing the stored ones. |

#### Example request

```sh
curl -s -X PATCH -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/services/blog \
  -H 'Content-Type: application/merge-patch+json' \
  -d '{"env": {"MODE": "prod"}}'
```

#### Example response

```http
HTTP/1.1 202 Accepted
Content-Type: application/json
Location: /v1/machines/apps/actions/14

{
  "service": {
    "id": 3,
    "machine": "apps",
    "name": "blog",
    "managed": true,
    "state": "running",
    "deploy_status": "deploying",
    "last_error": null,
    "deployed_at": "2026-11-15T11:41:02Z",
    "action_id": 14,
    "compose": "services:\n  web:\n    image: ghcr.io/me/blog:2\n    mem_limit: 512m\n",
    "files": {},
    "env": {
      "MODE": "***",
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
        "access": false,
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
        "updated_at": "2026-11-15T11:50:00Z"
      }
    ],
    "created_at": "2026-11-15T11:40:20Z",
    "updated_at": "2026-11-15T11:45:10Z"
  },
  "action": {
    "id": 14,
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
    "status": "pending",
    "output_cleared": false,
    "created_at": "2026-11-15T11:45:10Z",
    "updated_at": "2026-11-15T11:45:10Z",
    "output": null
  }
}
```

With only `alerts_enabled`, the answer is `200 OK` with the service, as `GET` returns it.

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | The machine doesn't exist, or it has no service with this name. |
| `409` | A change of a service Tender didn't deploy, which only `alerts_enabled` can change. Or the service is being removed, or a route's host or `https_port` is taken. |
| `413` | The body is larger than 4 MiB. |
| `422` | A field isn't valid, as for `POST`, or `compose` is `null`. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `set_service_alerts`, or `deploy_service` for a change | `tender services alerts apps blog off`, or `tender services deploy apps blog -f compose.yaml --env MODE=prod` |

### `DELETE /v1/machines/{machine}/services/{service}`

Queues the remove of a managed service. The daemon runs `docker compose down`, takes the service's routes out of the exposures, and keeps its folder in `/var/lib/tender/services/.removed/` on the machine, or deletes it with `delete_data=true`. The hub forgets the service once the remove succeeded. See [Change, redeploy and remove](services.md#change-redeploy-and-remove).

> [!WARNING]
> `delete_data=true` deletes the data of the service's named volumes, and can't be undone.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `service` | string | Yes | Path. The service's name. |
| `delete_data` | boolean | No | Query. `true` deletes the service's folder and its data. The default, `false`, keeps it aside. |

#### Example request

```sh
curl -s -X DELETE -H "Authorization: Bearer $TENDER_TOKEN" 'https://apps.your-tailnet.ts.net/v1/machines/apps/services/blog?delete_data=true'
```

#### Example response

```http
HTTP/1.1 202 Accepted
Content-Type: application/json
Location: /v1/machines/apps/actions/15

{
  "service": {
    "id": 3,
    "machine": "apps",
    "name": "blog",
    "managed": true,
    "state": "running",
    "deploy_status": "removing",
    "last_error": null,
    "deployed_at": "2026-11-15T11:41:02Z",
    "action_id": 15,
    "compose": "services:\n  web:\n    image: ghcr.io/me/blog:2\n    mem_limit: 512m\n",
    "files": {},
    "env": {
      "MODE": "***",
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
        "access": false,
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
        "updated_at": "2026-11-15T11:50:00Z"
      }
    ],
    "created_at": "2026-11-15T11:40:20Z",
    "updated_at": "2026-11-15T11:50:00Z"
  },
  "action": {
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
    "delete_data": true,
    "status": "pending",
    "output_cleared": false,
    "created_at": "2026-11-15T11:50:00Z",
    "updated_at": "2026-11-15T11:50:00Z",
    "output": null
  }
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | `delete_data` isn't `true` or `false`. |
| `404` | The machine doesn't exist, or it has no service with this name. |
| `409` | The service isn't managed, or it is being removed already. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `remove_service` | `tender services remove apps blog --delete-data --yes` |

### `POST /v1/machines/{machine}/services/{service}/deploy`

Queues a deploy of a managed service as it is stored. Compose pulls the images again, so a new image under the same tag runs, and recreates the containers that changed.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `service` | string | Yes | Path. The service's name. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/services/blog/deploy
```

#### Example response

```http
HTTP/1.1 202 Accepted
Content-Type: application/json
Location: /v1/machines/apps/actions/16

{
  "service": {
    "id": 3,
    "machine": "apps",
    "name": "blog",
    "managed": true,
    "state": "running",
    "deploy_status": "deploying",
    "last_error": null,
    "deployed_at": "2026-11-15T11:41:02Z",
    "action_id": 16,
    "compose": "services:\n  web:\n    image: ghcr.io/me/blog:2\n    mem_limit: 512m\n",
    "files": {},
    "env": {
      "MODE": "***",
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
        "access": false,
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
        "updated_at": "2026-11-15T11:50:00Z"
      }
    ],
    "created_at": "2026-11-15T11:40:20Z",
    "updated_at": "2026-11-15T11:48:00Z"
  },
  "action": {
    "id": 16,
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
    "status": "pending",
    "output_cleared": false,
    "created_at": "2026-11-15T11:48:00Z",
    "updated_at": "2026-11-15T11:48:00Z",
    "output": null
  }
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The machine doesn't exist, or it has no service with this name. |
| `409` | The service isn't managed, or it is being removed. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `redeploy_service` | `tender services redeploy apps blog` |

### `POST /v1/machines/{machine}/services/{service}/start`

Queues a start of every container of the service, or of one. A managed service as a whole starts with `docker compose start` in its folder; one container, or an unmanaged service, through Docker's API. Starting a running container changes nothing.

`POST /v1/machines/{machine}/services/{service}/stop` and `POST /v1/machines/{machine}/services/{service}/restart` work the same, and so does `POST /v1/machines/{machine}/actions` with the type and the service. A container Tender stopped is marked `stopped_by_tender` and doesn't alert as down until it runs again.

> [!WARNING]
> A `stop` takes the service offline until something starts it again.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `service` | string | Yes | Path. The service's name. |
| `container` | string | No | Body. Only this container of the service, such as `web`. Leave the body out for every container. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/services/blog/restart \
  -H 'Content-Type: application/json' \
  -d '{"container": "web"}'
```

#### Example response

```http
HTTP/1.1 202 Accepted
Content-Type: application/json
Location: /v1/machines/apps/actions/17

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
    "action_id": 16,
    "compose": "services:\n  web:\n    image: ghcr.io/me/blog:2\n    mem_limit: 512m\n",
    "files": {},
    "env": {
      "MODE": "***",
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
        "access": false,
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
        "updated_at": "2026-11-15T11:50:00Z"
      }
    ],
    "created_at": "2026-11-15T11:40:20Z",
    "updated_at": "2026-11-15T11:49:10Z"
  },
  "action": {
    "id": 17,
    "machine": "apps",
    "type": "restart",
    "service": "blog",
    "container": "web",
    "unit": null,
    "priority": null,
    "since": null,
    "lines": null,
    "destination": null,
    "version": null,
    "packages": null,
    "delete_data": null,
    "status": "pending",
    "output_cleared": false,
    "created_at": "2026-11-15T11:55:00Z",
    "updated_at": "2026-11-15T11:55:00Z",
    "output": null
  }
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | The machine doesn't exist, it has no service with this name, or the service has no container with this name. |
| `409` | The container was removed, or the service has no containers left: Docker on the machine no longer has them. |
| `422` | `container` is empty. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `start_service`, `stop_service` or `restart_service` | `tender services restart apps blog --container web` |

## Registries

A registry is the login of a container registry, such as `ghcr.io`, that managed services pull private images from. A deploy uses the login of the registry whose host an image is on. The hub stores the password encrypted and shows it as `"***"` with `password_set`.

### `GET /v1/registries`

Lists the registries, by name.

#### Parameters

None.

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/registries
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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

| Status | When |
| --- | --- |
| `500` | The hub failed to read its database. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_registries` | `tender registries list` |

### `POST /v1/registries`

Adds a registry's login. It answers `201 Created` with the registry and its `Location`.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | Body. Lowercase letters, digits and dashes, starting with a letter or digit, at most 63. |
| `host` | string | Yes | Body. The registry's host, as image names start with it, such as `ghcr.io` or `registry.example.com:5000`; `docker.io` for Docker Hub. |
| `username` | string | Yes | Body. The login name. |
| `password` | string | Yes | Body. The password or access token. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/registries \
  -H 'Content-Type: application/json' \
  -d '{"name": "ghcr", "host": "ghcr.io", "username": "me", "password": "ghp_example"}'
```

#### Example response

```http
HTTP/1.1 201 Created
Content-Type: application/json
Location: /v1/registries/ghcr

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

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `409` | A registry has this name, or this host already has a login. |
| `422` | A field isn't valid, or the password is `"***"`. |
| `503` | The hub has no `TENDER_ENCRYPTION_KEY` to store the password with. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `add_registry` | `tender registries add ghcr --host ghcr.io --username me` |

### `PATCH /v1/registries/{registry}`

Changes a registry's host, username or password. The fields left out stay.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `registry` | string | Yes | Path. The registry's name. |
| `host` | string | No | Body. A new host. |
| `username` | string | No | Body. A new login name. |
| `password` | string | No | Body. A new password or access token. |

#### Example request

```sh
curl -s -X PATCH -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/registries/ghcr \
  -H 'Content-Type: application/merge-patch+json' \
  -d '{"password": "ghp_new"}'
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "id": 1,
  "name": "ghcr",
  "host": "ghcr.io",
  "username": "me",
  "password": "***",
  "password_set": true,
  "created_at": "2026-11-15T09:00:00Z",
  "updated_at": "2026-11-15T12:00:00Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `404` | No registry has this name. |
| `409` | Another registry has the new host. |
| `422` | A field isn't valid. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `update_registry` | `tender registries update ghcr --password` |

### `DELETE /v1/registries/{registry}`

Removes a registry's login. Services whose images are on it can no longer pull them, unless they are public. It answers `204 No Content`.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `registry` | string | Yes | Path. The registry's name. |

#### Example request

```sh
curl -s -X DELETE -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/registries/ghcr
```

#### Example response

```http
HTTP/1.1 204 No Content
```

#### Errors

| Status | When |
| --- | --- |
| `404` | No registry has this name. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `remove_registry` | `tender registries remove ghcr` |

## Actions

An action is work for a machine's daemon: deploying or removing a managed service, which only [its routes](#services) queue, starting, stopping or restarting a service or one of its containers, reading a service's logs or the machine's journal, trying a backup destination (`test`, queued with [`POST /v1/backups/destinations/{destination}/test`](#post-v1backupsdestinationsdestinationtest)), upgrading the machine's packages, or updating Tender, which only a rollout queues. The hub keeps each machine's newest 100 finished actions, and every `pending` or `in_progress` one.

### `POST /v1/machines/{machine}/actions`

Queues an action for the machine's daemon and answers at once with the action as `pending`.

To restart one container of a service, send `{"type": "restart", "service": "notes", "container": "web"}`; leave `container` out for every container. A service's logs are its containers' lines merged in time order, each with its container's name, or one container's lines with `container`.

To read the machine's journal, send `{"type": "logs", "unit": "tenderd", "priority": "warning", "since": "1h", "lines": 200}`. Logs output is at most 256 KiB, dropping the oldest lines past it.

To upgrade a Debian or Ubuntu machine's packages, send `{"type": "upgrade", "packages": "security"}`, or `"all"` for every package. The daemon runs it in the `tenderupgrade` unit, and the hub gives it 45 minutes instead of 15. Its output is the end of what the upgrade printed. Tender never reboots the machine.

> [!WARNING]
> A `stop` takes the service offline until something starts it again. Logs can hold secrets. The hub returns them only on `GET .../actions/{id}`, clears them 1 hour after the action completes, and never puts them in alerts or emails. See [Security](security.md#logs-can-hold-secrets).

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `type` | string | Yes | Body. `start`, `stop`, `restart`, `logs` or `upgrade`. An `update` comes only from `POST /v1/updates/rollouts`. |
| `service` | string | For `start`, `stop` and `restart` | Body. The service's name. With `logs`, read this service's logs; leave it out to read the machine's journal. |
| `container` | string | No | Body. With `service`, only this container of the service, such as `web`. |
| `unit` | string | No | Body. Machine logs only. Only this systemd unit, such as `tenderd` or `nginx.service`. |
| `priority` | string | No | Body. Machine logs only. Only this priority and the more severe ones: `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info` or `debug`. |
| `since` | string | No | Body. Logs only. Only entries from this far back, such as `30m`, `1h` or `7d`, at most `7d`. |
| `lines` | integer | No | Body. Logs only. How many of the newest lines, from 1 to 2000. The default is 200. |
| `packages` | string | No | Body. Upgrade only. `security`, the default, or `all`. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/actions \
  -H 'Content-Type: application/json' \
  -d '{"type": "restart", "service": "notes", "container": "web"}'
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
| `404` | The machine doesn't exist, it has no service with this name, or the service has no container with this name. |
| `409` | The service or the container was removed: Docker on the machine no longer has it. |
| `422` | The type isn't one of the five, a service action has no service or has a logs field, `container` comes without `service` or is empty, a service's logs action has `unit` or `priority`, a logs field isn't valid, `packages` isn't `security` or `all` or comes without `upgrade`, or the type is `update`, `deploy` or `remove`, which go through their own routes. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `start_service`, `stop_service`, `restart_service`, `service_logs`, `get_machine_logs` or `upgrade_machine` | `tender services start\|stop\|restart\|logs apps notes --container web`, `tender logs apps`, or `tender machines upgrade apps` |

### `GET /v1/machines/{machine}/actions`

Lists a machine's actions, newest first, without their output.

`output_cleared` is `true` once the hub has cleared a completed logs action's output.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/actions
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
      "service": "notes",
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
      "output_cleared": true,
      "created_at": "2026-11-15T11:28:14Z",
      "updated_at": "2026-11-15T11:28:35Z"
    },
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
      "updated_at": "2026-11-15T11:28:34Z"
    },
    {
      "id": 1,
      "machine": "apps",
      "type": "restart",
      "service": "notes",
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

`output` is `null` while the action runs, for a completed start, stop or restart, and once the hub has cleared a completed logs action's output. A failed action keeps why it failed. A machine logs line is the time in UTC, the unit and the message. A service logs line is the time and the message, with the container's name and `|` between them when the logs are of more than one container. A deploy's output is the end of what compose printed, and a remove's says where the service's folder was kept.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `machine` | string | Yes | Path. The machine's name. |
| `id` | integer | Yes | Path. The action's id. |

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/machines/apps/actions/8
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/updates
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/updates/settings
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
curl -s -X PUT -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/updates/settings \
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/updates/rollouts
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

Starts a rollout of a stable release to the machines named, or to every machine on Tender v0.3.0 or later, and answers `202 Accepted` with the rollout. The hub's own machine goes first, because a newer hub takes the reports of older daemons. Follow it with `GET /v1/updates/rollouts/{id}`.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | string | No | Body. The release, such as `0.3.1`. Leave it out for the latest. |
| `machines` | array of strings | No | Body. The machines to update, by name. Leave it out, or send `[]`, for every machine. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/updates/rollouts \
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
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/updates/rollouts/4
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
     "error": "Tender v0.3.2 failed its health check, so it rolled back to Tender 0.3.1: after 2m0s, tenderd is not running.",
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

## Log sinks

A log sink is a place each machine's daemon sends its logs to: the systemd journal and its Docker containers' logs, straight from the machine. The logs never pass through the hub, which keeps only the sinks and each daemon's counters. [Log shipping](log-shipping.md) explains the batches, the buffer and the webhook contract.

A sink's JSON has `id`, `name`, `type`, `config`, `enabled`, `sources`, `machines`, `status`, `created_at` and `updated_at`. In `config`, each secret shows as `"***"`, with `<field>_set` saying whether it is set. The query string of `url` or `endpoint`, which can carry a key, is kept as a secret too and shows as `?***`, such as `https://logs.example.com/in?***`. Send that URL back in a `PATCH` and the query stays as it is.

| Type | `config` fields | Secrets |
| --- | --- | --- |
| `otlp` | `endpoint`, the collector's OTLP/HTTP logs URL, such as `https://otel.example.com:4318/v1/logs` | `headers`, an object of HTTP headers, such as `{"Authorization": "Bearer <key>"}` |
| `webhook` | `url` | `secret`, at least 16 characters. Leave it out and the hub makes one. |

`sources` says which logs go, as `{"journal": {"units": [], "min_priority": "info"}, "services": {"names": []}}`. An empty `units` or `names` list means all of them, and a source that is `null` sends nothing. `min_priority` is the least severe journal priority sent: `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info` or `debug`. `machines` lists the machines that send to the sink, and an empty list means every machine.

`status` has one entry per machine whose daemon reported on the sink. Daemons report every minute, and within seconds when sending starts or stops failing. Each entry has these fields:

| Field | What it is |
| --- | --- |
| `machine` | The machine's name. |
| `records_sent` | The records the sink took, since the daemon first sent to it. |
| `records_dropped` | The records the daemon dropped, because its buffer for the sink was full or the sink refused them for good. |
| `last_sent_at` | When a send last worked, or `null`. |
| `last_error` | Why the last send failed, such as `the sink answered 503 Service Unavailable`, or `null` once a send works. |
| `failing_since` | When sending started to fail, or `null` while it works. |
| `last_dropped_at` | When the daemon last dropped records, or `null`. |
| `created_at`, `updated_at` | When the machine first and last reported on the sink. |

### `GET /v1/logs/sinks`

Lists every sink, by name, with each machine's status. Secrets show as `"***"`.

#### Parameters

None.

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/logs/sinks
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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

| Status | When |
| --- | --- |
| `500` | The hub failed to read its database. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_log_sinks` | `tender sinks list` |

### `POST /v1/logs/sinks`

Adds a sink. Each daemon it is for starts sending to it within a minute, with the logs from then on.

For a `webhook` sink without a `secret`, the response has `secret`, the one the hub made, shown only this once. Give it to the receiver, which checks each batch's signature with it.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | Body. Lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |
| `type` | string | Yes | Body. `otlp` or `webhook`. |
| `config` | object | Yes | Body. The type's fields, from the table above. The hub stores the secrets encrypted. |
| `enabled` | boolean | No | Body. Whether daemons send to it. The default is `true`. |
| `sources` | object | No | Body. Which logs go. The default is every unit's journal from `info` up, and every container. |
| `machines` | array | No | Body. The machines that send to it, by name. The default, `[]`, is every machine. |

#### Example request

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/logs/sinks \
  -H 'Content-Type: application/json' \
  -d '{"name": "otel", "type": "otlp", "config": {"endpoint": "https://otel.example.com:4318/v1/logs", "headers": {"Authorization": "Bearer <key>"}}}'
```

A webhook sink for one machine's nginx journal and its web container:

```sh
curl -s -X POST -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/logs/sinks \
  -H 'Content-Type: application/json' \
  -d '{"name": "hook", "type": "webhook", "config": {"url": "https://logs.example.com/tender"}, "machines": ["apps"], "sources": {"journal": {"units": ["nginx.service"], "min_priority": "warning"}, "services": {"names": ["web"]}}}'
```

#### Example response

```http
HTTP/1.1 201 Created
Content-Type: application/json
Location: /v1/logs/sinks/hook

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

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `409` | A sink with this name already exists. |
| `422` | The name or the type isn't valid, `config` is missing, a config field is missing, unknown for the type or not valid, a secret is given as `"***"`, `sources` names neither source or has a unit, container or priority that isn't valid, or `machines` names a machine that doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `add_log_sink` | `tender sinks add otel --type otlp --endpoint URL --headers-file PATH` |

### `GET /v1/logs/sinks/{sink}`

Returns one sink, with each machine's status and its secrets shown as `"***"`.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `sink` | string | Yes | Path. The sink's name. |

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/logs/sinks/otel
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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
      "records_dropped": 120,
      "last_sent_at": "2026-11-15T12:04:10Z",
      "last_error": "the sink answered 503 Service Unavailable",
      "failing_since": "2026-11-15T12:04:40Z",
      "last_dropped_at": "2026-11-15T12:20:13Z",
      "created_at": "2026-11-15T11:31:02Z",
      "updated_at": "2026-11-15T12:20:15Z"
    }
  ],
  "created_at": "2026-11-15T11:30:41Z",
  "updated_at": "2026-11-15T11:30:41Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The sink doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `list_log_sinks` | `tender sinks show otel` |

### `PATCH /v1/logs/sinks/{sink}`

Changes a sink's `config`, `enabled`, `sources` or `machines`, as a JSON merge patch. In `config`, `null` removes an optional field and a secret sent as `"***"` stays as it is, so you can send back what a read gave you. In `sources`, `null` turns a source off. The name and the type can't change. Daemons pick the change up within a minute.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `sink` | string | Yes | Path. The sink's name. |
| `config` | object | No | Body. The config fields to change. |
| `enabled` | boolean | No | Body. Whether daemons send to it. |
| `sources` | object | No | Body. The sources fields to change. |
| `machines` | array | No | Body. The machines that send to it, replacing the list. `[]` is every machine. |

#### Example request

```sh
curl -s -X PATCH -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/logs/sinks/otel \
  -H 'Content-Type: application/merge-patch+json' \
  -d '{"sources": {"journal": {"min_priority": "warning"}, "services": null}}'
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

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

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON, or has a field a sink doesn't have. |
| `404` | The sink doesn't exist. |
| `422` | The body has `name` or `type`, or the sink that results isn't valid, as for `POST`. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `update_log_sink` | `tender sinks update otel --min-priority warning --no-services` |

### `DELETE /v1/logs/sinks/{sink}`

Removes a sink and every machine's status of it.

> [!WARNING]
> Daemons stop sending to the sink within a minute. They keep the logs they buffered for it for 7 days, then delete them, and nothing sends them anywhere: a sink added again is a new sink. The logs already sent stay at the sink.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `sink` | string | Yes | Path. The sink's name. |

#### Example request

```sh
curl -s -X DELETE -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/logs/sinks/otel
```

#### Example response

```http
HTTP/1.1 204 No Content
```

#### Errors

| Status | When |
| --- | --- |
| `404` | The sink doesn't exist. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `remove_log_sink` | `tender sinks remove otel` |

## Exposures

The settings services' routes need to reach people. Only `cloudflare` routes need any: the hub's Cloudflare account and API token. [Exposure](exposure.md#cloudflare) explains how the hub uses them.

### `GET /v1/exposures/cloudflare`

Returns the hub's Cloudflare settings. The API token is always `"***"`, with `api_token_set`.

#### Parameters

None.

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/exposures/cloudflare
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "account_id": "0123456789abcdef0123456789abcdef",
  "api_token": "***",
  "api_token_set": true,
  "created_at": "2026-11-15T10:00:00Z",
  "updated_at": "2026-11-15T10:00:00Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `404` | Cloudflare isn't set up on this hub. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `get_cloudflare_settings` | `tender exposures cloudflare show` |

### `PUT /v1/exposures/cloudflare`

Replaces the hub's Cloudflare settings. The hub first lists the account's tunnels and zones with the token, and stores nothing if Cloudflare refuses.

#### Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `account_id` | string | Yes | Body. The Cloudflare account ID, 32 lowercase hex digits, from the dashboard's Account home. |
| `api_token` | string | No | Body. Secret. A Cloudflare API token with Account Cloudflare Tunnel Edit, Zone DNS Edit and Zone Read. A route with `access` also needs Access: Apps and Policies Read and Access: Organizations, Identity Providers, and Groups Read. Required the first time; leave it out to keep the stored one. |

#### Example request

```sh
curl -s -X PUT -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/exposures/cloudflare \
  -H 'Content-Type: application/json' \
  -d "{\"account_id\": \"0123456789abcdef0123456789abcdef\", \"api_token\": \"$(cat cloudflare-token)\"}"
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "account_id": "0123456789abcdef0123456789abcdef",
  "api_token": "***",
  "api_token_set": true,
  "created_at": "2026-11-15T10:00:00Z",
  "updated_at": "2026-11-15T10:00:00Z"
}
```

#### Errors

| Status | When |
| --- | --- |
| `400` | The body isn't valid JSON or has a field the route doesn't know. |
| `422` | `account_id` isn't 32 lowercase hex digits, `api_token` isn't a token or is `"***"`, there is no stored token to keep, or Cloudflare refused the account or the token. The detail gives Cloudflare's reason. |
| `502` | Cloudflare didn't answer, or answered with a `5xx` or `429`, after 3 tries. Try again later. |
| `503` | The hub has no `TENDER_ENCRYPTION_KEY` to seal the token with. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `set_cloudflare_settings` | `tender exposures cloudflare set --account-id ID --api-token-file PATH` |

### `DELETE /v1/exposures/cloudflare`

Removes the hub's Cloudflare settings. The tunnels and DNS records already in Cloudflare stay.

#### Parameters

None.

#### Example request

```sh
curl -s -X DELETE -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/v1/exposures/cloudflare
```

#### Example response

```http
HTTP/1.1 204 No Content
```

#### Errors

| Status | When |
| --- | --- |
| `404` | Cloudflare isn't set up on this hub. |
| `409` | A service still has a `cloudflare` route. Take the routes off first. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| `remove_cloudflare_settings` | `tender exposures cloudflare remove` |

## Metrics

### `GET /metrics`

Serves the machines, services, backups and open problems in the Prometheus text format, version 0.0.4, for a scraper. [Metrics](metrics.md) lists every metric.

#### Parameters

None.

#### Example request

```sh
curl -s -H "Authorization: Bearer $TENDER_TOKEN" https://apps.your-tailnet.ts.net/metrics
```

#### Example response

```http
HTTP/1.1 200 OK
Content-Type: text/plain; version=0.0.4; charset=utf-8

# HELP tender_build_info The hub's version, as the version label; always 1.
# TYPE tender_build_info gauge
tender_build_info{version="v0.3.0"} 1
# HELP tender_machine_cpu_load The machine's 1-minute load average, from its latest reading.
# TYPE tender_machine_cpu_load gauge
tender_machine_cpu_load{machine="apps"} 0.09
# HELP tender_service_state 1 for the service's current state: running, degraded, stopped or unknown. managed is true for a service Tender deployed.
# TYPE tender_service_state gauge
tender_service_state{machine="apps",service="notes",managed="true",state="running"} 1
# HELP tender_container_state 1 for the container's current state: created, running, paused, restarting, exited, removing, dead, unknown or removed.
# TYPE tender_container_state gauge
tender_container_state{machine="apps",service="notes",container="web",runtime="docker",state="running"} 1
# HELP tender_backup_last_age_seconds How old the machine's newest snapshot is.
# TYPE tender_backup_last_age_seconds gauge
tender_backup_last_age_seconds{machine="apps"} 31560
# HELP tender_problems_open How many problems are open, by event and the severity of their latest notification.
# TYPE tender_problems_open gauge
tender_problems_open{event="backup.failed",severity="critical"} 0
```

The example shows a few lines of each kind. The real page has every metric.

#### Errors

| Status | When |
| --- | --- |
| `500` | The hub couldn't read its database. The answer is a problem, never part of a page. |

#### Equivalents

| MCP tool | CLI command |
| --- | --- |
| None. The typed tools, such as `list_machines`, `list_services` and `get_backup_status`, return the same data. | None. Read it with `curl`, or use `tender status`. |

## Daemon routes

The daemons use these routes to report and to take their work. Each needs the machine's own token as `Authorization: Bearer <token>`, and you never call them yourself.

| Route | What the daemon does with it |
| --- | --- |
| `POST /v1/machines/{machine}/usage` | Sends a reading of CPU load, memory, disk and uptime, once a minute, with the Tender `version` it runs. |
| `GET /v1/machines/{machine}/backups/settings` | Gets the machine's settings with `credentials`: the destination's whole `config` with its secrets, and the password. For an `s3` destination it also carries `endpoint`, `bucket` and `path` in `destination`, and `access_key_id` and `secret_access_key` in `credentials`, where daemons before v0.3 read them. People get the same route without any of them. |
| `GET /v1/machines/{machine}/actions?wait=25` (a test) | A test action comes with `target`: the destination's name, type, whole config and password, for this daemon only. |
| `PUT /v1/machines/{machine}/backups/snapshots` | Replaces the hub's list of the machine's snapshots after each backup and cleanup. |
| `POST /v1/machines/{machine}/backups/history` | Records a run as it starts, or a finished run it couldn't send while the hub was down. |
| `PATCH /v1/machines/{machine}/backups/history/{id}` | Records how a run ended. |
| `PUT /v1/machines/{machine}/services` | Sends `{"docker": "running", "containers": [...]}` with every container, `[]` for none, each with its compose `project`, `compose_service` and `container_number`, or `{"docker": "unreachable", "docker_reason": "refused"}` with no list, or `{"docker": "off"}` when Docker is turned off, once a minute and after each start, stop, restart, deploy or remove. A running container whose stats couldn't be read has `"stats_unavailable": true` and no usage. A daemon before v0.4 sends the list as `services`, which the hub still takes. |
| `GET /v1/machines/{machine}/actions?wait=25` | Waits up to 25 seconds, at most 30, for the oldest pending action, and gets it without changing its status. |
| `GET /v1/machines/{machine}/actions?wait=25` (a deploy or remove) | A deploy or remove action comes with `deployment`: the service's `name`, `compose`, `files`, `env` in the clear, its `routes` with their local ports and `expose`, the `registries` logins, the machine's `exposures`, and `cloudflare_error` when the hub couldn't route the service's cloudflare routes through the machine's tunnel, which fails the action once the containers are done, for this daemon only. |
| `GET /v1/machines/{machine}/exposures` | Gets `{"routes": [...], "cloudflare_tunnel_token": ...}`, the machine's whole set of routes, each with `service`, `container`, `expose`, `local_port`, `host` and `https_port`, when the daemon starts. The tunnel token, `null` while the machine has no Cloudflare tunnel, reaches only this daemon. See [Exposure](exposure.md#how-the-daemon-applies-them). |
| `PUT /v1/machines/{machine}/exposures/status` | Sends `{"tailscale_dns_name": "apps.tail1234.ts.net"}`, or `null` without Tailscale, after it applies the exposures. Answers `204`. |
| `GET /v1/machines/{machine}/logs/sinks` | Gets the enabled sinks for the machine, every 30 seconds, each with its whole `config`, secrets included, its `sources` and `updated_at`. |
| `PUT /v1/machines/{machine}/logs/status` | Sends `{"data": [...]}`, one entry per sink with `sink_id` and the `status` fields, every minute and within seconds when sending starts or stops failing. An entry for a sink that is gone is ignored. |
| `PUT /v1/machines/{machine}/security` | Sends the security checks, when the daemon starts and every 15 minutes, and after an upgrade. Answers `204`. |
| `PATCH /v1/machines/{machine}/actions/{id}` | Claims an action with `{"status": "in_progress"}`, which answers `204`, or `409` when it isn't pending. Then records how it ended, with `completed` or `failed` and its `output`, which only those two take. |

## Next steps

1. [MCP reference](mcp.md) has the same operations as tools for an AI client.
2. [CLI reference](cli.md) has the `tender` commands that call these routes.
3. [Security](security.md) explains who can reach the hub and how tokens are kept.
