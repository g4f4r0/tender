# CLI

The `tender` command manages the hub, its machines, backups, services and alerts, and installs Tender on a machine.

## Prerequisites

1. The `tender` binary at `/usr/local/bin/tender`. The installer puts it there on every machine with a hub or a daemon. See [Install](install.md).
2. Network access to the hub, for every command that talks to it.
3. Root, only for `tender install` and `tender update`. The `hub` and `daemon` commands run as their systemd services.

## Global behavior

Commands that talk to the hub find it in this order:

1. The `--hub URL` option, such as `--hub https://apps.your-tailnet.ts.net`.
2. The `TENDER_HUB` environment variable.
3. `http://127.0.0.1:8080`, where the hub listens on its own machine. On the hub machine you need neither.

They need no token, because the hub has no login in this version. See [Security](security.md).

Options and arguments can come in any order, so `tender machines add apps --hub URL` and `tender machines add --hub URL apps` are the same.

A command exits with one of these codes:

| Code | Meaning |
| --- | --- |
| `0` | It worked, or it printed its help. |
| `1` | It failed, or a group got an unknown subcommand such as `tender machines nope`. The reason is on stderr. |
| `2` | You ran `tender` or a group such as `tender machines` with no command, and the usage is on stdout. An unknown command such as `tender nope` also exits 2, with the reason on stderr. |

A failure prints one line on stderr that starts with `tender:` and says what to do. A usage mistake also names the help command:

```text
tender: --lines must be from 1 to 2000. Run "tender logs --help" for usage.
```

When the hub refuses a request, the line is the hub's own sentence:

```text
tender: Service "nope" doesn't exist on this machine.
```

When the hub doesn't answer, the line says what to check:

```text
tender: cannot reach the hub at https://apps.your-tailnet.ts.net. Check that the hub is running (systemctl status tenderhub) and that --hub or TENDER_HUB has its address.
```

Add `--help` to any command for its usage, or run `tender help` for the list of commands.

## `tender status`

### `tender status`

Shows every machine, when it last reported, its last backup, how many of its services run, and the open alerts.

#### Usage

```sh
tender status [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. The default is `$TENDER_HUB`, else `http://127.0.0.1:8080`. |

#### Example

```sh
tender status
```

```text
MACHINE   LAST SEEN      LAST BACKUP   STATUS      SERVICES
apps      1 minute ago   just now      completed   1/2 running

Open alerts: 1
SEVERITY   SINCE      ALERT
warning    just now   The service web on apps uses 950 MiB, 92% of its 1 GiB memory limit.
```

`SERVICES` says `Docker unreachable` while the machine's Docker doesn't answer, and doesn't count removed services. Against an older hub without `GET /v1/services`, the table leaves out the `SERVICES` column. An alert longer than 100 characters, or longer than one line, ends in `...`; its email has the whole text. With no open alerts, the list ends with `No open alerts.`

#### Errors

1. `cannot reach the hub at URL. ...` when the hub doesn't answer.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/machines`, `GET /v1/backups`, `GET /v1/services` and `GET /v1/notifications` | `list_machines`, `get_backup_status`, `list_services` and `list_notifications` |

## `tender machines`

Add the machines that back up, manage their tokens and settings, and upgrade their packages.

### `tender machines list`

Lists the machines, when each last reported, its memory and disk use from the latest report, and the Tender version its daemon runs.

#### Usage

```sh
tender machines list [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender machines list
```

```text
NAME   LAST SEEN   MEMORY   DISK   VERSION
apps   just now    15%      28%    0.3.1
```

A machine whose daemon never reported shows `never`, with `-` for memory and disk. A daemon before v0.3.0 reports no version, shown as `-`. With no machines, it prints `No machines yet. Add one with: tender machines add NAME` and exits 0.

#### Errors

1. `cannot reach the hub at URL. ...` when the hub doesn't answer.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/machines` | `list_machines` |

### `tender machines add`

Adds a machine and prints its token once.

#### Usage

```sh
tender machines add NAME [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The machine's name: lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender machines add apps
```

```text
Added machine apps.

Its hub token, shown only now. Save it before you go on:
  <token>

Install the daemon on that machine; it asks for the token:
  curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh \
    | sudo sh -s -- daemon --hub HUB_URL --machine apps
```

The install command shows `HUB_URL` when you reached the hub at `127.0.0.1` or `localhost`, which the new machine can't use. Put the hub's tailnet address there.

#### Errors

1. `Machine names are lowercase letters, digits and hyphens, up to 63 characters.`
2. `Machine "apps" already exists. Choose another name, or rotate its token to get a new one.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines` | `add_machine` |

### `tender machines remove`

Removes a machine and its history from the hub.

> [!WARNING]
> There is no undo. The machine's token stops working, and its settings, backup history, services, actions and alerts are deleted from the hub. Its backups stay in the destination.

#### Usage

```sh
tender machines remove NAME --yes [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The machine to remove. |
| `--yes` | flag | Yes | Confirms the removal. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender machines remove apps --yes
```

```text
Removed machine apps and its history. Its backups stay in the destination.
```

#### Errors

1. `this removes apps and its history from the hub, with no undo. Run "tender machines remove apps --yes" to confirm` without `--yes`.
2. `Machine "apps" doesn't exist.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `DELETE /v1/machines/{machine}` | `remove_machine` |

### `tender machines rotate-token`

Replaces a machine's token and prints the new one once.

> [!WARNING]
> The old token stops working at once, so the machine's daemon stays offline and stops backing up until you install the new token on it.

#### Usage

```sh
tender machines rotate-token NAME [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The machine whose token to replace. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender machines rotate-token apps
```

```text
Replaced the token of apps. The old one no longer works.

The new token, shown only now. Save it before you go on:
  <token>

Give it to the daemon on that machine: sudo tender install daemon --token-file PATH
```

#### Errors

1. `Machine "apps" doesn't exist.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/token` | `rotate_machine_token` |

### `tender machines set`

Changes a machine's settings: its own update mode, its security allowlist and its security alerts.

#### Usage

```sh
tender machines set NAME [--updates inherit|auto|manual] [--allow-port PORT ...]
                         [--deny-port PORT ...] [--security-alerts on|off] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The machine's name. |
| `--updates` | string | No | `auto` or `manual` for the machine's own update mode, or `inherit` for the hub's. |
| `--allow-port` | string | No | A port with its protocol, such as `41641/udp`, that may listen on a public address without a `security.port_exposed` alert. Repeat it for more. |
| `--deny-port` | string | No | A port to take off the allowlist. Repeat it for more. |
| `--security-alerts` | string | No | `on` or `off` for the machine's security alerts. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender machines set apps --allow-port 41641/udp
```

```text
apps: updates inherit, allowed ports 41641/udp, security alerts on.
```

#### Errors

1. `machines set needs --updates, --allow-port, --deny-port or --security-alerts. Run "tender machines set --help" for usage.`
2. `"41641" is not a port. Write each port with its protocol, such as 41641/udp or 443/tcp.`
3. `updates must be inherit, auto or manual.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `PATCH /v1/machines/{machine}` | `update_machine` |

### `tender machines upgrade`

Upgrades a Debian or Ubuntu machine's system packages through its daemon: after `apt-get update`, the security updates, with `unattended-upgrade`, or every package with `--all-packages`, with `apt-get upgrade --with-new-pkgs`, which installs new packages such as a kernel but never removes one, keeping the config files you changed. It runs in the `tenderupgrade` unit, waits up to 45 minutes and prints the end of the upgrade's output. Tender never reboots the machine.

#### Usage

```sh
tender machines upgrade NAME [--all-packages] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The machine's name. |
| `--all-packages` | flag | No | Upgrade every package, not only the security updates. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender machines upgrade apps
```

```text
Upgrading the security packages on apps. This can take a few minutes.
Packages that will be upgraded: libssl3 openssl
All upgrades installed
Upgraded the security packages on apps.
```

#### Errors

1. `Could not upgrade the security packages: ...`, or `Could not upgrade every package: ...` with `--all-packages`, with the upgrade's last line.
2. `this daemon doesn't know the action "upgrade"; update Tender on this machine`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/actions` | `upgrade_machine` |

## `tender destinations`

Manage the S3 buckets backups go to, with the keys and the repository password the hub keeps for them. To change a destination's endpoint, bucket, path or keys, use the API or the MCP tool `update_destination`.

### `tender destinations list`

Lists the destinations, without their keys or passwords.

#### Usage

```sh
tender destinations list [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender destinations list
```

```text
NAME   TYPE   ENDPOINT                                        BUCKET       PATH
r2     s3     https://<account-id>.r2.cloudflarestorage.com   my-backups   tender
```

With no destinations, it prints `No destinations yet. Add one with: tender destinations add NAME --endpoint URL --bucket BUCKET` and exits 0.

#### Errors

1. `cannot reach the hub at URL. ...` when the hub doesn't answer.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/backups/destinations` | `list_destinations` |

### `tender destinations add`

Adds a destination. It asks for the access key ID, the secret access key and the repository password with typing hidden, or reads each from a file.

#### Usage

```sh
tender destinations add NAME --endpoint URL --bucket BUCKET [--path PATH] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The destination's name: lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |
| `--endpoint` | URL | Yes | The S3 endpoint, such as `https://<account-id>.r2.cloudflarestorage.com`. |
| `--bucket` | string | Yes | The bucket name. |
| `--path` | string | No | A folder inside the bucket. The default is the bucket's root. Each machine gets its own folder under it. |
| `--type` | string | No | The kind of destination. Only `s3`, the default. |
| `--access-key-id-file` | path | No | A file with the access key ID, instead of asking. |
| `--secret-access-key-file` | path | No | A file with the secret access key, instead of asking. |
| `--password-file` | path | No | A file with the repository password, instead of asking. |
| `--hub` | URL | No | The hub's address. |

Leave the password empty when it asks, and the hub makes a strong one and shows it once. Without a terminal, the keys must come from files, and the hub makes the password unless `--password-file` gives one.

#### Example

```sh
tender destinations add r2 --endpoint https://<account-id>.r2.cloudflarestorage.com --bucket my-backups --path tender
```

```text
Added destination r2.

The repository password, made by the hub and shown only now:
  <password>

Save it in a password manager now. Every machine's backups at this destination are encrypted with
it, and without it they cannot be restored. Tender cannot show it again.
```

Save the password in a password manager. It can't change later, and without it nobody can restore the backups at this destination.

#### Errors

1. `destinations add needs --endpoint and --bucket. Run "tender destinations add --help" for usage.`
2. `there is no terminal to ask for the access key ID. Save it in a file and add --access-key-id-file PATH.`
3. `A destination with this name or this endpoint, bucket and path already exists. Choose another name, or use the existing one.`
4. `endpoint must be a URL such as https://<account>.r2.cloudflarestorage.com.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/backups/destinations` | `add_destination` |

### `tender destinations remove`

Removes a destination that no machine backs up to.

> [!WARNING]
> The hub forgets the destination's keys and password. The backups already in the bucket stay there, so keep the password in your password manager to restore them.

#### Usage

```sh
tender destinations remove NAME [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The destination to remove. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender destinations remove r2
```

```text
Removed destination r2. The backups already in its bucket stay there.
```

#### Errors

1. `Destination "r2" is used by apps. Stop their backups first (delete their backup settings), then remove the destination.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `DELETE /v1/backups/destinations/{destination}` | `remove_destination` |

## `tender backups`

See how each machine's backups are doing, and set when and where each machine backs up.

### `tender backups status`

Shows every machine's backups, or one machine's: the destination, the schedule in UTC, the retention, the last backup and its status, and the snapshot count.

#### Usage

```sh
tender backups status [MACHINE] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | No | Only this machine. The default is every machine. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender backups status apps
```

```text
MACHINE   DESTINATION   SCHEDULE     KEEPS     LAST BACKUP   STATUS      SNAPSHOTS
apps      r2            30 3 * * *   30 days   just now      completed   1
```

A machine without settings shows `not set`, followed by the command that sets them.

#### Errors

1. `Machine "apps" doesn't exist.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/backups` and `GET /v1/machines/{machine}/backups` | `get_backup_status` |

### `tender backups set`

Chooses where, when and for how long a machine backs up. It replaces any earlier settings.

#### Usage

```sh
tender backups set MACHINE --destination NAME --schedule CRON --retention-days N [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine to back up. |
| `--destination` | string | Yes | The destination to back up to. |
| `--schedule` | cron | Yes | When to back up: five cron fields in UTC, such as `"30 3 * * *"`. |
| `--retention-days` | integer | Yes | How many days to keep snapshots, 1 or more. |
| `--hub` | URL | No | The hub's address. |

A daemon without settings picks them up within 30 seconds. A daemon that has settings checks for changes every 10 minutes.

#### Example

```sh
tender backups set apps --destination r2 --schedule "30 3 * * *" --retention-days 30
```

```text
apps backs up to r2 at "30 3 * * *" (UTC) and keeps 30 days.
```

#### Errors

1. `backups set needs --destination, --schedule and --retention-days (1 or more). Run "tender backups set --help" for usage.`
2. `schedule must be a standard 5-field cron expression: ...`
3. `Destination "r2" doesn't exist.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `PUT /v1/machines/{machine}/backups/settings` | `set_backup` |

### `tender backups unset`

Stops backing up a machine. Its daemon deletes its saved settings and password. Its backups stay in the destination.

#### Usage

```sh
tender backups unset MACHINE [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine to stop backing up. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender backups unset apps
```

```text
apps no longer backs up. Its backups stay in the destination.
```

#### Errors

1. `This machine has no backup settings. There is nothing to remove.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `DELETE /v1/machines/{machine}/backups/settings` | `unset_backup` |

## `tender services`

See and control each machine's Docker services, one per container, by its container name. Start, stop, restart and logs go to the machine's daemon, and the command waits up to 60 seconds for it. If the daemon hasn't finished by then, the command says what happens next from the action's status, and you check it with `tender actions show`. A `pending` action runs if the daemon picks it up within 15 minutes of being asked, and fails otherwise. An `in_progress` one fails if the daemon doesn't say how it ended within 15 minutes.

### `tender services list`

Lists every machine's Docker services, or one machine's, from the latest report.

#### Usage

```sh
tender services list [MACHINE] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | No | Only this machine. The default is every machine. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services list apps
```

```text
MACHINE   SERVICE   STATE                 HEALTH    MEM               CPU     RESTARTS   STARTED       ALERTS
apps      migrate   exited (0)            -         -                 -       0          -             on
apps      old-web   removed 2 hours ago   -         -                 -       0          3 hours ago   on
apps      web       running               healthy   200 MiB / 1 GiB   12.5%   0          1 hour ago    on
```

The columns are:

1. `STATE` is Docker's state, with the exit code once stopped, and `by Tender` when a Tender stop left it stopped. While Docker is unreachable it is `unknown` alone. A service whose container is gone shows `removed` and when, until the hub deletes it 7 days later, as [Removed services](services.md#removed-services) explains.
2. `MEM` is the memory it uses, with its limit when it has one.
3. `CPU` is its use since the previous report, where 100% is one whole CPU. It shows `-` while there is no previous reading, as [Services](services.md#what-a-service-is) explains.
4. `ALERTS` says whether the service sends alert emails.

While Docker is unreachable, the list ends with `Docker is unreachable on apps, so the states of its services are unknown.` With no services, it prints `apps has no services. Its daemon reports the Docker containers once a minute; a machine without Docker has none.` and exits 0.

#### Errors

1. `Machine "apps" doesn't exist.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/services` and `GET /v1/machines/{machine}/services` | `list_services` |

### `tender services start`

Starts a service. Starting a running service changes nothing.

#### Usage

```sh
tender services start MACHINE SERVICE [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's container name. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services start apps web
```

```text
Started web on apps.
```

#### Errors

1. `Service "web" doesn't exist on this machine.`
2. `Service "web" was removed: Docker on this machine no longer has its container.`
3. `the daemon on apps hasn't finished within 60 seconds. Action 3 is still pending: the daemon hasn't picked it up. It runs if the daemon picks it up within 15 minutes of being asked, and fails otherwise. Check it with: tender actions show apps 3`
4. `the daemon on apps hasn't finished within 60 seconds. Action 3 is in progress: the daemon started it. The hub fails it if the daemon doesn't say how it ended within 15 minutes. Check it with: tender actions show apps 3`
5. `Could not start web: ...` with Docker's reason, when the action failed.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/actions` and `GET /v1/machines/{machine}/actions/{id}` | `start_service` |

### `tender services stop`

Stops a service, with the container's own stop timeout. A service Tender stopped doesn't alert as down until it runs again.

> [!WARNING]
> The service stays offline until something starts it again.

#### Usage

```sh
tender services stop MACHINE SERVICE [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's container name. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services stop apps web
```

```text
Stopped web on apps.
```

#### Errors

1. `Service "web" doesn't exist on this machine.`
2. `Service "web" was removed: Docker on this machine no longer has its container.`
3. `the daemon on apps hasn't finished within 60 seconds. ...` with the action's id and status, as for `start`.
4. `Could not stop web: ...` with Docker's reason, when the action failed.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/actions` and `GET /v1/machines/{machine}/actions/{id}` | `stop_service` |

### `tender services restart`

Restarts a service.

#### Usage

```sh
tender services restart MACHINE SERVICE [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's container name. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services restart apps web
```

```text
Restarted web on apps.
```

#### Errors

1. `Service "web" doesn't exist on this machine.`
2. `Service "web" was removed: Docker on this machine no longer has its container.`
3. `the daemon on apps hasn't finished within 60 seconds. ...` with the action's id and status, as for `start`.
4. `Could not restart web: ...` with Docker's reason, when the action failed.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/actions` and `GET /v1/machines/{machine}/actions/{id}` | `restart_service` |

### `tender services logs`

Prints a service's newest lines of output, stdout and stderr together, each starting with Docker's timestamp.

#### Usage

```sh
tender services logs MACHINE SERVICE [--lines N] [--since D] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's container name. |
| `--lines` | integer | No | How many of the newest lines, from 1 to 2000. The default is 200. |
| `--since` | duration | No | Only lines from this far back, such as `30m`, `1h` or `7d`. At most `7d`. |
| `--hub` | URL | No | The hub's address. |

The output stops at 256 KiB and drops the oldest lines past it.

> [!WARNING]
> Logs can hold secrets. The hub clears them 1 hour after the action finished, and never puts them in alerts or emails. See [Security](security.md#logs-can-hold-secrets).

#### Example

```sh
tender services logs apps web --since 10m --lines 3
```

```text
2026-11-15T11:28:14.373342396Z web listening on :3000
```

With no lines, it prints `web on apps has no logs.` and exits 0.

#### Errors

1. `--lines must be from 1 to 2000. Run "tender services logs --help" for usage.`
2. `--since must be a duration such as 30m, 1h or 7d, at most 7d. Run "tender services logs --help" for usage.`
3. `Service "web" doesn't exist on this machine.`
4. `Service "web" was removed: Docker on this machine no longer has its container.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/actions` and `GET /v1/machines/{machine}/actions/{id}` | `get_service_logs` |

### `tender services alerts`

Turns a service's alert emails on or off. They are on for every new service. The switch follows the service's name, as [Services](services.md#what-a-service-is) explains.

#### Usage

```sh
tender services alerts MACHINE SERVICE on|off [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's container name. |
| `on` or `off` | string | Yes | Whether the service sends alert emails. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services alerts apps web off
```

```text
Alerts for web on apps are off. Its open alerts resolve within a minute, or once their resolved email goes out when sending it is retried.
```

#### Errors

1. `services alerts needs a machine, a service and on or off. Run "tender services alerts --help" for usage.`
2. `Service "web" doesn't exist on this machine.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `PATCH /v1/machines/{machine}/services/{service}` | `set_service_alerts` |

## `tender logs`

### `tender logs`

Prints the newest entries of a machine's systemd journal, one per line: the time in UTC, the unit and the message, the newest last. The daemon reads them with `journalctl`, and the command waits up to 60 seconds for it.

#### Usage

```sh
tender logs MACHINE [--unit UNIT] [--priority P] [--since D] [--lines N] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine whose journal to read. |
| `--unit` | string | No | Only this systemd unit, such as `tenderd` or `nginx.service`. |
| `--priority` | string | No | Only this priority and the more severe ones: `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info` or `debug`. |
| `--since` | duration | No | Only entries from this far back, such as `30m`, `1h` or `7d`. At most `7d`. |
| `--lines` | integer | No | How many of the newest entries, from 1 to 2000. The default is 200. |
| `--hub` | URL | No | The hub's address. |

The output stops at 256 KiB and drops the oldest lines past it.

> [!WARNING]
> The journal can hold secrets that services print. The hub clears the output 1 hour after the action finished, and never puts it in alerts or emails. See [Security](security.md#logs-can-hold-secrets).

#### Example

```sh
tender logs apps --unit tenderd --priority warning --since 1h --lines 2
```

```text
2026-11-15T11:27:13.000000000Z tenderd.service backup took 41s, longer than usual.
```

With no matching entries, it prints `apps has no logs.` and exits 0.

#### Errors

1. `--lines must be from 1 to 2000. Run "tender logs --help" for usage.`
2. `--since must be a duration such as 30m, 1h or 7d, at most 7d. Run "tender logs --help" for usage.`
3. `unit must be a systemd unit name such as tenderd or nginx.service.`
4. `Could not read the machine's logs: journalctl is not installed on this machine, so Tender can't read its logs.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/actions` and `GET /v1/machines/{machine}/actions/{id}` | `get_machine_logs` |

## `tender actions`

See what was asked of a machine's daemon: service starts, stops, restarts and logs, and reads of its journal. The hub keeps each machine's newest 100 finished actions, and every `pending` or `in_progress` one.

### `tender actions list`

Lists a machine's actions, newest first, without their output.

#### Usage

```sh
tender actions list MACHINE [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine whose actions to list. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender actions list apps
```

```text
ID   TYPE      OF                                                  STATUS      ASKED
7    logs      web                                                 completed   just now
6    logs      journal, unit tenderd, priority warning, since 1h   completed   just now
3    start     web                                                 completed   just now
2    stop      web                                                 completed   just now
1    restart   web                                                 completed   just now
```

`STATUS` is `pending`, `in_progress`, `completed` or `failed`. With no actions, it prints `Nothing was asked of apps yet.` and exits 0.

#### Errors

1. `Machine "apps" doesn't exist.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/machines/{machine}/actions` | `list_actions` |

### `tender actions show`

Shows one action and its output: the logs, or why it failed.

#### Usage

```sh
tender actions show MACHINE ID [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the action is for. |
| `ID` | integer | Yes | The action's id, from `tender actions list`. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender actions show apps 6
```

```text
Action 6 on apps: logs of journal, unit tenderd, priority warning, since 1h, completed, asked just now.

2026-11-15T11:27:13.000000000Z tenderd.service backup took 41s, longer than usual.
```

An hour after a logs action completed, its output is gone, and the command says when it was cleared:

```text
Action 6 on apps: logs of journal, unit tenderd, priority warning, since 1h, completed, asked 2 hours ago.
Its logs were cleared 1 hour ago: the hub keeps logs only for a while after they are read, because logs can hold secrets.
```

A failed action shows why it failed instead, such as `The daemon restarted before the hub had these logs, and it never keeps logs on disk. Ask for them again.`

#### Errors

1. `the action id must be a number, got "x". Run "tender actions show --help" for usage.`
2. `Action 6 doesn't exist on this machine.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/machines/{machine}/actions/{id}` | `get_action` |

## `tender recipients`

Choose who gets the alert emails. Every recipient gets every alert.

### `tender recipients list`

Lists who gets the alert emails.

#### Usage

```sh
tender recipients list [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender recipients list
```

```text
EMAIL             ADDED
ops@example.com   just now
```

With no recipients, it prints `No recipients yet, so no alert is emailed. Add one with: tender recipients add EMAIL` and exits 0.

#### Errors

1. `cannot reach the hub at URL. ...` when the hub doesn't answer.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/notifications/recipients` | `list_recipients` |

### `tender recipients add`

Adds an address that gets every alert email.

#### Usage

```sh
tender recipients add EMAIL [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `EMAIL` | string | Yes | A bare address, such as `ops@example.com`, without a display name. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender recipients add ops@example.com
```

```text
Added ops@example.com. It gets every alert. Check that the email arrives with: tender email test
```

#### Errors

1. `email must be a bare address such as alerts@example.com.`
2. `ops@example.com is already a recipient.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/notifications/recipients` | `add_recipient` |

### `tender recipients remove`

Removes an address from the alert emails.

#### Usage

```sh
tender recipients remove EMAIL [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `EMAIL` | string | Yes | The address to remove, in any case. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender recipients remove ops@example.com
```

```text
Removed ops@example.com. It no longer gets alerts.
```

#### Errors

1. `ops@example.com is not a recipient. Run "tender recipients list" to see who is.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/notifications/recipients` and `DELETE /v1/notifications/recipients/{id}` | `remove_recipient` |

## `tender email`

Check that alert emails arrive.

### `tender email test`

Sends a test email, with the subject `Tender test email`, to every recipient.

#### Usage

```sh
tender email test [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender email test
```

```text
Sent a test email to ops@example.com.
```

#### Errors

1. `Email is off on this hub. Set TENDER_SMTP_HOST and TENDER_SMTP_FROM in /etc/tender/hub.env and restart the hub.`
2. `There are no recipients to send to. Add one first, such as alerts@example.com.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/notifications/test` | `send_test_email` |

## `tender install`

Set up this machine. Every `install` command needs root. Most people run them through `install.sh`, which downloads and checks the binary first; see [Install](install.md).

### `tender install hub`

Installs and starts the hub on this machine, then publishes it on your tailnet with Tailscale Serve. Running it again keeps the encryption key and every setting whose option you leave out. An option passed empty removes its setting from `/etc/tender/hub.env`.

#### Usage

```sh
sudo tender install hub [--smtp-host HOST --smtp-from SENDER] [--smtp-port PORT --smtp-username USER] [--smtp-password-file PATH]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--smtp-host` | string | No | The SMTP server. Email is off without it. `""` removes it. |
| `--smtp-from` | string | With `--smtp-host` | The sender, such as `"Tender <alerts@example.com>"`. `""` removes it. |
| `--smtp-port` | integer | No | The SMTP port, with implicit TLS. The default is `465`. `0` removes the setting, so the hub uses `465`. |
| `--smtp-username` | string | No | The SMTP login, for servers that ask for one. With it, the command asks for the password with typing hidden. `""` removes it and the password. |
| `--smtp-password-file` | path | No | A file with the SMTP password, instead of asking. Needs `--smtp-username`. |

#### Example

```sh
sudo tender install hub --smtp-host smtp.example.com --smtp-from "Tender <alerts@example.com>" --smtp-username alerts@example.com
```

```text
The hub is running at https://apps.your-tailnet.ts.net.
The tender command on this machine finds it by itself.
Anywhere else, set TENDER_HUB=https://apps.your-tailnet.ts.net or pass --hub https://apps.your-tailnet.ts.net.
```

The output goes on with the next steps: add a destination, a machine, its schedule and the recipients. A run that changes nothing prints `tenderhub is already installed and up to date. Nothing to change.`

The host and the sender go together, so pass both empty to turn alert emails off:

```sh
sudo tender install hub --smtp-host "" --smtp-from ""
```

#### Errors

1. `install hub needs root. Run it again with sudo.`
2. `--smtp-password-file needs --smtp-username. Run "tender install hub --help" for usage.`
3. `the SMTP password is empty. Run the command again and type it, or pass --smtp-password-file PATH.`
4. `alert emails need both an SMTP server and a sender. Pass --smtp-host and --smtp-from together, or both empty, --smtp-host "" --smtp-from "", to turn alert emails off`
5. `TENDER_LISTEN in /etc/tender/hub.env is "127.0.0.1", which is not an address with a port, such as 127.0.0.1:8080. Fix it and run this again`, before the command changes anything.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| None | None |

### `tender install daemon`

Installs and starts the daemon on this machine. It checks the token with the hub before it changes anything.

> [!WARNING]
> The daemon runs as root, in a systemd sandbox. See [Install](install.md#the-daemon-runs-as-root).

#### Usage

```sh
sudo tender install daemon --hub URL --machine NAME [--token-file PATH] [--force]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | Yes, on a new install | The hub's address, such as `https://apps.your-tailnet.ts.net`. |
| `--machine` | string | Yes, on a new install | This machine's name on the hub. |
| `--token-file` | path | No | A file with the hub token. The default is one line on stdin. |
| `--force` | flag | No | Replace an existing `/etc/tender/daemon.toml`. |

The token never comes from an argument, where other users could see it. On a machine that already has a daemon, leave out `--hub` and `--machine`: the config and the token stay, and the command repairs the install.

#### Example

```sh
sudo tender install daemon --hub https://apps.your-tailnet.ts.net --machine apps --token-file /root/apps-token
```

```text
The daemon is running. It gets its destination, schedule and keys from the hub. Next:
  1. Set when this machine backs up, from any machine that can reach the hub:
       tender backups set apps --destination DESTINATION --schedule "30 3 * * *" --retention-days 30
  2. Watch the daemon: journalctl -u tenderd -f
```

#### Errors

1. `install daemon needs both --hub and --machine, or neither on a machine that already has a daemon. Run "tender install daemon --help" for usage.`
2. `the hub at https://apps.your-tailnet.ts.net does not accept this token, so nothing was changed. Make a new one with "tender machines rotate-token apps" and pass it with --token-file PATH.`
3. `cannot reach the hub at https://apps.your-tailnet.ts.net, so nothing was changed. Check --hub and that this machine can reach the hub: ...`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| None | None |

### `tender install refresh`

Checks the pinned restic, rewrites the service units and restarts the installed services. The configs and the credentials stay. On a daemon installed before v0.3.0 it also moves the daemon to root: it gives the daemon's state and cache folders to root and takes the `tender` user out of the `docker` and `systemd-journal` groups. `tender update` runs it for you.

#### Usage

```sh
sudo tender install refresh
```

#### Options

None.

#### Example

```sh
sudo tender install refresh
```

```text
Restarted tenderd
```

#### Errors

1. `install refresh needs root. Run it again with sudo.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| None | None |

## `tender update`

### `tender update`

Updates Tender to the latest stable release, or to `--version`. With machine names or `--all`, the hub rolls the release out, one machine at a time with the hub's own machine first, and that needs no sudo. Without a machine, it updates this machine, as root. [Updates](updates.md) explains both.

Every update checks the release's signature and checksums, waits for a running backup or action to finish, however long it takes, and rolls back if the services aren't healthy within 2 minutes. It refuses prereleases. Only the update by hand can go back to an older release.

#### Usage

```sh
tender update MACHINE ...|--all [--version X.Y.Z] [--hub URL]
sudo tender update [--version X.Y.Z]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | No | A machine to update through the hub. Name one or more. |
| `--all` | flag | No | Update every machine on Tender v0.3.0 or later through the hub. |
| `--version` | string | No | The release to install, such as `0.3.1`. The default is the latest. |
| `--hub` | URL | No | The hub's address, for an update through the hub. |

#### Example

```sh
tender update --all
```

```text
Started rollout 3 of Tender 0.3.1, one machine at a time: db, apps.
Follow it with: tender updates show
```

```sh
sudo tender update
```

```text
Updating Tender 0.3.0 to v0.3.1
Downloaded Tender v0.3.1 and checked its signature and checksums
Installed Tender v0.3.1 at /usr/local/bin/tender
Restarted tenderd

Updated to Tender v0.3.1.
```

#### Errors

1. `updating this machine needs root. Run it again with sudo, or update it through the hub, which needs no sudo: tender update MACHINE, or tender update --all`
2. `Rollout 3 is still in progress. Wait for it to end; it updates one machine at a time.`
3. `db runs a Tender before v0.3.0, which can't update through the hub. Update it once by hand, on that machine: sudo tender update`
4. `Tender v0.3.2-rc.1 is a prerelease, and Tender only updates to stable releases. Name a stable release, such as 0.3.1.`
5. `the signature of Tender v0.3.1 does not match its checksums, so nothing was changed. ...`
6. `Tender v0.3.1 failed its health check, so it rolled back to Tender 0.3.0: ...`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/updates/rollouts` | `start_update` |

## `tender updates`

See and set how Tender updates itself.

### `tender updates show`

Shows the update settings, the newest release the hub found on GitHub, each machine's version and mode, and the last 5 rollouts.

#### Usage

```sh
tender updates show [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender updates show
```

```text
Mode: auto, window: 02:00-05:00 UTC.
Latest release: 0.3.1, checked 2 hours ago.

MACHINE   VERSION   MODE               UPDATE
apps      0.3.0     auto               yes
db        0.3.1     manual (its own)   -

Rollout 3 of 0.3.1, in progress, started 1 minute ago:
  db: completed
  apps: in progress
```

#### Errors

None of its own.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/updates` | `get_updates` |

### `tender updates set`

Changes the hub's update mode or window. A machine with its own mode keeps it.

#### Usage

```sh
tender updates set [--mode auto|manual] [--window HH:MM-HH:MM] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--mode` | string | No | `auto` rolls out each new stable release by itself; `manual` waits for `tender update`. |
| `--window` | string | No | When auto mode may start a rollout, in UTC, such as `02:00-05:00`. `""` is any time. |
| `--hub` | URL | No | The hub's address. |

Pass at least one of `--mode` and `--window`. The other keeps its setting.

#### Example

```sh
tender updates set --mode auto --window 02:00-05:00
```

```text
Updates are auto, window: 02:00-05:00 UTC.
```

#### Errors

1. `updates set needs --mode or --window. Run "tender updates set --help" for usage.`
2. `mode must be auto or manual.`
3. `window must be two UTC times, such as 02:00-05:00.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `PUT /v1/updates/settings` | `set_update_settings` |

## `tender security`

### `tender security`

Shows what each machine's daemon found in its security checks: the firewall, the ports open on a public address, SSH, pending updates and a needed reboot. With a machine, it shows that machine's checks in full. [Security](security.md#security-checks) explains each check.

#### Usage

```sh
tender security [MACHINE] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | No | The machine to show in full. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender security
```

```text
MACHINE   FIREWALL             EXPOSED    SSH         UPDATES                REBOOT
apps      ufw, incoming deny   8080/tcp   keys only   3 pending, 1 security  not needed
db        ufw, incoming deny   none       keys only   0 pending              not needed
```

#### Errors

1. `This machine hasn't sent a security report yet. Its daemon sends one when it starts and every 15 minutes, from Tender v0.3.0 on.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/machines/{machine}/security` | `get_security` |

## `tender hub`

### `tender hub`

Runs the hub, which keeps every machine's state, serves the REST API and the MCP endpoint, and sends alert emails. Its `tenderhub` service runs it with the settings in `/etc/tender/hub.env`.

#### Usage

```sh
tender hub [--database PATH] [--listen ADDR]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--database` | path | No | The SQLite database file. Overrides `TENDER_DATABASE`. |
| `--listen` | address | No | The address to listen on. Overrides `TENDER_LISTEN`. |

It reads its other settings from the environment. See [Hub settings](install.md#hub-settings). `tender hub help` prints them.

#### Example

```sh
sudo systemctl status tenderhub
```

The hub logs to the journal:

```text
level=INFO msg="hub listening" address=127.0.0.1:8080 database=/var/lib/tender/hub/hub.db version=v0.3.0 email=true
```

#### Errors

1. `TENDER_ENCRYPTION_KEY is not set. The hub stores secrets encrypted with it. Make a key with "openssl rand -base64 32" and put it in /etc/tender/hub.env as TENDER_ENCRYPTION_KEY=<the key>, or run "sudo tender install hub", which does both`
2. `TENDER_ENCRYPTION_KEY does not open the secrets in /var/lib/tender/hub/hub.db: ...` when the key is not the one the secrets were stored with.
3. `the hub can't write its database /srv/tender/hub.db: /srv/tender: read-only file system. Its service may only write in /var/lib/tender/hub: keep the database there, or let it write that folder with ReadWritePaths= in a drop-in, made with: sudo systemctl edit tenderhub` when the hub may not write the database or its folder.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| None | None |

## `tender daemon`

### `tender daemon`

Runs the daemon, which backs up the machine on the schedule the hub sets, reports usage, Docker services and security checks, and runs the actions the hub queues. Its `tenderd` service runs it as root, in a systemd sandbox.

#### Usage

```sh
tender daemon [--config PATH] [--state PATH] [--check]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--config` | path | No | The daemon's config file. The default is `/etc/tender/daemon.toml`. |
| `--state` | path | No | The folder for the daemon's state. The default is `/var/lib/tender/daemon`. |
| `--check` | flag | No | Check that the config, the hub token and the state folder can be read, print `The daemon's config, hub token and state folder are in order.`, and exit. `sudo tender update` runs it as its health check. |

It reaches Docker at `/var/run/docker.sock`, or at the socket in `TENDER_DOCKER_SOCKET`.

#### Example

```sh
journalctl -u tenderd -f
```

```text
level=INFO msg="daemon started" machine=apps hub=https://apps.your-tailnet.ts.net version=v0.3.0
level=INFO msg="backup due" schedule="30 3 * * *"
level=INFO msg="run finished" type=backup status=completed error=""
level=INFO msg="run finished" type=cleanup status=completed error=""
```

#### Errors

1. `cannot read the hub token in /etc/tender/credentials. Run "sudo tender install daemon --token-file PATH" to save it.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| None | None |

## `tender mcp`

### `tender mcp`

Serves the MCP tools over standard input and output, for an AI client on the hub machine. It reads and writes the hub's database directly. Clients elsewhere use the hub's `/mcp` endpoint; see [Connect an AI client](guides/connect-an-ai-client.md).

#### Usage

```sh
tender mcp --stdio [--database PATH]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--stdio` | flag | Yes | Serve over standard input and output. |
| `--database` | path | No | The hub's database. The default is `TENDER_DATABASE`, else `/var/lib/tender/hub/hub.db`. |

Run it as the `tender` user, which owns `/var/lib/tender/hub`. The hub must have run once on the same version, so the database has the current schema. The tools that store destination keys need `TENDER_ENCRYPTION_KEY`, and `send_test_email` needs the `TENDER_SMTP_` variables. [Connect an AI client](guides/connect-an-ai-client.md#connect-over-stdio-on-the-hub-machine) explains what `sudo` and root change.

#### Example

```sh
claude mcp add tender --scope user -- sudo -u tender tender mcp --stdio
```

It speaks MCP's JSON-RPC on stdin and stdout, and logs errors on stderr.

#### Errors

1. `no database at /var/lib/tender/hub/hub.db. Run "tender hub" once to create it, or pass --database PATH.`
2. `cannot open the hub database /var/lib/tender/hub/hub.db. Check that you can read and write it, or pass --database PATH: ...`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| None | None |

## `tender version`

### `tender version`

Prints the version of this binary.

#### Usage

```sh
tender version
```

#### Options

None.

#### Example

```sh
tender version
```

```text
Tender 0.3.0
```

#### Errors

None.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| None | None |

## Next steps

1. [API reference](api.md) for the REST routes behind each command.
2. [MCP reference](mcp.md) for the tools an AI client uses.
3. [Quickstart](quickstart.md) to set up a hub and a first machine.
