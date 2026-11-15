# Alerts

The hub checks every machine once a minute and emails your recipients when a backup fails, a machine goes quiet, a disk fills up or a service has trouble.

This page explains each alert and when it opens and resolves. To get the emails, see [Set up email alerts](guides/set-up-email-alerts.md).

## How an alert works

An alert is a notification with a type, a severity and a status. The severity is `info`, `warning` or `critical`. The status is `pending` while an email is owed, `sent` once it went out, and `resolved` once the problem is gone. With email off or no recipients, an open alert stays `pending`.

You get an email:

1. When the alert opens.
2. The first time it gets worse, such as a disk going from 90% to 95% full. The email says it is now critical. Later rises change only the alert's severity and content, and a fall sends nothing.
3. When it resolves.

If a send fails, the hub retries at its next checks, waiting longer each time up to 10 minutes, until the email goes out. An alert that was emailed stays open until its resolved email goes out. Alerts are recorded even when there are no recipients, so `tender status` and the API still show them.

## Machine and backup alerts

| Type | Opens when | Severity |
| --- | --- | --- |
| `machine_silent` | The machine hasn't reported for 10 minutes. | `critical` |
| `backup_failed` | The machine's latest finished backup failed. | `critical` |
| `backup_stale` | No backup has completed for 26 hours, counted from the start of the newest completed backup, or from when you set the schedule if none has. | `warning`, then `critical` after 50 hours |
| `disk_full` | The machine's root filesystem `/` is at least 90% full. | `warning`, then `critical` at 95% |

`backup_failed` and `backup_stale` apply only to machines with backup settings.

While a machine is silent, its Docker and its services don't alert, because the hub can't tell what is happening on it. Its backup and disk alerts still open and resolve from what the hub last heard.

## Service alerts

The daemon reports every Docker container once a minute. A service alert opens only after two reports in a row show the problem, so a container that restarts once during a deploy doesn't page anyone.

| Type | Opens when | Severity |
| --- | --- | --- |
| `service_down` | The service is exited or dead and should be running. | `critical` |
| `service_restarting` | The service is restarting, or Docker restarted it again. | `critical` |
| `service_unhealthy` | The service's health check fails. | `warning` |
| `service_memory_high` | The service uses more than 90% of its container's memory limit. | `warning` |
| `docker_unreachable` | Two reports in a row say the daemon can't reach Docker, or the machine still reports usage but its daemon has sent no services report for 5 minutes after it had sent one. | `critical` |

A service should be running when it exited with an error, or when its restart policy is `always` or `unless-stopped`. A container that exits 0 with the restart policy `no` or `on-failure`, such as a migration job, finished on purpose and doesn't alert. A service you stop with Tender doesn't alert either, until something starts it again.

`service_memory_high` compares a service's memory with its container's limit only. A container without a memory limit never gets this alert. Set a limit with `mem_limit` in Compose or `--memory` in `docker run` for the services you want watched. A report without a running service's stats, as [Services](services.md#what-a-service-is) explains, neither resolves this alert nor starts its count again.

`docker_unreachable` says why in words, from the report's `docker_reason`, such as `Docker on apps can't be reached: its socket refuses connections, so Docker is stopped.` [Services](services.md#when-docker-cant-be-reached) lists the reasons. While Docker is unreachable, the machine's other service alerts neither open nor resolve. The count of reports in a row starts again once Docker answers.

A removed service's alerts resolve, because its counts start again. See [Removed services](services.md#removed-services).

## Turn a service's alerts off

Each service has its own alerts. Turn them off for a service you expect to stop, such as a worker you run by hand:

```sh
tender services alerts apps web off
```

```text
Alerts for web on apps are off. Its open alerts resolve within a minute, or once their resolved email goes out when sending it is retried.
```

This works even while Docker is unreachable. Turn them back on with `on`.

## Next steps

1. [Set up email alerts](guides/set-up-email-alerts.md) to send these to your team.
2. [Services](services.md) explains what the daemon reports about each container.
3. [API reference](api.md#notifications) lists the notification routes.
