# Notifications

The hub checks every machine once a minute and tells your channels, such as an email address, Slack or a webhook, when a backup fails, a machine goes quiet, a disk fills up, a service has trouble, logs stop reaching a sink, a security check finds a hole or an update of Tender fails.

This page explains how notifications work and lists every event the hub can send. To get them, see [Set up email alerts](guides/set-up-email-alerts.md), or [Adapters](adapters.md) for Slack, webhooks and your own programs.

## How a notification works

A notification is one message. The hub adds it once and never reopens it. Each has an event, a severity, a message and a status.

The event is a dotted name, like Stripe's events. A problem adds up to three notifications:

1. `<area>.<condition>` when it starts, such as `service.down`.
2. `<area>.<condition>.escalated` the first time it gets worse, such as a disk going from 90% to 95% full. Later rises add nothing, and a fall adds nothing.
3. `<area>.<condition>.resolved` when it clears. It goes only to the channels that got the problem's first or escalated notification.

The severity is `info`, `warning` or `critical`. The status is `pending` until a channel delivers it, `sent` once one did, with `sent_at` set, or `failed` once every channel's delivery of it failed. A notification that no channel could get, because there was none or the only ones were email while email was off, stays `pending` while its problem is open, and goes to the first channel you add. Once its problem resolves, it is `failed`.

A problem is open while its latest notification is not a `.resolved` one. The hub tells problems apart by event, machine, service and channel: two stopped services on one machine are two problems. List the open problems with `tender notifications list --open`, `GET /v1/notifications?open=true` or the `list_notifications` tool with `open`. `tender status` shows them too.

Each channel gets a notification when its `events` take the event and the notification is at least as severe as the channel's `min_severity`. `events` is a list of patterns: `*` for every event, an area such as `service.*`, or one event such as `backup.failed`. The default is `["*"]`. An escalated or resolved notification follows its base event, so `service.*` takes `service.down.resolved` too.

Each channel has its own delivery of each notification, which `GET /v1/notifications` shows with it. A failed delivery is tried again after 1, 2, 5, 10 and 30 minutes, then every 30 minutes, for up to 24 hours. A `4xx` answer other than `429` fails at once. One failing channel never delays or repeats another, and it doesn't keep a problem open: the problem resolves when it is gone. [How delivery works](adapters.md#how-delivery-works) has the details.

Notifications are recorded even when there are no channels, so `tender status`, `GET /metrics` and the API still show them. The hub keeps every notification, and the deliveries of a test or of a resolved problem for 30 days.

## Events

Every event the hub can send is in this table. A new event can't be added without a row here: a test checks it.

| Event | Severity | Fires when | Resolves when |
| --- | --- | --- | --- |
| `machine.silent` | `critical` | The machine's daemon has sent nothing for 10 minutes, or never reported since the machine was added. | The daemon reports again. |
| `backup.failed` | `critical` | The machine's latest finished backup failed. | A later backup completes. |
| `backup.stale` | `warning`, then `critical` | No backup has completed for 26 hours, counted from the start of the newest completed backup, or from when you set the schedule if none has. It escalates after 50 hours. | A backup completes. |
| `disk.full` | `warning`, then `critical` | The machine's root filesystem `/` is at least 90% full. It escalates at 95%. | The disk is below 90% full. |
| `service.down` | `critical` | Two reports in a row find a service exited or dead that should be running. | A report finds it running, its notifications are turned off, or it is gone. |
| `service.restarting` | `critical` | Two reports in a row find a service restarting, or Docker restarted it again. | A report finds it settled, its notifications are turned off, or it is gone. |
| `service.unhealthy` | `warning` | Two reports in a row find a service's health check failing. | A report finds it healthy, its notifications are turned off, or it is gone. |
| `service.memory_high` | `warning` | Two reports in a row find a service using more than 90% of its container's memory limit. | A report finds it below, its notifications are turned off, or it is gone. |
| `docker.unreachable` | `critical` | Two reports in a row say the daemon can't reach Docker, or the machine still reports usage but its daemon has sent no services report for 5 minutes after it had sent one. | A report reaches Docker again, or says Docker is turned off on the machine. |
| `logs.shipping_failing` | `warning` | Sending a machine's logs to an enabled sink has failed for 15 minutes, or the daemon dropped records since its last send that worked. | Every sink in it takes a send again. |
| `security.firewall_off` | `critical` | Two security reports in a row find no active firewall that denies incoming traffic by default: ufw is inactive or allows by default, or without ufw no nftables or iptables input policy drops. | A report finds a firewall that denies by default. |
| `security.port_exposed` | `warning` | Two security reports in a row find a TCP or UDP port listening on an address other than loopback, link-local or the tailnet, and not on the machine's allowlist. Each port is its own problem, with the port, such as `8080/tcp`, as its `service`. | A report finds the port closed or on loopback or the tailnet, or it is added to the allowlist. |
| `security.ssh_weak` | `warning` | Two security reports in a row find `sshd -T` saying `PasswordAuthentication yes`, so anyone can try a password, and with `PermitRootLogin yes` root too. Root login with a key only is not weak. | A report finds password login off. |
| `security.updates_pending` | `warning` | Security updates have been pending for more than 3 days, so unattended-upgrades isn't installing them. | A report finds none pending. |
| `security.auto_updates_off` | `warning` | Two security reports in a row find unattended-upgrades not installed, or `APT::Periodic::Unattended-Upgrade` off. | A report finds it on. |
| `security.reboot_required` | `warning` | `/var/run/reboot-required` is more than 7 days old. Tender never reboots a machine. | A report finds no reboot required. |
| `update.failed` | `warning` | An update of Tender on the machine failed and rolled back. The message says the version and the reason. | The machine reports that version or a newer one, after a later rollout. |
| `channel.failing` | `warning` | Every try to deliver to an enabled channel has failed for 1 hour. | A delivery or a test to the channel gets through, or it is turned off or removed. |
| `notification.test` | `info` | You send a test with `tender channels test`, `POST /v1/notifications/test` or `send_test_notification`. | Never: a test is a single message. |

`backup.failed` and `backup.stale` apply only to machines with backup settings.

While a machine is silent, its Docker, its services, its log shipping, its security checks and its updates add nothing, and their open problems stay as they are, because the hub can't tell what is happening on it. Its backup and disk problems still start and resolve from what the hub last heard.

## Service events

The daemon reports every Docker container once a minute. A service problem starts only after two reports in a row show it, so a container that restarts once during a deploy doesn't page anyone.

A service should be running when it exited with an error, or when its restart policy is `always` or `unless-stopped`. A container that exits 0 with the restart policy `no` or `on-failure`, such as a migration job, finished on purpose and adds nothing. A service you stop with Tender adds nothing either, until something starts it again.

`service.memory_high` compares a service's memory with its container's limit only. A container without a memory limit never gets this event. Set a limit with `mem_limit` in Compose or `--memory` in `docker run` for the services you want watched. A report without a running service's stats, as [Services](services.md#what-a-service-is) explains, neither resolves this problem nor starts its count again.

`docker.unreachable` says why in words, from the report's `docker_reason`, such as `Docker on apps can't be reached: its socket refuses connections, so Docker is stopped.` [Services](services.md#when-docker-cant-be-reached) lists the reasons. While Docker is unreachable, the machine's other service problems neither start nor resolve. The count of reports in a row starts again once Docker answers.

A removed service's problems resolve, because its counts start again. See [Removed services](services.md#removed-services).

## Log shipping events

A machine has one `logs.shipping_failing` problem for all its sinks. Its message names each failing sink and why, such as `Log shipping from apps to otel has failed since 2026-11-15 12:04 UTC (16 minutes): the sink answered 503 Service Unavailable. It resolves after a send to the sink works.` It never holds a log line. [Log shipping](log-shipping.md#when-a-sink-is-down) explains when a daemon drops records.

## Security events

The daemon runs its [security checks](security.md#security-checks) every 15 minutes. Most security problems start only when two reports in a row find them, so a change you are in the middle of doesn't page anyone. A check that couldn't run, or that says `unsupported` on a system other than Debian or Ubuntu, never starts or resolves a problem. Turning a machine's security notifications off resolves its open security problems.

Silence a port that should be reachable, such as Tailscale's, with the machine's allowlist, or turn a machine's security notifications off:

```sh
tender machines set apps --allow-port 41641/udp
tender machines set lab --security-alerts off
```

Install pending security updates with `tender machines upgrade apps`. [Security](security.md#upgrade-a-machines-packages) explains it.

## Update events

`update.failed` says the version and why, such as that the new version failed its health check and rolled back. [Updates](updates.md#when-an-update-fails) explains what to do.

## Channel events

`channel.failing` goes to every other enabled channel whose `events` take it, whatever its `min_severity`, and never to the failing one. Its machine is `null` and its `channel` names the failing channel. Only real tries count: a delivery dropped because its problem resolved or its channel was turned off doesn't, and neither does an email test while email is off. Turning a channel off and on again starts its count again.

## Turn a service's notifications off

Each service has its own notifications. Turn them off for a service you expect to stop, such as a worker you run by hand:

```sh
tender services alerts apps web off
```

```text
Alerts for web on apps are off. Its open problems resolve within a minute.
```

This works even while Docker is unreachable. Turn them back on with `on`.

## Next steps

1. [Set up email alerts](guides/set-up-email-alerts.md) to send these to your team.
2. [Adapters](adapters.md) sends them to Slack, a webhook or your own program.
3. [Services](services.md) explains what the daemon reports about each container.
4. [API reference](api.md#notifications) lists the notification routes.
