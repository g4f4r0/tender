# Metrics

The hub serves its machines, services, backups and open alerts at `GET /metrics` in the Prometheus text format, for Prometheus, Grafana Agent or an OpenTelemetry collector to scrape.

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
| `tender_service_state` | `machine`, `service`, `runtime`, `state` | Always `1`. The `state` label is the service's state, such as `running` or `exited`. |
| `tender_service_memory_bytes` | `machine`, `service` | Memory in use, less the page cache it can drop, while it runs. |
| `tender_service_memory_limit_bytes` | `machine`, `service` | The container's memory limit, when it has one. |
| `tender_service_cpu_percent` | `machine`, `service` | CPU use since the previous report, where 100 is one whole CPU. |
| `tender_service_restart_count` | `machine`, `service` | How often Docker restarted the container. |

[Services](services.md#what-a-service-is) explains each state and when usage is missing.

### Backups

| Metric | Labels | What it holds |
| --- | --- | --- |
| `tender_backup_last_timestamp_seconds` | `machine` | When the newest snapshot was taken, in Unix seconds. |
| `tender_backup_last_age_seconds` | `machine` | How old the newest snapshot is, at the scrape. |
| `tender_backup_last_size_bytes` | `machine` | The newest snapshot's size, when restic reported one. |
| `tender_backup_snapshots` | `machine` | How many snapshots the machine has. |
| `tender_backup_last_run_failed` | `machine` | `1` when the newest backup run failed, else `0`. |

### Alerts

| Metric | Labels | What it holds |
| --- | --- | --- |
| `tender_alerts_open` | `type`, `severity` | How many alerts are open, `pending` or `sent`. Every type has a series at every severity, `0` included. |

[Alerts](alerts.md) lists every type.

### Log shipping

<!-- Helper b: the log-shipping counters go in this section. -->

The log-shipping counters are listed here once log shipping lands.

## Example queries

1. A machine without a snapshot for 26 hours: `tender_backup_last_age_seconds > 26 * 3600`.
2. Services that stopped: `tender_service_state{state="exited"} == 1`.
3. Any critical alert: `sum(tender_alerts_open{severity="critical"}) > 0`.

## Next steps

1. [REST API](api.md#get-metrics) has the route's reference.
2. [Alerts](alerts.md) explains when each alert opens.
3. [Adapters](adapters.md) sends alerts to Slack, a webhook or your own program.
