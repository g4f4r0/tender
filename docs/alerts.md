# Alerts

The hub checks every machine once a minute and emails your recipients when a backup fails, a machine goes quiet, a disk fills up or a service has trouble.

This page explains each alert and when it opens and resolves. To get the emails, see [Set up email alerts](guides/set-up-email-alerts.md).

## How an alert works

An alert is a notification with a type, a severity and a status. The severity is `info`, `warning` or `critical`. The status is `pending` until the email goes out, `sent` while the problem lasts, and `resolved` once it is gone.

You get at most three emails for one problem:

1. One when the alert opens.
2. One if it gets worse, such as a disk going from 90% to 95% full.
3. One when it resolves.

If a send fails, the hub keeps retrying until the email goes out. Alerts are recorded even when there are no recipients, so `tender status` and the API still show them.

## Machine and backup alerts

| Type | Opens when | Severity |
| --- | --- | --- |
| `machine_silent` | The machine hasn't reported for 10 minutes. | `critical` |
| `backup_failed` | The machine's latest finished backup failed. | `critical` |
| `backup_stale` | The machine has no completed backup for 26 hours. | `warning`, then `critical` after 50 hours |
| `disk_full` | A disk is at least 90% full. | `warning`, then `critical` at 95% |

A silent machine alerts once, as `machine_silent`. Its Docker and its services don't alert while it is silent, because the hub can't tell what is happening on it.

## Service alerts

The daemon reports every Docker container once a minute. A service alert opens only after two reports in a row show the problem, so a container that restarts once during a deploy doesn't page anyone.

| Type | Opens when | Severity |
| --- | --- | --- |
| `service_down` | The service is exited or dead and should be running. | `critical` |
| `service_restarting` | The service is restarting, or Docker restarted it again. | `critical` |
| `service_unhealthy` | The service's health check fails. | `warning` |
| `service_memory_high` | The service uses more than 90% of its container's memory limit. | `warning` |
| `docker_unreachable` | Docker refuses connections, doesn't answer its ping within 5 seconds, or its socket went away after Tender saw it. | `critical` |

A service should be running when it exited with an error, or when its restart policy is `always` or `unless-stopped`. A container that exits 0 with the restart policy `no` or `on-failure`, such as a migration job, finished on purpose and doesn't alert. A service you stop with Tender doesn't alert either, until something starts it again.

`service_memory_high` compares a service's memory with its container's limit only. A container without a memory limit never gets this alert, because the machine's report has no total to compare it with. Set a limit with `mem_limit` in Compose or `--memory` in `docker run` for the services you want watched.

While Docker is unreachable, the machine's other service alerts neither open nor resolve. The count of reports in a row starts again once Docker answers.

## Turn a service's alerts off

Each service has its own alerts. Turn them off for a service you expect to stop, such as a worker you run by hand:

```sh
tender services alerts apps web off
```

Turning them off resolves the service's open alerts, even while Docker is unreachable. Turn them back on with `on`.

## Next steps

1. [Set up email alerts](guides/set-up-email-alerts.md) to send these to your team.
2. [Services](services.md) explains what the daemon reports about each container.
3. [API reference](api.md#notifications) lists the notification routes.
