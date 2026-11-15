# Metrics

The hub serves its machines, services, backups and open problems at `GET /metrics` in the Prometheus text format, for Prometheus, Grafana Agent or an OpenTelemetry collector to scrape.

This page explains how to scrape the hub and what each metric holds. Nothing is pushed: a scraper reads the page when it wants.

## Scrape the hub

`/metrics` takes the same access rule as the rest of the API: anyone who can reach the hub can read it, and no token is needed. See [Security](security.md#the-hub-has-no-login).

1. Check that the hub answers:

   ```sh
   curl -s https://apps.your-tailnet.ts.net/metrics | grep tender_build_info
   ```

   It prints the hub's version:

   ```text
   # HELP tender_build_info The hub's version, as the version label; always 1.
   # TYPE tender_build_info gauge
   tender_build_info{version="v0.3.0"} 1
   ```

2. Add the hub to Prometheus's `prometheus.yml`. A one-minute interval matches how often the daemons report:

   ```yaml
   scrape_configs:
     - job_name: tender
       scrape_interval: 1m
       scheme: https
       static_configs:
         - targets: ["apps.your-tailnet.ts.net"]
   ```

   For an OpenTelemetry collector, put the same `scrape_configs` under its `prometheus` receiver's `config`.

3. Reload Prometheus, then check that the target is up:

   ```sh
   curl -s 'http://localhost:9090/api/v1/query?query=up{job="tender"}'
   ```

   The answer holds `"value":[<time>,"1"]`.

## What the hub serves

Every metric is a gauge, read from the hub's database when it is scraped. A value Tender doesn't have, such as the usage of a machine that never reported, has no series instead of a zero. The page shows each metric's `# HELP` line too.

### Machines

| Metric | Labels | What it holds |
| --- | --- | --- |
| `tender_build_info` | `version` | Always `1`. The label is the hub's version. |
| `tender_machine_last_seen_timestamp_seconds` | `machine` | When the daemon measured its latest reading, in Unix seconds. |
| `tender_machine_cpu_load` | `machine` | The 1-minute load average. |
| `tender_machine_memory_used_percent` | `machine` | Memory in use, from 0 to 100. |
| `tender_machine_disk_used_percent` | `machine` | Space in use on the root file system `/`, from 0 to 100. |
| `tender_machine_uptime_seconds` | `machine` | How long the machine had been up. |

The machine numbers come from its latest reading, which the daemon sends once a minute.

### Services

| Metric | Labels | What it holds |
| --- | --- | --- |
| `tender_docker_up` | `machine` | `1` while the daemon reaches Docker, `0` while it can't. |
| `tender_service_state` | `machine`, `service`, `managed`, `state` | Always `1`. The `state` label is the service's state: `running`, `degraded`, `stopped` or `unknown`. `managed` is `true` for a service Tender deployed. |
| `tender_container_state` | `machine`, `service`, `container`, `runtime`, `state` | Always `1`. The `state` label is the container's state, such as `running` or `exited`. |
| `tender_container_memory_bytes` | `machine`, `service`, `container` | Memory in use, less the page cache it can drop, while it runs. |
| `tender_container_memory_limit_bytes` | `machine`, `service`, `container` | The container's memory limit, when it has one. |
| `tender_container_cpu_percent` | `machine`, `service`, `container` | CPU use since the previous report, where 100 is one whole CPU. |
| `tender_container_restart_count` | `machine`, `service`, `container` | How often Docker restarted the container. |

[Services](services.md#what-a-service-is) explains each state, and [What the daemon reports](services.md#what-the-daemon-reports) when usage is missing. Before v0.4, the container numbers were `tender_service_*` series without a `container` label.

### Backups

| Metric | Labels | What it holds |
| --- | --- | --- |
| `tender_backup_last_timestamp_seconds` | `machine` | When the newest snapshot was taken, in Unix seconds. |
| `tender_backup_last_age_seconds` | `machine` | How old the newest snapshot is, at the scrape. |
| `tender_backup_last_size_bytes` | `machine` | The newest snapshot's size, when restic reported one. |
| `tender_backup_snapshots` | `machine` | How many snapshots the machine has. |
| `tender_backup_last_run_failed` | `machine` | `1` when the newest backup run failed, else `0`. |

### Problems

| Metric | Labels | What it holds |
| --- | --- | --- |
| `tender_problems_open` | `event`, `severity` | How many problems are open: ones whose latest notification is not a `.resolved` one. `event` is the base event, such as `service.down`, and `severity` is the latest notification's. Every event has a series at every severity, `0` included. Tests are not problems. |

[Notifications](notifications.md#events) lists every event.

### Log shipping

Each machine's daemon reports its counters for each enabled [log sink](log-shipping.md) once a minute. A sink no daemon has reported on has no series.

| Metric | Labels | What it holds |
| --- | --- | --- |
| `tender_log_records_sent_total` | `machine`, `sink` | A counter of the records the sink took from the machine, since its daemon first sent to it. |
| `tender_log_records_dropped_total` | `machine`, `sink` | A counter of the records the daemon dropped for the sink, because its buffer was full or the sink refused them for good. |
| `tender_log_last_sent_timestamp_seconds` | `machine`, `sink` | When a send last worked, in Unix seconds. It has no series before the first one. |
| `tender_log_shipping_failing` | `machine`, `sink` | `1` while sending to the sink counts toward `logs.shipping_failing`: it has failed for 15 minutes, or records were dropped since the last send that worked. Else `0`. |

## Example queries

1. A machine without a snapshot for 26 hours: `tender_backup_last_age_seconds > 26 * 3600`.
2. Services that don't run as they should: `tender_service_state{state=~"degraded|stopped"} == 1`.
3. Containers that stopped: `tender_container_state{state="exited"} == 1`.
4. Any critical problem: `sum(tender_problems_open{severity="critical"}) > 0`.
5. Log records dropped in the last hour: `increase(tender_log_records_dropped_total[1h]) > 0`.

## Next steps

1. [REST API](api.md#get-metrics) has the route's reference.
2. [Notifications](notifications.md) explains when each event fires and resolves.
3. [Adapters](adapters.md) sends notifications to Slack, a webhook or your own program.
