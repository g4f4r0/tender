# CLI

The `tender` command manages the hub, its machines, backups, services, log sinks and notifications, and installs Tender on a machine.

## Prerequisites

1. The `tender` binary at `/usr/local/bin/tender`. The installer puts it there on every machine with a hub or a daemon. See [Install](install.md).
2. Network access to the hub, for every command that talks to it.
3. An API token in `TENDER_TOKEN`, for every command that talks to the hub. See [Global behavior](#global-behavior).
4. Root, only for `tender install`, `tender update` and the first `tender tokens add`. The `hub` and `daemon` commands run as their systemd services.

## Global behavior

Commands that talk to the hub find it in this order:

1. The `--hub URL` option, such as `--hub https://apps.your-tailnet.ts.net`.
2. The `TENDER_HUB` environment variable.
3. `http://127.0.0.1:8080`, where the hub listens on its own machine. On the hub machine you need neither.

They send an API token, from the first of:

1. The `--token TOKEN` option. It shows in your shell history and the process list, so prefer the file.
2. The `TENDER_TOKEN` environment variable, such as `export TENDER_TOKEN=tender_...`.
3. The file `tender/token` in `XDG_CONFIG_HOME`, else `~/.config/tender/token`, with spaces and line breaks around the token left out. `sudo tender install hub` writes it on the hub machine, and [`tender tokens add`](#tender-tokens-add) prints a line that writes it on another.

A token file that other users can read is refused, with the fix:

```text
tender: the API token file /home/you/.config/tender/token can be read by other users (mode 0644), so the tender command won't use it. Make it yours alone with: chmod 600 /home/you/.config/tender/token
```

Without a token, a command stops with:

```text
tender: the hub at https://apps.your-tailnet.ts.net needs an API token. Save one in ~/.config/tender/token, set TENDER_TOKEN to one, or pass --token TOKEN. Make one on the hub machine with: sudo tender tokens add NAME
```

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

Shows every machine, when it last reported, its last backup, how many of its services run, and the open problems.

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
apps      1 minute ago   just now      completed   1/2 running, 3 containers

Open problems: 1
SEVERITY   SINCE      EVENT                 PROBLEM
warning    just now   service.memory_high   web in notes on apps uses 950 MiB, 92% of its 1 GiB memory limit.
```

`SERVICES` is how many of the machine's services are `running`, of all of them, and how many containers they have. It says `Docker unreachable` while the machine's Docker doesn't answer, and doesn't count removed containers, or a service Tender didn't deploy once they are all removed. A machine whose Docker is turned off shows `-`, as one without Docker does. Against an older hub without `GET /v1/services`, the table leaves out the `SERVICES` column. Each open problem shows its latest notification, as [Notifications](notifications.md#how-a-notification-works) explains. A message longer than 100 characters, or longer than one line, ends in `...`; the notification has the whole text. With no open problems, the list ends with `No open problems.`

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
> There is no undo. The machine's token stops working, and its settings, backup history, services, actions and notifications are deleted from the hub. Its backups stay in the destination.

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
tender machines set NAME [--updates inherit|auto|manual] [--allow-port PORT|PROGRAM ...]
                         [--deny-port PORT|PROGRAM ...] [--security-alerts on|off] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The machine's name. |
| `--updates` | string | No | `auto` or `manual` for the machine's own update mode, or `inherit` for the hub's. |
| `--allow-port` | string | No | A port with its protocol, such as `41641/udp`, that may listen on a public address without a `security.port_exposed` notification, or a program's name as `ss` shows it, such as `cloudflared`, whose every port may. A name is 1 to 32 letters, digits, dots, dashes and underscores, not digits only. Repeat it for more. |
| `--deny-port` | string | No | A port or program to take off the allowlist. Repeat it for more. |
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
2. `"41641" is not a port or a program. Write a port with its protocol, such as 41641/udp or 443/tcp, or a program's name as ss shows it, such as cloudflared, to allow every port it listens on.`
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

Manage the places backups go, with the keys and the repository password the hub keeps for them. [Backups](backups.md#destination-types) explains each type.

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
NAME   TYPE   LOCATION
nas    sftp   sftp://tender@nas.example.com:22//srv/restic
r2     s3     s3:https://<account-id>.r2.cloudflarestorage.com/my-backups/tender
```

`LOCATION` is the restic repository. Each machine's repository is a folder named after the machine in it. With no destinations, it prints `No destinations yet. Add one with: tender destinations add NAME --type TYPE ... (run tender destinations add --help for the types)` and exits 0.

#### Errors

1. `cannot reach the hub at URL. ...` when the hub doesn't answer.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/backups/destinations` | `list_destinations` |

### `tender destinations add`

Adds a destination. It asks for the type's secrets and the repository password with typing hidden, or reads each from a file.

#### Usage

```sh
tender destinations add NAME [--type TYPE] [OPTIONS] [--password-file PATH] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The destination's name: lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |
| `--type` | string | No | `s3`, the default, `local`, `sftp`, `rest` or `rclone`. |
| `--endpoint` | URL | For `s3` | The S3 endpoint, such as `https://<account-id>.r2.cloudflarestorage.com`. |
| `--bucket` | string | For `s3` | The bucket name. |
| `--path` | string | For `local` and `sftp` | For `s3`, an optional folder inside the bucket, the bucket's root by default. For `local`, an absolute folder on each machine. For `sftp`, the folder on the server, absolute or relative to the user's home. |
| `--host` | string | For `sftp` | The SSH server's host name or IP address. |
| `--port` | number | No | For `sftp`, the SSH port. The default is 22. |
| `--user` | string | For `sftp` | The SSH user. |
| `--url` | URL | For `rest` | The rest-server's address, such as `https://backup.example.com:8000`. |
| `--username` | string | No | For `rest`, the login, when the server has logins. It then asks for the password. |
| `--remote` | string | For `rclone` | The rclone remote and a folder in it, such as `gdrive:backups`. |
| `--access-key-id-file` | path | No | For `s3`, a file with the access key ID, instead of asking. |
| `--secret-access-key-file` | path | No | For `s3`, a file with the secret access key, instead of asking. |
| `--rest-password-file` | path | No | For `rest` with `--username`, a file with the login's password, instead of asking. |
| `--password-file` | path | No | A file with the repository password, instead of asking. |
| `--hub` | URL | No | The hub's address. |

Each machine gets its own folder under the place you give. Leave the password empty when it asks, and the hub makes a strong one and shows it once. Without a terminal, the secrets must come from files, and the hub makes the password unless `--password-file` gives one.

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

Save the password in a password manager. It can't change later, and without it nobody can restore the backups at this destination. An SFTP server takes `tender destinations add nas --type sftp --host nas.example.com --user tender --path /srv/restic`, once each machine has its SSH key, as [Backups](backups.md#sftp) shows.

#### Errors

1. `destinations add needs --endpoint and --bucket. Run "tender destinations add --help" for usage.`
2. `--host is not for a s3 destination. Run "tender destinations add --help" for usage.`, for an option of another type.
3. `there is no terminal to ask for the access key ID. Save it in a file and add --access-key-id-file PATH.`
4. `A destination with this name or this location (...) already exists. Choose another name, or use the existing one.`
5. The hub's sentence for a field that isn't valid, such as `config.endpoint must be a URL such as https://<account>.r2.cloudflarestorage.com.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/backups/destinations` | `add_destination` |

### `tender destinations update`

Changes where a destination is, or its keys. Only the options you give change.

New secrets come from the files you name, never from a prompt. The type and the repository password can't change. Move the data yourself before you point a destination at another place. Daemons pick up the change within 10 minutes.

#### Usage

```sh
tender destinations update NAME [OPTIONS] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The destination to change. |
| `OPTIONS` | | Yes, one or more | The place and secret-file options of [`tender destinations add`](#tender-destinations-add) for the destination's type, except `--type` and `--password-file`. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender destinations update r2 --access-key-id-file ./key-id --secret-access-key-file ./secret-key
```

```text
Updated destination r2: s3:https://<account-id>.r2.cloudflarestorage.com/my-backups/tender.
```

#### Errors

1. `destinations update needs an option to change. Run "tender destinations update --help" for usage.`
2. `--remote is not for a s3 destination. Run "tender destinations update --help" for usage.`
3. `Destination "r2" doesn't exist.`
4. `Another destination already uses .... Use that one, or choose another location.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `PATCH /v1/backups/destinations/{destination}` | `update_destination` |

### `tender destinations test`

Tries a destination from a machine. The machine's daemon opens its repository at the destination, which checks the address, the login and the password, or makes the repository when there is none yet.

It waits up to 60 seconds for the daemon. Run it after you add a destination and before you point a machine's backups at it.

#### Usage

```sh
tender destinations test NAME MACHINE [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The destination to try. |
| `MACHINE` | string | Yes | The machine whose daemon tries it. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender destinations test nas apps
```

```text
Reached sftp://tender@nas.example.com:22//srv/restic/apps from apps. It had no repository for apps yet, so the test made one with the destination's password.
```

A second run prints `Reached ... Its repository opens with the destination's password.`

#### Errors

1. `destinations test needs a destination name and a machine name. Run "tender destinations test --help" for usage.`
2. `Destination "nas" doesn't exist.`
3. `Machine "apps" doesn't exist. Run tender machines list to see the names.`
4. `Could not reach the destination nas from apps: ...`, with restic's reason or what the machine lacks, and the command exits 1.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/backups/destinations/{destination}/test` | `test_destination` |

### `tender destinations remove`

Removes a destination that no machine backs up to.

> [!WARNING]
> The hub forgets the destination's keys and password. The backups already at the destination stay there, so keep the password in your password manager to restore them.

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
Removed destination r2. The backups already in it stay there.
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

See, deploy and control each machine's services. A service is a compose project, or a container without one, and its containers are named by their compose service, such as `web`. See [Services](services.md#what-a-service-is).

Start, stop, restart and logs go to the machine's daemon, and the command waits up to 60 seconds for it. Deploy, redeploy and remove wait up to 45 minutes, because compose may pull large images and wait for health checks; `--no-wait` returns at once. If the daemon hasn't finished in time, the command says what happens next from the action's status, and you check it with `tender actions show`. A `pending` action runs if the daemon picks it up within 15 minutes of being asked, and fails otherwise. An `in_progress` one fails if the daemon doesn't say how it ended within 15 minutes, or 45 for a deploy or remove.

### `tender services list`

Lists every machine's services, or one machine's, from the latest report.

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
MACHINE   SERVICE   STATE      RUNNING   MEM       CPU     DEPLOY     ALERTS
apps      notes     degraded   1/2       1.5 GiB   2.5%    deployed   off
apps      redis     running    1/1       200 MiB   12.5%   -          on
```

The columns are:

1. `STATE` is `running`, `degraded`, `stopped` or `unknown`, as [Services](services.md#what-a-service-is) explains.
2. `RUNNING` is how many of its containers run, of those Docker still has. A service whose containers are all gone shows `removed`, as [Removed containers](services.md#removed-containers) explains.
3. `MEM` and `CPU` are its running containers' together, where 100% is one whole CPU. They show `-` while no container says, as [What the daemon reports](services.md#what-the-daemon-reports) explains.
4. `DEPLOY` is how the last deploy of a service Tender deployed went: `deploying`, `deployed`, `failed` or `removing`, and `-` for one it didn't.
5. `ALERTS` says whether the service alerts.

While Docker is unreachable, the list ends with `Docker is unreachable on apps, so the states of its services are unknown.` Once Docker is turned off, as [Services](services.md#when-docker-is-turned-off) explains, its services show `removed`, and the list says nothing about Docker. With no services, it prints `apps has no services. Its daemon reports the Docker containers once a minute; a machine without Docker has none.` and exits 0.

#### Errors

1. `Machine "apps" doesn't exist.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/services` and `GET /v1/machines/{machine}/services` | `list_services` |

### `tender services show`

Shows one service: its state, how its last deploy went and why it failed, its routes with where they answer, its env keys and files, and each container.

#### Usage

```sh
tender services show MACHINE SERVICE [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's name. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services show apps notes
```

```text
Service notes on apps: degraded.
Deploy: deployed, last succeeded 2 hours ago (action 7).
Its alerts are off.

CONTAINER   PORT   EXPOSE      LOCAL             URL
web         8080   tailscale   127.0.0.1:20000   https://apps.tail1234.ts.net:8443

Env: DB_URL, SECRET_KEY.
Files: config/app.toml.

CONTAINER   IMAGE                STATE                     HEALTH    MEM               CPU    RESTARTS   STARTED
web         ghcr.io/me/notes:1   running                   healthy   1.5 GiB / 2 GiB   2.5%   0          3 hours ago
worker      ghcr.io/me/notes:1   exited (137), by Tender   -         -                 -      4          -
```

It never shows the env values. After a failed deploy, `Why the last one failed:` follows the deploy line, with the reason and the end of compose's output. A container's `STATE` is Docker's state, with the exit code once stopped, and `by Tender` when a Tender stop left it stopped; while Docker is unreachable it is `unknown` alone, and a removed container shows `removed` and when. A route on the tailnet before the daemon has sent the machine's tailnet name shows `the tailnet name isn't known yet`. A service Tender didn't deploy says `Tender found it running and didn't deploy it, so it can start, stop and watch it, but not deploy it.` instead of the deploy line.

#### Errors

1. `Service "notes" doesn't exist on this machine. Run tender services list to see the names.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/machines/{machine}/services/{service}` | `get_service` |

### `tender services deploy`

Deploys a service from a docker compose file, through the daemon on the machine, and waits for it. A service Tender deployed already changes: the file replaces its compose file, and its files, env and routes are replaced when their options are given and kept when not. [Deploy a service](services.md#deploy-a-service) explains what the daemon does and the rules the compose file must follow.

#### Usage

```sh
tender services deploy MACHINE NAME -f FILE [--file PATH ...] [--env-file PATH] [--env KEY=VALUE ...] [--route CONTAINER:PORT:EXPOSE[:HOST] ...] [--no-wait] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine to deploy it on. |
| `NAME` | string | Yes | The service's name, its compose project: lowercase letters and digits, starting with a letter, at most 32, with no dashes. |
| `-f` | path | Yes | The docker compose file. |
| `--file` | path | No | A config file the compose file mounts, in the compose file's folder; repeat for each. It goes to the same path relative to the compose file on the machine. |
| `--env-file` | path | No | A file of `KEY=VALUE` lines for the env. Blank lines and `#` comments are skipped, `export ` is taken off, and so is one pair of quotes around a value. |
| `--env` | `KEY=VALUE` | No | One env value, over the env file's; repeat for each. |
| `--route` | string | No | A route, such as `web:8080:tailscale`, `web:8080:tailscale:443` for HTTPS port 443, or `web:8080:public:notes.example.com`; repeat for each. See [Routes](exposure.md#routes). |
| `--no-wait` | flag | No | Queue the deploy and return, without waiting for it. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services deploy apps notes -f compose.yaml --file config/app.toml --env-file .env --route web:8080:tailscale
```

```text
Deployed notes on apps.
web answers at https://apps.tail1234.ts.net:10000
```

With `--no-wait`, it prints `Queued action 13. Follow it with: tender actions show apps 13`.

#### Errors

1. `services deploy needs the compose file: -f compose.yaml. Run "tender services deploy --help" for usage.`
2. `--route "web:80" must be CONTAINER:PORT:EXPOSE or CONTAINER:PORT:EXPOSE:HOST, such as web:80:tailscale or web:80:public:app.example.com. Run "tender services deploy --help" for usage.`
3. `--file /etc/app.toml is outside the compose file's folder, .; a service's files are relative to its compose file. Run "tender services deploy --help" for usage.`
4. `Tender didn't deploy service "redis", so it can't change it: ...` for a service Tender found running.
5. `Could not deploy notes: ...` when the deploy failed, with the rule the compose file broke, such as `service web in the compose file publishes ports, which Tender's routes do instead: remove ports from web and add a route to its port, such as --route web:80:tailscale`, or compose's reason and the end of its output.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/services`, or `PATCH /v1/machines/{machine}/services/{service}` for a service that exists | `deploy_service` |

### `tender services redeploy`

Deploys a service Tender deployed again, as the hub stores it, and waits for it. Compose pulls the images again, so a new image under the same tag runs, and recreates the containers that changed.

#### Usage

```sh
tender services redeploy MACHINE SERVICE [--no-wait] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's name. |
| `--no-wait` | flag | No | Queue the deploy and return, without waiting for it. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services redeploy apps notes
```

```text
Deployed notes on apps again.
web answers at https://apps.tail1234.ts.net:10000
```

#### Errors

1. `Service "notes" doesn't exist on this machine. Run tender services list to see the names.`
2. `Tender didn't deploy service "redis", so it can't deploy it: ...`
3. `Could not deploy notes: ...` with compose's reason, when the deploy failed.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/services/{service}/deploy` | `redeploy_service` |

### `tender services remove`

Removes a service Tender deployed, through the daemon on the machine. `docker compose down` stops and removes its containers, its routes stop answering, and the hub forgets it. Its folder, with its named volumes' data, is kept in `/var/lib/tender/services/.removed/` on the machine, unless `--delete-data` deletes it.

> [!WARNING]
> `--delete-data` deletes the service's data, and can't be undone.

#### Usage

```sh
tender services remove MACHINE SERVICE [--delete-data] --yes [--no-wait] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's name. |
| `--delete-data` | flag | No | Delete the service's folder and its data too. |
| `--yes` | flag | Yes | Confirm the removal. |
| `--no-wait` | flag | No | Queue the remove and return, without waiting for it. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services remove apps notes --yes
```

```text
Removed notes from apps.
 Container notes-web-1  Removed
 Container notes-worker-1  Removed
Kept the service's folder, data and all, in /var/lib/tender/services/.removed/notes-1794657600. Delete it when you no longer need it: sudo rm -rf /var/lib/tender/services/.removed/notes-1794657600
```

#### Errors

1. `this removes notes from apps, keeping its data on the machine. Run "tender services remove apps notes --yes" to confirm` without `--yes`.
2. `Tender didn't deploy service "redis", so it can't remove it: ...`
3. `Could not remove notes: ...` with compose's reason, when the remove failed. The hub keeps the service, and you can remove it again.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `DELETE /v1/machines/{machine}/services/{service}` | `remove_service` |

### `tender services start`

Starts every container of a service, or one with `--container`. Starting a running container changes nothing. A service Tender deployed starts through `docker compose start`, and one container or any other service through Docker.

`tender services stop` and `tender services restart` take the same options. A stop uses each container's own stop timeout, and a container Tender stopped doesn't alert as down until it runs again.

> [!WARNING]
> `tender services stop` takes the service offline until something starts it again.

#### Usage

```sh
tender services start|stop|restart MACHINE SERVICE [--container C] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's name. |
| `--container` | string | No | Only this container of the service, such as `web`. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services restart apps notes --container web
```

```text
Restarted web in notes on apps.
```

Without `--container`, it prints `Restarted notes on apps.`

#### Errors

1. `Service "notes" doesn't exist on this machine. Run tender services list to see the names.`
2. `Container "nope" isn't in service notes. Its containers are: web, worker.`
3. `Container worker in notes was removed: Docker on this machine no longer has it.`
4. `the daemon on apps hasn't finished within 60 seconds. Action 3 is still pending: the daemon hasn't picked it up. It runs if the daemon picks it up within 15 minutes of being asked, and fails otherwise. Check it with: tender actions show apps 3`
5. `the daemon on apps hasn't finished within 60 seconds. Action 3 is in progress: the daemon started it. The hub fails it if the daemon doesn't say how it ended within 15 minutes. Check it with: tender actions show apps 3`
6. `Could not restart web in notes: ...` with Docker's or compose's reason, when the action failed.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/services/{service}/start`, `.../stop` or `.../restart`, and `GET /v1/machines/{machine}/actions/{id}` | `start_service`, `stop_service` or `restart_service` |

### `tender services logs`

Prints a service's newest lines of output, stdout and stderr together, each starting with Docker's timestamp. The lines of several containers are merged by time, each with its container's name after the timestamp.

#### Usage

```sh
tender services logs MACHINE SERVICE [--container C] [--lines N] [--since D] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's name. |
| `--container` | string | No | Only this container of the service, such as `web`. |
| `--lines` | integer | No | How many of the newest lines, from 1 to 2000. The default is 200. |
| `--since` | duration | No | Only lines from this far back, such as `30m`, `1h` or `7d`. At most `7d`. |
| `--hub` | URL | No | The hub's address. |

The output stops at 256 KiB and drops the oldest lines past it.

> [!WARNING]
> Logs can hold secrets. The hub clears them 1 hour after the action finished, and never puts them in alerts or emails. See [Security](security.md#logs-can-hold-secrets).

#### Example

```sh
tender services logs apps notes --since 10m --lines 3
```

```text
2026-11-15T11:28:12.120541873Z worker | job 41 done
2026-11-15T11:28:14.373342396Z web | listening on :8080
2026-11-15T11:28:15.002114220Z web | GET /healthz 200
```

With no lines, it prints `notes on apps has no logs.` and exits 0.

#### Errors

1. `--lines must be from 1 to 2000. Run "tender services logs --help" for usage.`
2. `--since must be a duration such as 30m, 1h or 7d, at most 7d. Run "tender services logs --help" for usage.`
3. `Service "notes" doesn't exist on this machine. Run tender services list to see the names.`
4. `Container worker in notes was removed: Docker on this machine no longer has it.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/machines/{machine}/actions` and `GET /v1/machines/{machine}/actions/{id}` | `service_logs` |

### `tender services alerts`

Turns a service's alerts on or off, for all its containers. They are on for every new service: an alert when a container stops, keeps restarting, turns unhealthy or uses more than 90% of its memory limit, and when a deploy fails. The switch stays when the service's containers are made again.

#### Usage

```sh
tender services alerts MACHINE SERVICE on|off [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | Yes | The machine the service runs on. |
| `SERVICE` | string | Yes | The service's name. |
| `on` or `off` | string | Yes | Whether the service alerts. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender services alerts apps notes off
```

```text
Alerts for notes on apps are off. Its open problems resolve within a minute.
```

#### Errors

1. `services alerts needs a machine, a service and on or off. Run "tender services alerts --help" for usage.`
2. `Service "notes" doesn't exist on this machine. Run tender services list to see the names.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `PATCH /v1/machines/{machine}/services/{service}` | `set_service_alerts` |

## `tender registries`

Keep the logins of the container registries that services Tender deploys pull private images from. A deploy uses the login of the registry whose host an image is on. The hub keeps the passwords encrypted and never shows them. See [Registries](services.md#registries).

### `tender registries list`

Lists the registries: name, host and username.

#### Usage

```sh
tender registries list [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender registries list
```

```text
NAME   HOST      USERNAME
ghcr   ghcr.io   me
```

With none, it prints `No registries yet. Add one with: tender registries add NAME --host ghcr.io --username USER`.

#### Errors

1. `cannot reach the hub at URL. ...` when the hub doesn't answer.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/registries` | `list_registries` |

### `tender registries add`

Adds the login of a container registry. It asks for the password or access token, with typing hidden, unless `--password-file` has it.

#### Usage

```sh
tender registries add NAME --host HOST --username USER [--password-file PATH] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The registry's name: lowercase letters, digits and dashes. |
| `--host` | string | Yes | The registry's host, as image names start with it, such as `ghcr.io` or `registry.example.com:5000`; `docker.io` for Docker Hub. |
| `--username` | string | Yes | The login name. |
| `--password-file` | path | No | A file whose first line is the password or access token. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender registries add ghcr --host ghcr.io --username me
```

```text
Password (typing is hidden):
Added registry ghcr for ghcr.io.
```

#### Errors

1. `registries add needs --host and --username. Run "tender registries add --help" for usage.`
2. `Registry "ghcr" already exists. Change it with tender registries update.`
3. `Registry "ghcr" already has the host ghcr.io; a host has one login.`
4. `This process has no TENDER_ENCRYPTION_KEY, ...` when the hub can't store the password.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/registries` | `add_registry` |

### `tender registries update`

Changes a registry's host, username or password. What isn't given stays.

#### Usage

```sh
tender registries update NAME [--host HOST] [--username USER] [--password] [--password-file PATH] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The registry's name. |
| `--host` | string | No | A new host. |
| `--username` | string | No | A new login name. |
| `--password` | flag | No | Ask for a new password or access token, with typing hidden. |
| `--password-file` | path | No | A file whose first line is a new password or access token. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender registries update ghcr --password
```

```text
Password (typing is hidden):
Updated registry ghcr.
```

#### Errors

1. `registries update needs --host, --username, --password or --password-file. Run "tender registries update --help" for usage.`
2. `Registry "ghcr" doesn't exist. Run tender registries list to see the names.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `PATCH /v1/registries/{registry}` | `update_registry` |

### `tender registries remove`

Removes a registry's login. Services whose images are on it can no longer pull them, unless they are public.

#### Usage

```sh
tender registries remove NAME [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The registry's name. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender registries remove ghcr
```

```text
Removed registry ghcr.
```

#### Errors

1. `Registry "ghcr" doesn't exist. Run tender registries list to see the names.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `DELETE /v1/registries/{registry}` | `remove_registry` |

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

## `tender sinks`

Manage the log sinks, where each machine's daemon sends its journal and its Docker containers' logs, straight from the machine. [Log shipping](log-shipping.md) explains how sending works.

### `tender sinks list`

Lists the sinks, with how sending to each goes. Their secrets are never shown.

#### Usage

```sh
tender sinks list [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender sinks list
```

```text
NAME   TYPE      SENDS TO                                MACHINES   STATUS
hook   webhook   https://logs.example.com/tender         apps       ok
otel   otlp      https://otel.example.com:4318/v1/logs   all        failing on web
```

`STATUS` is `ok` when every machine's last report says sending works, `failing on` the machines where it fails, `dropped on` the machines that dropped records, `no report yet` before the first report, and `disabled` for a disabled sink. With no sinks, it prints `No log sinks yet. Add one with: tender sinks add NAME --type otlp --endpoint URL` and exits 0.

#### Errors

1. `cannot reach the hub at URL. ...` when the hub doesn't answer.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/logs/sinks` | `list_log_sinks` |

### `tender sinks add`

Adds a sink. Each machine's daemon starts sending to it within a minute, with the logs from then on. Secrets come from files, so they stay out of your shell history.

#### Usage

```sh
tender sinks add NAME --type TYPE [OPTIONS] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The sink's name: lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters. |
| `--type` | string | Yes | `otlp` or `webhook`. |
| `--endpoint` | URL | For `otlp` | The collector's OTLP/HTTP logs URL, such as `https://otel.example.com:4318/v1/logs`. |
| `--headers-file` | path | No | For `otlp`, a file of HTTP headers to send, one `Name: value` a line, such as `Authorization: Bearer <key>`. Blank lines and lines starting with `#` are skipped. |
| `--url` | URL | For `webhook` | Where to POST the batches. |
| `--secret-file` | path | No | For `webhook`, a file with the signing secret, at least 16 characters. Without it, the hub makes one and shows it once. |
| `--units` | list | No | The journal's systemd units, comma-separated, such as `nginx.service,tenderd.service`, or `all`, the default. |
| `--min-priority` | string | No | The journal's least severe priority sent: `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info`, the default, or `debug`. |
| `--services` | list | No | The Docker containers, comma-separated, or `all`, the default. |
| `--no-journal` | flag | No | Send no journal. |
| `--no-services` | flag | No | Send no container logs. |
| `--machines` | list | No | The machines that send, comma-separated, or `all`, the default. |
| `--disabled` | flag | No | Add it without sending to it yet. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender sinks add hook --type webhook --url https://logs.example.com/tender --machines apps
```

```text
Added log sink hook. Machines start sending to it within a minute.

The signing secret, made by the hub and shown only now:
  <secret>

Give it to the receiver, which checks each batch's Tender-Signature header with it. Tender cannot
show it again; to replace it, run tender sinks update hook --secret-file PATH.
```

An OpenTelemetry collector takes `tender sinks add otel --type otlp --endpoint https://otel.example.com:4318/v1/logs --headers-file ./otel-headers`, which prints only the first line.

#### Errors

1. `sinks add needs --type otlp or --type webhook. Run "tender sinks add --help" for usage.`
2. `line 1 of ./otel-headers isn't a header. Write one "Name: value" a line, such as Authorization: Bearer KEY`
3. `config.endpoint is required: an http or https URL, such as https://otel.example.com:4318/v1/logs.`
4. `config has endpoint, which a webhook sink doesn't take. It takes url, secret.`, for an option of the other type.
5. `sources must name journal, services or both, or the sink gets nothing. Leave sources out for both.`, for `--no-journal --no-services`.
6. `A log sink called hook already exists. Choose another name, or change that one.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/logs/sinks` | `add_log_sink` |

### `tender sinks show`

Shows a sink and how sending to it goes on each machine. Its secrets are never shown.

#### Usage

```sh
tender sinks show NAME [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The sink to show. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender sinks show otel
```

```text
NAME       otel
TYPE       otlp
SENDS TO   https://otel.example.com:4318/v1/logs
ENABLED    yes
SOURCES    journal (warning and up)
MACHINES   all

MACHINE   SENT    DROPPED   LAST SENT     PROBLEM
web       18342   120       3 hours ago   failing since 3 hours ago: the sink answered 503 Service Unavailable
```

`SENT` and `DROPPED` count records since the daemon first sent to the sink. Before any machine reports, the table is replaced by `No machine has reported on it yet. Daemons report every minute.`

#### Errors

1. `Log sink "otel" doesn't exist.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/logs/sinks/{sink}` | `list_log_sinks` |

### `tender sinks update`

Changes a sink. Only the options you give change, and daemons pick the change up within a minute. The name and the type can't change.

#### Usage

```sh
tender sinks update NAME [OPTIONS] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The sink to change. |
| `OPTIONS` | | Yes, one or more | The options of [`tender sinks add`](#tender-sinks-add) except `--type` and `--disabled`. `--units`, `--min-priority` and `--services` turn their source back on. |
| `--enable` | flag | No | Send to it. |
| `--disable` | flag | No | Stop sending to it, and keep it. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender sinks update otel --min-priority warning --no-services
```

```text
Updated log sink otel. Machines pick the change up within a minute.
```

#### Errors

1. `sinks update needs an option to change. Run "tender sinks update --help" for usage.`
2. `--enable and --disable can't go together. Run "tender sinks update --help" for usage.`
3. `Log sink "otel" doesn't exist.`
4. The hub's sentence for a sink that isn't valid, as for `tender sinks add`.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `PATCH /v1/logs/sinks/{sink}` | `update_log_sink` |

### `tender sinks remove`

Removes a sink.

> [!WARNING]
> Daemons stop sending to the sink within a minute. They keep the logs they buffered for it for 7 days, then delete them, and nothing sends them anywhere: a sink added again is a new sink. The logs already sent stay at the sink.

#### Usage

```sh
tender sinks remove NAME [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The sink to remove. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender sinks remove hook
```

```text
Removed log sink hook. The logs already sent stay at the sink.
```

#### Errors

1. `Log sink "hook" doesn't exist.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `DELETE /v1/logs/sinks/{sink}` | `remove_log_sink` |

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

## `tender channels`

Choose where notifications go: email addresses, Slack, webhooks that get signed JSON, and exec adapters on the hub. Each channel gets the notifications of its `events` that are at least as severe as its `min_severity`. [Adapters](adapters.md) explains each type and the event they get, and [Notifications](notifications.md#events) lists the events.

### `tender channels list`

Lists the channels. Their secrets are never shown.

#### Usage

```sh
tender channels list [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender channels list
```

```text
NAME       TYPE      SENDS TO                            EVENTS                    MIN SEVERITY   ON
ops        email     ops@example.com                     *                         warning        on
chat       slack     Slack webhook (secret)              service.*,backup.failed   critical       on
receiver   webhook   https://alerts.example.com/tender   *                         warning        on
```

With no channels, it prints `No channels yet, so notifications are only recorded. Add one with: tender channels add ops --type email --to ops@example.com` and exits 0.

#### Errors

1. `cannot reach the hub at URL. ...` when the hub doesn't answer.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/notifications/channels` | `list_channels` |

### `tender channels add`

Adds a channel. An email channel needs `--to` and the hub's SMTP settings. A Slack channel asks for its incoming webhook URL with typing hidden, or reads it from `--webhook-url-file`, because the URL is its key. A webhook channel needs `--url`, and the hub makes its signing secret and shows it once. An exec channel needs `--command`, a program root installed in `/etc/tender/adapters` on the hub.

#### Usage

```sh
tender channels add NAME --type TYPE [--to EMAIL | --url URL | --webhook-url-file PATH | --command NAME] [--min-severity SEVERITY] [--events PATTERNS] [--off] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The channel's name: lowercase letters, digits and dashes, such as `ops`. |
| `--type` | string | Yes | `email`, `slack`, `webhook` or `exec`. |
| `--to` | string | For `email` | A bare address, such as `ops@example.com`. |
| `--webhook-url-file` | path | No | For `slack`, a file with the incoming webhook URL, instead of asking. |
| `--url` | URL | For `webhook` | Where the hub posts the signed event. |
| `--command` | string | For `exec` | The program's name in `/etc/tender/adapters`, never a path. |
| `--min-severity` | string | No | The least severe notification it gets: `info`, `warning` or `critical`. The default is `warning`. |
| `--events` | string | No | The events it gets, comma-separated: `*` for every event, an area such as `service.*`, or an event such as `backup.failed`. The default is `*`. |
| `--off` | flag | No | Add it turned off, so it gets no notifications until you turn it on. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender channels add receiver --type webhook --url https://alerts.example.com/tender
```

```text
Added channel receiver. It gets the notifications of * from warning up. Check that it arrives with: tender channels test receiver

Its signing secret, made by the hub and shown only now:
  whsec_DegsXtcqOW0BeSvwAslbz0WjeZpZwFbB_W8oo6EHXuE

Give it to the webhook's receiver, which checks the Tender-Signature header with it. Tender cannot
show it again; replace it with: tender channels update receiver --secret-file PATH
```

Other types print only the first line.

#### Errors

1. `A channel called ops already exists. Choose another name, or change that one.`
2. `there is no /etc/tender/adapters/ntfy. As root on the hub, install the program there and make it executable.`
3. `channels add needs --type: email, slack, webhook or exec. Run "tender channels add --help" for usage.`
4. `events: "disk.*" takes no event this hub knows. The events are listed in docs/notifications.md.` when a pattern matches no event.
5. `url: the URL leads to 127.0.0.1, a loopback address, and the hub doesn't send to loopback, link-local, multicast or unspecified addresses. ...` when a webhook's URL is an address the hub refuses. [Where a webhook may send](adapters.md#where-a-webhook-may-send) has the rules.
6. `webhook_url must be a Slack incoming webhook URL, which starts with https://hooks.slack.com/. Copy it from the app's Incoming Webhooks page.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/notifications/channels` | `add_channel` |

### `tender channels show`

Shows one channel. Its secrets are never shown.

#### Usage

```sh
tender channels show NAME [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The channel's name. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender channels show receiver
```

```text
FIELD          VALUE
name           receiver
type           webhook
on             on
events         *
min severity   warning
secret         set (secret, never shown)
url            https://alerts.example.com/tender
added          just now
changed        just now
```

#### Errors

1. `Channel "receiver" doesn't exist. List the channels to see their names.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/notifications/channels/{channel}` | `list_channels` |

### `tender channels update`

Changes a channel. Options left out stay as they are. Secrets are replaced from a file and never shown. Turning a channel off drops what it still owed.

#### Usage

```sh
tender channels update NAME [--to EMAIL | --url URL | --command NAME] [--webhook-url-file PATH | --secret-file PATH] [--min-severity SEVERITY] [--events PATTERNS] [--on | --off] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The channel's name. Its type can't change. |
| `--to`, `--url`, `--command` | string | No | A new address, URL or program, by the channel's type. |
| `--webhook-url-file` | path | No | A file with a Slack channel's new webhook URL. |
| `--secret-file` | path | No | A file with a webhook channel's new signing secret, 24 characters or more. |
| `--min-severity` | string | No | `info`, `warning` or `critical`. |
| `--events` | string | No | The events it gets, comma-separated, such as `service.*,backup.failed`. |
| `--on`, `--off` | flag | No | Turn it on or off. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender channels update ops --min-severity critical --off
```

```text
Updated channel ops: off, the notifications of * from critical up.
```

#### Errors

1. `channels update needs something to change, such as --off, --min-severity critical or --events 'service.*'. Run "tender channels update --help" for usage.`
2. `channels update takes --on or --off, not both. Run "tender channels update --help" for usage.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `PATCH /v1/notifications/channels/{channel}` | `update_channel` |

### `tender channels remove`

Removes a channel, so it gets no more notifications. What it still owed is dropped.

#### Usage

```sh
tender channels remove NAME [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The channel's name. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender channels remove chat
```

```text
Removed channel chat. It gets no more notifications.
```

#### Errors

1. `Channel "chat" doesn't exist. List the channels to see their names.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `DELETE /v1/notifications/channels/{channel}` | `remove_channel` |

### `tender channels test`

Sends a `notification.test` notification to every enabled channel, or to one named channel even when it is off, and says how each went. Each waits at most 10 seconds, or 30 for email. It exits 1 when any channel failed.

#### Usage

```sh
tender channels test [NAME] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | No | Test only this channel. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender channels test
```

```text
CHANNEL    RESULT
ops        delivered
chat       failed: HTTP 404 Not Found
receiver   delivered
```

A failed channel is followed by `the test didn't reach every channel. Fix the channel's settings with "tender channels update" and test again.`

#### Errors

1. `There are no enabled channels to test. Add one first, such as an email channel to ops@example.com.`
2. `Channel "ops" doesn't exist. List the channels to see their names.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/notifications/test` | `send_test_notification` |

## `tender notifications`

See what the hub told its channels. A notification is one message: a problem adds one when it starts, such as `service.down`, one when it gets worse, `service.down.escalated`, and one when it clears, `service.down.resolved`. [Notifications](notifications.md) explains them and lists every event.

### `tender notifications list`

Lists notifications, newest first: of every machine, or of one. With `--open`, it lists the open problems instead, each by the latest notification of a problem that hasn't resolved.

#### Usage

```sh
tender notifications list [MACHINE] [--open] [--status STATUS] [--severity SEVERITY] [--limit N] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `MACHINE` | string | No | Only this machine's notifications. |
| `--open` | flag | No | Only the open problems. |
| `--status` | string | No | Only `pending`, `sent` or `failed`. |
| `--severity` | string | No | Only `info`, `warning` or `critical`. |
| `--limit` | number | No | How many of the newest, from 1 to 500. The default is 50. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender notifications list apps
```

```text
ID   ADDED           EVENT                   SEVERITY   ABOUT        STATUS   MESSAGE
14   just now        service.down.resolved   critical   apps/notes   sent     Resolved: web in notes on apps stopped with exit code 1.
12   5 minutes ago   service.down            critical   apps/notes   sent     web in notes on apps stopped with exit code 1.
```

`ABOUT` is the machine, the machine and service, or `channel NAME` for `channel.failing`. With none, it prints `No notifications.`, or `No open problems.` with `--open`. When there are more than `--limit`, it ends with `These are the newest 50. Raise --limit to see more.`

#### Errors

1. `Machine "apps" doesn't exist.`
2. `--limit must be from 1 to 500. Run "tender notifications list --help" for usage.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/notifications` and `GET /v1/machines/{machine}/notifications` | `list_notifications` |

## `tender tokens`

Make, list and delete the API tokens that people and AI clients send to the hub.

### `tender tokens list`

Lists the API tokens, when each was last used and when it was made. The tokens themselves are never shown again.

#### Usage

```sh
tender tokens list [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender tokens list
```

```text
NAME          LAST USED   CREATED
claude-code   never       just now
laptop        just now    2 hours ago
```

#### Errors

1. `the hub at URL needs an API token. ...` without one.

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/tokens` | `list_tokens` |

### `tender tokens add`

Makes an API token and prints it once.

As root without a token, such as `sudo tender tokens add laptop` on the hub machine, it writes to the hub's database directly, which is how you make a token when you have none. `sudo` leaves out `TENDER_TOKEN` and your token file, so this works even when you have one. Otherwise it asks the hub, with the token you have.

#### Usage

```sh
tender tokens add NAME [--hub URL]
sudo tender tokens add NAME [--database PATH]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The token's name: lowercase letters, digits and hyphens, starting with a letter or digit, up to 63 characters, such as `laptop` or `claude-code`. |
| `--hub` | URL | No | The hub's address, when it asks the hub. |
| `--database` | path | No | The hub's database, when it writes to it directly. The default is `TENDER_DATABASE`, else the one in `/etc/tender/hub.env`, else `/var/lib/tender/hub/hub.db`. |

#### Example

```sh
sudo tender tokens add laptop
```

```text
Made API token laptop.

The token, shown only now. Save it before you go on:
  tender_<43 characters>

Use it from the tender command:
  export TENDER_TOKEN=tender_<43 characters>

Or save it for the tender command on another machine:
  mkdir -p ~/.config/tender && (umask 077; printf %s 'tender_<43 characters>' > ~/.config/tender/token)
```

Run the last line as yourself, without `sudo`, on the machine you'll use the token from.

#### Errors

1. `Token names are lowercase letters, digits and hyphens, up to 63 characters, such as laptop or claude-code.`
2. `Token "laptop" already exists. Choose another name, or remove it first with: tender tokens remove laptop`
3. `there is no hub database at /var/lib/tender/hub/hub.db, so this is not the hub machine, or the hub has not run yet. ...` as root on another machine. Set `TENDER_TOKEN` there instead.
4. `the database at PATH has no current schema. Start the hub once on this version, with "sudo systemctl restart tenderhub", then run this again: ...`
5. `--database works only as root, without a token, on the hub machine. ...`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `POST /v1/tokens` | None, so an AI client can't make itself more access. |

### `tender tokens remove`

Deletes an API token. Every client that sends it is refused from its next request on.

> [!WARNING]
> Deleting the last token locks everyone out until you make a new one with `sudo tender tokens add NAME` on the hub machine. The daemons keep working.

#### Usage

```sh
tender tokens remove NAME --yes [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `NAME` | string | Yes | The token to delete. |
| `--yes` | flag | Yes | Confirms the removal. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender tokens remove claude-code --yes
```

```text
Deleted API token claude-code. Clients that send it are refused.
```

#### Errors

1. `this deletes the API token claude-code, and every client that uses it is refused. Run "tender tokens remove claude-code --yes" to confirm` without `--yes`.
2. `Token "claude-code" doesn't exist. List the tokens with: tender tokens list`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `DELETE /v1/tokens/{token}` | `delete_token` |

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
Saved your API token owner in /home/you/.config/tender/token, where the tender command finds it.
The hub is running at https://apps.your-tailnet.ts.net.
The tender command on this machine finds it by itself.
Anywhere else, set TENDER_HUB=https://apps.your-tailnet.ts.net or pass --hub https://apps.your-tailnet.ts.net.
```

The install makes the API token `owner`, or `owner-2` and up when that name is taken, once the hub has made its database, and saves it in `~/.config/tender/token` of `SUDO_USER`, or root's without it: the folder mode `0700`, the file `0600`, both that user's. It never prints the token. A token file already there stays, and no token is made. When it can't make one, it says why and how to make it by hand, and the install goes on. The output goes on with the next steps: add a destination, a machine, its schedule and a channel. A run that changes nothing prints `tenderhub is already installed and up to date. Nothing to change.`

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

On the hub's machine, an update to v0.4 or later of a hub with no API token ends with one more line: `From v0.4 the hub refuses every request from people and AI clients until it has an API token; make the first one on this machine with: sudo tender tokens add NAME.`

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

## `tender exposures`

Sets up how services' routes reach people. Only `cloudflare` routes need settings: the hub's Cloudflare account and API token. [Exposure](exposure.md#cloudflare) explains them.

### `tender exposures cloudflare show`

Shows the Cloudflare account that services' `cloudflare` routes use. The API token is never shown.

#### Usage

```sh
tender exposures cloudflare show [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender exposures cloudflare show
```

```text
Cloudflare account: 0123456789abcdef0123456789abcdef
API token: set, hidden
Updated 2 hours ago.
```

#### Errors

1. `Cloudflare isn't set up on this hub. Set it up with: tender exposures cloudflare set --account-id ID --api-token-file PATH`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `GET /v1/exposures/cloudflare` | `get_cloudflare_settings` |

### `tender exposures cloudflare set`

Sets the Cloudflare account and API token. It asks for the token with typing hidden, or reads it from `--api-token-file`. The hub checks the token with Cloudflare before it stores it, and replaces the stored settings.

#### Usage

```sh
tender exposures cloudflare set --account-id ID [--api-token-file PATH] [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--account-id` | string | Yes | The Cloudflare account ID, 32 lowercase hex digits, from the dashboard's Account home. |
| `--api-token-file` | path | No | A file whose first line is the API token. Without it, the command asks for the token. |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender exposures cloudflare set --account-id 0123456789abcdef0123456789abcdef --api-token-file cloudflare-token
```

```text
Saved the Cloudflare settings. Cloudflare accepted the API token for account 0123456789abcdef0123456789abcdef.
```

#### Errors

1. `exposures cloudflare set needs --account-id. Run "tender exposures cloudflare set --help" for usage.`
2. `there is no terminal to ask for the Cloudflare API token. Save it in a file and add --api-token-file PATH.`
3. `Cloudflare refused GET /accounts/.../cfd_tunnel: 403 Forbidden, Authentication error (10000). Check the account ID, and that the API token has Account Cloudflare Tunnel Edit, Zone DNS Edit and Zone Read.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `PUT /v1/exposures/cloudflare` | `set_cloudflare_settings` |

### `tender exposures cloudflare remove`

Removes the hub's Cloudflare settings. It refuses while a service has a `cloudflare` route. The tunnels and DNS records already in Cloudflare stay.

#### Usage

```sh
tender exposures cloudflare remove [--hub URL]
```

#### Options

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `--hub` | URL | No | The hub's address. |

#### Example

```sh
tender exposures cloudflare remove
```

```text
Removed the Cloudflare settings. The tunnels and DNS records in Cloudflare stay.
```

#### Errors

1. `Cloudflare isn't set up on this hub. There is nothing to remove.`
2. `2 cloudflare routes need these settings. Take the routes off their services first, or redeploy them with another expose.`

#### Equivalents

| REST route | MCP tool |
| --- | --- |
| `DELETE /v1/exposures/cloudflare` | `remove_cloudflare_settings` |

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

It reaches Docker at `/var/run/docker.sock`, or at the socket in `TENDER_DOCKER_SOCKET`, such as Podman's `/run/podman/podman.sock`. See [Podman](services.md#podman).

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

Run it as the `tender` user, which owns `/var/lib/tender/hub`. The hub must have run once on the same version, so the database has the current schema. The tools that store destination keys need `TENDER_ENCRYPTION_KEY`, and `send_test_notification` needs the `TENDER_SMTP_` variables for email channels. [Connect an AI client](guides/connect-an-ai-client.md#connect-over-stdio-on-the-hub-machine) explains what `sudo` and root change.

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
