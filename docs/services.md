# Services

Tender watches every Docker container on every machine, and deploys services from docker compose files. It lists each service with its containers' state, health, memory and CPU, starts, stops and restarts them, and reads their logs.

This page explains what a service is, how Tender deploys one, what the daemon reports and how actions reach a machine. To make a service reachable, see [Exposure](exposure.md). To restart one or read its logs, see [Control a service](guides/control-a-service.md).

## What a service is

A service is a compose project on a machine, such as `notes`, made of containers. A container without a compose project is a service of its own, named after the container, such as `redis`.

Each container is named in its service by its compose service, such as `web`, with `-2` and up for the second and later replicas. A container without the compose labels that say so is named by its container name less the project's prefix and a `-1` suffix, so `notes-web-1` is `web`. Two containers that would get one name are told apart by the start of their container id. Messages name a container in its service as `web in notes on apps`.

A service is managed or unmanaged:

1. Tender deployed a managed service from its compose file, through the services API, MCP or `tender services deploy`. The hub keeps its compose file, files, env and routes, and can deploy it again, change it and remove it.
2. Tender only found an unmanaged service running, such as one started with `docker compose up` before Tender. Tender watches, starts, stops and restarts it, and reads its logs. Changing it, deploying it again or removing it answers `409`.

A service's `state` comes from its containers:

| `state` | When |
| --- | --- |
| `running` | Every container that should run does. |
| `degraded` | Some of them run. |
| `stopped` | None of them run. |
| `unknown` | The machine's Docker is unreachable. |

A container that exited `0` and that Docker wouldn't restart, such as a migration with the restart policy `no`, finished its job, so it doesn't count. A removed container doesn't count either.

## What the daemon reports

The daemon reads the containers from Docker's API once a minute and sends the whole list to the hub, `[]` when there are none. A machine without Docker has no services; its backups and usage reports work the same.

For each container the hub keeps the latest report only:

| Field | What it holds |
| --- | --- |
| `runtime` | What runs it. It is `docker`, the one runtime in this version, also for Podman. |
| `state` | `created`, `running`, `paused`, `restarting`, `exited`, `removing` or `dead`, as Docker says. The hub sets `unknown` while Docker is unreachable, and `removed` once Docker no longer has the container. |
| `health` | `healthy`, `unhealthy` or `starting`, or `null` for a container without a health check. |
| `exit_code` | The exit code while the container is exited or dead, else `null`. It is `null` while Docker is unreachable too. |
| `restart_count` and `restart_policy` | How often Docker restarted it, and its policy: `no`, `always`, `unless-stopped` or `on-failure`. |
| `memory_bytes` | The memory it uses, less the page cache the kernel can drop, as `docker stats` shows it. |
| `memory_limit_bytes` | The container's memory limit, or `null` without one. |
| `cpu_percent` | Its CPU use since the previous report, where 100 is one whole CPU. |
| `stopped_by_tender` | `true` when a Tender stop left it stopped, so it doesn't alert as down. |
| `removed_at` | When the first report without the container arrived, while the state is `removed`, else `null`. |

Memory and CPU are `null` for a stopped container. CPU is also `null` on a container's first report, after it restarts or is made again, and after the daemon itself restarts, because the daemon computes it from its own previous reading instead of waiting a second for a new one.

When the daemon can't read a running container's stats in time, it sends the container with `stats_unavailable: true` and no usage. Its usage is unknown this time, not gone, so the hub keeps the last `memory_bytes` and `memory_limit_bytes` while the `container_id` is the same, and `cpu_percent` is `null`.

`alerts_enabled` is one switch on the service for all its containers. It is `true` for every new service, and stays when its containers are made again.

### Removed containers

When a report no longer has a container, the hub keeps it with the state `removed` and sets `removed_at`. Its counts of reports in a row start again, so its open problems resolve. A start, stop, restart or logs action on it gets `409` with `Container worker in notes was removed: Docker on this machine no longer has it.`

A container back under the same name takes its row back. The hub deletes a container 7 days after `removed_at`, and an unmanaged service with its last container.

## Deploy a service

Tender deploys a service from a docker compose file, through the daemon on the machine. Tender owns the networking, the data folders and the exposure, so every service on every machine follows one pattern, and an agent deploys through Tender's API or MCP instead of getting `sudo` or the Docker socket.

```sh
tender services deploy apps notes -f compose.yaml --env-file .env --route web:8080:tailscale
```

A service's name is its compose project name: lowercase letters, digits and dashes, starting with a letter and not ending with a dash, at most 32. It can't be `tender`, the project of Tender's own containers. With the compose file come:

1. `files`, the config files the compose file mounts, by their path relative to it, such as `config/app.toml`. A service has at most 64 files and 1 MiB in all.
2. `env`, the values for the compose file's `${KEY}` interpolation. Keys are capital letters, digits and underscores, and can't start with `COMPOSE_` or `DOCKER_`, which compose reads from `.env` as its own settings. The hub stores the values encrypted and shows each as `"***"`. A value can't hold a single quote or a line break.
3. `routes`, which make a container's port reachable. See [Exposure](exposure.md).

### What the daemon does

On the machine each managed service has a folder, `/var/lib/tender/services/<name>/`, owned by root with mode `0700`. It holds `compose.yaml`, the files, `.env` with mode `0600`, and `volumes/`. The folder is outside `/var/lib/docker`, so the whole-server backup has every service's data.

1. The daemon writes the folder, each file atomically, and removes the files of the last deploy that the service no longer has.
2. It runs `docker compose -p <name> -f compose.yaml config --format json` and checks the result against [the rules](#the-rules). A rule that fails fails the deploy with a sentence that names the service in the compose file and how to fix it.
3. It writes `compose.tender.yaml`, an override that:
   1. publishes each routed container's port on `127.0.0.1:<local_port>` only;
   2. puts the label `dev.tender.service=<name>` on every container;
   3. sets `restart: unless-stopped` where the compose file sets no restart policy;
   4. binds every named volume to `volumes/<volume>` in the folder, with `driver: local` and `driver_opts: {type: none, o: bind, device: ...}`, so its data is backed up and it works as a named volume does.
4. It writes the config it checked to `compose.checked.json` in a temporary `DOCKER_CONFIG` folder of the deploy's own, outside the service's folder, and deletes the folder afterwards. Pull and up run on that file and the override, never on `compose.yaml` again: `docker compose -p <name> --project-directory <folder> -f <checked> -f compose.tender.yaml`. It pulls the images with the logins of their registries, from that folder.
5. Before up, it lists the containers of the service's Compose project that aren't exited or dead, orphans of the old config too, with `docker ps --all --filter label=com.docker.compose.project=NAME`, and reads their read-write binds with `docker inspect`. When a path the new config binds or reads is in one of them, it stops those containers first with `docker stop`, and the action's output says so; otherwise they run until up recreates them. Then it walks each path in the folder that compose binds or reads, a part at a time, and fails the deploy at a symbolic link or anything that isn't a plain folder or file. Then it runs `up -d --remove-orphans --wait --wait-timeout 300`.
6. It applies the machine's exposures, as [Exposure](exposure.md) describes.

The action's output keeps the last 64 KiB of compose's output. The daemon gives a deploy 40 minutes, and the CLI waits for it; MCP waits 60 seconds and returns the action as it is.

A failed deploy sets `deploy_status` to `failed`, with `last_error` holding why, and sends a `service.deploy_failed` alert while the service's alerts are on. It leaves the containers as compose left them; there is no automatic rollback. To go back, deploy the last good compose file again. The next deploy that succeeds resolves the alert.

`deploy_status` is `deploying` while a deploy waits or runs, then `deployed` or `failed`, and `removing` while a remove does. It is `null` for an unmanaged service.

### The rules

The daemon checks the rendered config of your compose file, not Tender's override. Every key in it must be one Tender allows, at the top, on a service, on a volume, network, secret or config, and in a mount, `deploy` or a service's network; keys starting `x-` are always allowed. A key that isn't allowed fails the deploy with a sentence that names it. A key compose writes with its default, such as `"entrypoint": null`, an empty `{}` or `[]`, `false` or `0`, sets nothing and passes, whatever its name. On top of that:

1. Images only: `build` is refused. Build the image elsewhere and push it to a registry.
2. No `ports`: routes publish them.
3. `network_mode` may be `none`, `bridge`, `default` or `service:<name>` of a service in the same file. `ipc` may be `none`, `private`, `shareable` or `service:<name>`, and `pid` only `service:<name>`. `container:<name>` is refused: it joins any container's namespaces, such as one on the machine's own network.
4. No `privileged` and no `devices`, in `deploy.resources.reservations` too. `security_opt` may only be `no-new-privileges` or `no-new-privileges:true`, because another, such as a seccomp profile, could turn a protection off. `cap_add` may only add back capabilities Docker gives every container anyway: `NET_BIND_SERVICE`, `CHOWN`, `SETUID`, `SETGID`, `FOWNER`, `DAC_OVERRIDE`, `KILL`, `SETPCAP` and `NET_RAW`, such as after `cap_drop: ALL`. Only `net.*` sysctls, and only the `json-file`, `local`, `journald` and `none` log drivers.
5. A bind mount's source must be inside the service's folder, a relative path in the file, and no mount may name the Docker socket. It can't be the folder itself or what Tender keeps there: `compose.yaml`, `compose.tender.yaml`, `.env`, `.tender-files.json` and `volumes/`. A mount is `bind`, `volume` or `tmpfs`, without propagation other than `private`. `env_file`, `secrets` and `configs` files must be inside the folder too.
6. Named volumes may not be `external` or set their own `driver` or `driver_opts`; their names are letters, digits, dots, dashes and underscores. Networks may not be `external`, are bridge networks, and can't set `ipam`'s `config`, `options` or a `driver` other than `default`, since a chosen subnet could overlap the machine's network or Tailscale's `100.64.0.0/10`. The empty `ipam: {}` that compose writes on every network passes. Neither may set a `name` other than compose's own, `<name>_<key>`, which could be another service's.
7. No `container_name`.
8. Every compose service has a memory limit above zero, `mem_limit` or `deploy.resources.limits.memory`, so the memory-high alert works. `0` and `-1` mean no limit, and are refused.

No container can make a symbolic link between the check and up: one that can write where the new config binds or reads is stopped before the check. The folder rule, with binds kept off what Tender keeps in the folder, and pull and up running on the config Tender checked, guard the rest.

The common keys work: several services, health checks, `depends_on`, profiles, anchors, `env_file`, `command`, `environment`, `labels`, `user`, limits and reservations, `ulimits`, `tmpfs`, internal networks and the rest. For example:

```text
service web in the compose file publishes ports, which Tender's routes do instead: remove ports from web and add a route to its port, such as --route web:80:tailscale
```

### Change, redeploy and remove

1. `tender services deploy` on a service that exists changes it. The compose file is replaced, and the files, env and routes are replaced when their options are given and kept when not. `PATCH /v1/machines/{machine}/services/{service}` merges `files` and `env`, with `null` removing a key, and replaces `routes`.
2. `tender services redeploy` deploys the service again as stored. Compose pulls the images again, so a new image under the same tag runs.
3. `tender services remove MACHINE SERVICE --yes` runs `docker compose -p <name> down --remove-orphans`, takes the service's routes out of the exposures, and the hub forgets the service once that succeeded. The folder, with its volumes' data, moves to `/var/lib/tender/services/.removed/<name>-<unix time>`. Delete it when you no longer need it with `sudo rm -rf /var/lib/tender/services/.removed/<name>-<unix time>`. With `--delete-data`, the daemon deletes the folder instead, which can't be undone.

A name that an unmanaged service already has can't be deployed: take its containers down with `docker compose -p <name> down` first, or give the new service another name.

### Registries

A deploy pulls private images with the login of the registry whose host the image is on, such as `ghcr.io`. Add one with `tender registries add ghcr --host ghcr.io --username me`, which asks for the password or access token. The hub stores the password encrypted, shows it as `"***"` with `password_set: true`, and gives it only to a daemon that deploys a service. Images without a host, such as `nginx`, are on `docker.io`.

## When Docker can't be reached

When the daemon can't reach Docker, it reports `{"docker": "unreachable"}` with no list and a `docker_reason`:

| `docker_reason` | What the daemon found |
| --- | --- |
| `socket_missing` | `/var/run/docker.sock` is gone, so Docker was stopped or removed. |
| `permission_denied` | The daemon may not use the socket, such as on a daemon before v0.3.0 when Docker was installed after Tender. Update the machine, and the daemon runs as root. See [The daemon runs as root](install.md#the-daemon-runs-as-root). |
| `refused` | The socket refuses connections, so Docker is stopped. |
| `timeout` | Docker didn't answer within 5 seconds. |
| `error` | Anything else. |

Until the daemon has seen Docker answer on this machine, only a socket that refuses connections or doesn't answer counts as unreachable; a missing socket or any other failure means the machine has no Docker, and the daemon reports nothing. The daemon remembers that it saw Docker in the file `docker-seen` in its state folder, `/var/lib/tender/daemon`, so a restart doesn't forget it.

The hub keeps the last list and sets the `state` of every container that isn't removed to `unknown`, with `exit_code`, memory and CPU `null`, until Docker answers again. `GET /v1/services`, `GET /v1/machines/{machine}/services` and `list_services` return `docker_reason`, which is `null` while Docker runs. [Notifications](notifications.md#service-events) explains the `docker.unreachable` event.

### When Docker is turned off

Docker that you turned off on purpose is not an outage. When the socket is missing or refuses connections, the daemon asks systemd whether Docker is wanted, with `systemctl is-enabled docker.service docker.socket`. When each unit is `disabled`, `masked` or `not-found`, Docker is turned off, and the daemon reports `{"docker": "off"}` with no list and no reason. It does so whether or not it saw Docker before, and logs once:

```text
Docker is turned off on this machine (docker.service and docker.socket aren't enabled), so Tender doesn't watch its services.
```

The hub then forgets the machine's Docker, as for a machine without it:

1. `docker` and `docker_reason` are `null` in `GET /v1/services`, `GET /v1/machines/{machine}/services` and `list_services`.
2. Every container that isn't removed becomes `removed`, as [Removed containers](#removed-containers) describes, so its open problems resolve.
3. An open `docker.unreachable` resolves, and the 5-minute rule for a daemon that stops sending services reports no longer applies.

`tender status` shows `-` for the machine's services. Any other answer from systemd, such as `enabled` or `static` for either unit, means Docker is wanted, so a refusing socket is still `unreachable`. When `systemctl` is missing, fails or doesn't answer within 5 seconds, the daemon reports as if Docker were wanted. Turn Docker back on, and the next report with its containers takes the services back.

## How actions reach a machine

The hub never connects to a machine. Each daemon keeps one request open to the hub and waits for work, so actions work through NAT and firewalls without opening a port.

1. You ask for an action, such as a restart, with the CLI, the API or MCP.
2. The hub queues it with the status `pending` and answers `202 Accepted`.
3. The daemon's open request gets the machine's oldest pending action, usually within a second. The action stays `pending`.
4. The daemon claims it with `PATCH {"status": "in_progress"}`, and runs it only when the hub answers `204`. The hub hands an action that isn't claimed within 30 seconds out again, and answers `409` to a claim of an action that isn't pending.
5. The daemon runs it, one action at a time: a deploy or remove with docker compose, a start, stop or restart of a managed service with docker compose in its folder, and of one container or an unmanaged service through Docker's API, and logs through Docker's API or `journalctl`.
6. The daemon sends back `completed` or `failed`, with the output or the reason.

A start, stop, restart or logs action takes an optional `container`, which narrows it to one container of the service. The logs of several containers are merged by time, each line with its container's name after the timestamp, such as `2026-11-15T10:00:00Z web | listening on :8080`.

The CLI and MCP wait up to 60 seconds for the result of a start, stop, restart or logs. A `pending` action fails if the daemon hasn't claimed it 15 minutes after it was asked for, and an `in_progress` one fails if the daemon hasn't said how it ended within 15 minutes, or 45 minutes for a deploy or remove. The hub keeps each machine's newest 100 finished actions, and never deletes a `pending` or `in_progress` one.

After a start, stop, restart, deploy or remove, the daemon reports its containers at once, so the list shows the new state without waiting for the next minute.

### When the hub or the daemon restarts

The daemon retries a claim for up to 25 seconds with one `Idempotency-Key`, so a claim whose answer was lost gets the same answer again and the action runs once. If no answer comes, the daemon doesn't run the action and reports it failed, with `The daemon could not confirm with the hub that it may run this, so it did not run it.`

Each report waits in the daemon's state folder, as one JSON file per action under `/var/lib/tender/daemon/actions/`, readable only by the daemon. The daemon sends it again until the hub has it, across hub and daemon restarts. While reports wait, the daemon polls the hub without waiting and tries them again every minute.

A daemon restart can leave a report without its result. Logs never go to disk, so they are lost too:

1. A logs report that waited across a daemon restart is sent as failed, with `The daemon restarted before the hub had these logs, and it never keeps logs on disk. Ask for them again.`
2. An action the daemon was running when it stopped is sent as failed, with `The daemon stopped while it ran this, so it can't say how it ended.`

A report that arrives after the hub failed the action with `the daemon started this but did not say how it ended; is the machine online?` replaces that failure. A late completed stop still marks the containers `stopped_by_tender`.

## Logs

There are two kinds of logs:

1. A service's logs, which the daemon reads from Docker, for all its containers or one.
2. The machine's systemd journal, which the daemon reads with `journalctl`. Filter it by unit, priority and time.

Both return the newest entries, up to 2000 lines and 256 KiB. When the output is larger, the oldest lines are dropped.

> [!WARNING]
> Logs can hold secrets, such as tokens an application prints at startup. The hub clears the output of every completed logs action 1 hour after it finishes. See [Security](security.md#logs-can-hold-secrets).

## Docker and journal access

The daemon runs as root, so it reaches Docker's socket and reads every unit's journal. Deploying a service is close to root on its machine: the rules keep a compose file from taking the machine, but whoever can deploy can run any image there. [Security](security.md#what-the-daemon-can-do) explains what the daemon's sandbox allows.

## Podman

The daemon talks to Docker's Engine API, and Podman serves the same API on its Docker-compatible socket. To use Podman, enable that socket and point the daemon at it:

1. Enable the socket:

   ```sh
   sudo systemctl enable --now podman.socket
   ```

2. Set the socket for the daemon, then restart it:

   ```sh
   sudo systemctl edit tenderd
   ```

   Add these lines, save, and run `sudo systemctl restart tenderd`:

   ```ini
   [Service]
   Environment=TENDER_DOCKER_SOCKET=/run/podman/podman.sock
   ```

Its containers then show `"runtime": "docker"`, because the daemon speaks Docker's API to them.

> [!WARNING]
> Podman through its Docker-compatible socket is unverified: no Tender test runs against Podman. The daemon also needs read and write access to `/run/podman/podman.sock`, which the `docker` group doesn't give. Deploying needs `docker compose` on the daemon's PATH.

## Next steps

1. [Exposure](exposure.md) makes a service's ports reachable.
2. [Control a service](guides/control-a-service.md) to restart a container or read its logs.
3. [Read a machine's logs](guides/read-machine-logs.md) to search the journal.
4. [Notifications](notifications.md#service-events) explains when a service has a problem.
