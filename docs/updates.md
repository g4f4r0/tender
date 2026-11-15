# Updates

Tender updates itself on every machine from the hub, one machine at a time, with no sudo after the v0.3.0 install.

## Prerequisites

1. A hub and daemons on Tender v0.3.0 or later. A machine on v0.2.2 or earlier needs one update by hand first, as [Update to v0.3.0](install.md#update-to-v030) explains.
2. Outbound HTTPS from the hub and every machine to `api.github.com` and `github.com`, where the releases are.

## How an update works

The hub asks GitHub for the latest stable release every 6 hours. A rollout updates the machines to one release, one at a time, with the hub's own machine last. On each machine:

1. The daemon starts the update job, `tender update --apply VERSION`, as the transient systemd unit `tenderupdate`. The job runs outside the daemon's sandbox and outlives the daemon's restart.
2. The job downloads the release, checks the signature of its `SHA256SUMS` against the keys built into Tender, then checks the binary against `SHA256SUMS`. A release that isn't signed by Tender's key never installs.
3. It runs the new binary's `tender version` and checks that it reads the machine's daemon config and hub settings.
4. It keeps the current binary as `/usr/local/bin/tender.previous`.
5. It waits up to 30 minutes for a running backup or action to finish, then swaps the binary and restarts the services.
6. Within 2 minutes, `tenderd` must be active and have reported to the hub on the new version. On the hub's machine, `tenderhub` must be active and answer `GET /v1/health` with `200`.
7. Otherwise it puts the previous binary back, restarts the services and reports the failure.

The hub moves to the next machine once a machine reports the new version. It stops the rollout at the first machine that fails, and opens an `update.failed` alert for it. [Alerts](alerts.md#update-alerts) explains that alert.

An update never installs a prerelease, a draft or an older version. Only root can go back to an older release, by hand on that machine, with `sudo tender update --version X.Y.Z`.

## Choose auto or manual

The hub's mode applies to every machine that doesn't have its own.

1. In `auto` mode, the default, the hub starts a rollout of each new stable release to the machines in auto mode by itself. It leaves out a machine that hasn't reported for 10 minutes, and a release whose rollout already failed.
2. In `manual` mode nothing updates until you run `tender update`.

Set the mode, and the hours in UTC when auto mode may start a rollout:

```sh
tender updates set --mode auto --window 02:00-05:00
```

```text
Updates are auto, window: 02:00-05:00 UTC.
```

The window may cross midnight, such as `22:00-02:00`. Pass `--window ""` for any time. A rollout that started inside the window finishes even after it ends.

Give one machine its own mode, or set it back to the hub's with `inherit`:

```sh
tender machines set db --updates manual
```

```text
db: updates manual, allowed ports none, security alerts on.
```

A release that needs a person, such as one with a manual migration, carries the file `tender-release.json` with `{"auto": false}`. Auto mode never rolls it out; `tender update` still can.

## Update now

1. Start a rollout of the latest release to every machine:

   ```sh
   tender update --all
   ```

   ```text
   Started rollout 3 of Tender 0.3.1, one machine at a time: db, apps.
   Follow it with: tender updates show
   ```

   Name machines instead of `--all` to update only those, and add `--version 0.3.1` for a release other than the latest.

2. Follow it:

   ```sh
   tender updates show
   ```

   ```text
   Mode: auto, window: 02:00-05:00 UTC.
   Latest release: 0.3.1, checked 2 hours ago.

   MACHINE   VERSION   MODE            UPDATE
   apps      0.3.0     auto            yes
   db        0.3.1     manual (its own) -

   Rollout 3 of 0.3.1, in progress, started 1 minute ago:
     db: completed
     apps: in progress
   ```

Each machine takes a minute or two, and longer while a backup runs.

## When an update fails

The machine rolls back to the version it ran, and the rollout stops there, so the machines after it keep their version. `tender updates show` and the `update.failed` alert give the reason, such as:

```text
Rollout 4 of 0.3.2, failed, started 12 minutes ago:
  db: failed. Tender v0.3.2 failed its health check, so it rolled back to Tender 0.3.1: after 2m0s, tenderd is not running.
  apps: pending
```

Read the job's own log on that machine with `journalctl -u tenderupdate`. Auto mode doesn't try that release again. Once you fix the cause, start a rollout by hand with `tender update`.

## Reference

### Update settings

| Name | Values | Default | What it sets |
| --- | --- | --- | --- |
| `mode` | `auto`, `manual` | `auto` | Whether the hub rolls out new releases by itself. |
| `window` | `HH:MM-HH:MM` in UTC, or `null` | `null` | When auto mode may start a rollout. `null` is any time. |
| `updates`, per machine | `inherit`, `auto`, `manual` | `inherit` | The machine's own mode, or the hub's. |

### Signed releases

The release workflow signs each release's `SHA256SUMS` with an ed25519 key, into `SHA256SUMS.sig`, the base64 of the signature. The private key lives only in a GitHub Actions secret and in the maintainer's secret store. The binary carries the public keys:

```text
FFCQetX09NSfXliwZi1eVws0iGQkNwZch0dLQTru3TI=
```

Every update checks the signature, `tender update` by hand too, from Tender v0.3.0 on. Tender v0.2.2 and earlier can't check signatures, so their update to v0.3.0 checks only the release's checksums.

### Commands, routes and tools

| Task | CLI | REST | MCP |
| --- | --- | --- | --- |
| See versions and rollouts | `tender updates show` | `GET /v1/updates` | `get_updates` |
| Change the settings | `tender updates set` | `PUT /v1/updates/settings` | `set_update_settings` |
| Start a rollout | `tender update MACHINE ...` or `--all` | `POST /v1/updates/rollouts` | `start_update` |
| Give a machine its own mode | `tender machines set NAME --updates MODE` | `PATCH /v1/machines/{machine}` | `update_machine` |
| Update this machine by hand | `sudo tender update` | None | None |

## Next steps

1. [Security](security.md) explains why the daemon runs as root and what its sandbox allows.
2. [Alerts](alerts.md#update-alerts) explains the `update.failed` alert.
3. [CLI reference](cli.md#tender-update) lists every option of `tender update`.
