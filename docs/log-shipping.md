# Log shipping

Each daemon sends its machine's systemd journal and its Docker containers' logs to the sinks you add, such as an OpenTelemetry collector or your own webhook, straight from the machine.

This page explains how sending works, what happens when a sink is down, and the webhook contract a receiver implements. To read a machine's journal once, without a sink, see [Read a machine's logs](guides/read-machine-logs.md).

## How it works

A log sink is a place logs go, with a type and its settings. The hub keeps the sinks and their secrets, encrypted. Each daemon gets the sinks for its machine from the hub every 30 seconds, reads the logs on the machine, and sends them to each sink itself. The logs never pass through the hub, which only sees each daemon's counters: how many records it sent and dropped, and its last error.

| Type | Sends | To |
| --- | --- | --- |
| `otlp` | OTLP/HTTP JSON | An OpenTelemetry collector, or any service that takes OTLP logs, such as Grafana Cloud, Honeycomb or Datadog. |
| `webhook` | Tender's own JSON, signed | Any HTTPS endpoint you run. [The webhook contract](#the-webhook-contract) describes it. |

A sink applies to every machine, or to the machines you name. Each daemon sends the logs from the moment it first sees the sink. Older logs aren't sent. A source you turn on later, such as the journal after `--no-journal`, starts from the moment the daemon sees the change.

## Prerequisites

1. A hub and daemons on Tender v0.3.0 or later. The daemon reads the journal through the `systemd-journal` group and the containers through the `docker` group, which the installer sets up. See [Install](install.md).
2. A collector or receiver that each machine can reach over HTTP or HTTPS. The machines connect to it directly, so it must be reachable from them, not from the hub.

## Send logs to an OpenTelemetry collector

1. Save the collector's headers, such as its API key, in a file, one `Name: value` a line:

   ```sh
   printf 'Authorization: Bearer <key>\n' > otel-headers
   chmod 600 otel-headers
   ```

2. Add the sink:

   ```sh
   tender sinks add otel --type otlp --endpoint https://otel.example.com:4318/v1/logs --headers-file otel-headers
   ```

   ```text
   Added log sink otel. Machines start sending to it within a minute.
   ```

   The hub stores the headers encrypted, and never shows them again. Delete the file once it is added.

3. After a minute, check that each machine sends:

   ```sh
   tender sinks show otel
   ```

   ```text
   NAME       otel
   TYPE       otlp
   SENDS TO   https://otel.example.com:4318/v1/logs
   ENABLED    yes
   SOURCES    journal (info and up), services
   MACHINES   all

   MACHINE   SENT   DROPPED   LAST SENT   PROBLEM
   apps      412    0         just now    -
   ```

   Each machine reports once a minute. A machine that can't reach the collector shows the reason under `PROBLEM`, such as `failing since 2 minutes ago: the sink didn't answer: dial tcp 10.0.0.5:4318: connect: connection refused`.

The endpoint is the collector's logs URL, which ends in `/v1/logs` on a standard OpenTelemetry collector. Tender sends JSON, so the collector's OTLP receiver needs its HTTP protocol on, which is the default on port 4318.

## Send logs to a webhook

1. Add the sink. Leave out `--secret-file`, and the hub makes the signing secret:

   ```sh
   tender sinks add hook --type webhook --url https://logs.example.com/tender
   ```

   ```text
   Added log sink hook. Machines start sending to it within a minute.

   The signing secret, made by the hub and shown only now:
     <secret>

   Give it to the receiver, which checks each batch's Tender-Signature header with it. Tender cannot
   show it again; to replace it, run tender sinks update hook --secret-file PATH.
   ```

2. Give the secret to the receiver, which checks each request's signature with it, as [Verify a signature](#verify-a-signature) shows.
3. Check that each machine sends, with `tender sinks show hook`, as in the steps above.

## Choose what is sent

By default, a sink gets every unit's journal from the `info` priority up, and every container's logs. Narrow it when you add it, or later with `tender sinks update`:

```sh
tender sinks update otel --units nginx.service,tenderd.service --min-priority warning --services web
```

```text
Updated log sink otel. Machines pick the change up within a minute.
```

| Option | What it sends |
| --- | --- |
| `--units` | Only these systemd units' journal entries. `all` sends every unit's. |
| `--min-priority` | Only journal entries of this priority and the more severe ones: `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info` or `debug`. |
| `--services` | Only these containers' logs, by name. `all` sends every container's. |
| `--no-journal` | No journal entries. |
| `--no-services` | No container logs. |
| `--machines` | Only from these machines. `all` sends from every machine. |

The daemon follows every container. It starts following a new or re-created container as soon as Docker says it started, so even a job that runs for a moment is sent, and lists the containers every 5 seconds in case it missed one. A re-created container is a new container, with logs of its own, so the old one's last lines still go. A container's standard output gets the priority `info`, and its standard error `err`. The daemon reads what `docker logs` shows, so a container whose logs `docker logs` can't read sends nothing.

`tender sinks update otel --disable` stops sending and keeps the sink. Each daemon then deletes the logs it buffered for it, as it does for a machine you take out of `--machines`. `--enable` starts sending again, with the logs from then on.

## Batches

A daemon sends each sink's records in batches, one HTTP POST each. A batch goes out when it has 1000 records, when it reaches 1 MiB, or 5 seconds after its first record, whichever comes first. One log line is at most 64 KiB in a record. A longer one is cut, ends in ` [truncated]`, and the lines after it come as usual.

The daemon saves each batch on disk before it sends it, and saves where it has read up to in the journal and in each container's logs once the batch is saved. After a restart, it sends the saved batches, then reads on from there. No record is lost, and at most the one batch being sent when the daemon stopped is sent again. A receiver that must not count a record twice can drop a batch whose records it already has.

A sink answers each POST with a 2xx status once it has the records. Any other answer, or none within 30 seconds, is a failure. The daemon never follows a redirect, so a sink that answers `301` or `307` fails until its URL is updated to the new address.

## When a sink is down

While a sink fails, the daemon keeps reading logs and keeps their batches on disk, up to 64 MiB for each sink. It retries the oldest batch after 1 second, then waits twice as long each time, up to 1 minute. Once the sink answers, it sends the batches it kept, the oldest first.

When a sink's 64 MiB is full, the daemon drops its oldest batches to make room for new ones, and counts their records in `records_dropped`. A sink that answers `400`, `413` or `422` refused the batch for good, so the daemon drops that batch, counts it and goes on with the next.

The `log_shipping_failing` alert opens when sending to a sink has failed for 15 minutes, or when the daemon dropped records. It resolves after a send to the sink works. [Alerts](alerts.md#log-shipping-alerts) shows how it reads.

`last_error` says why the last send failed, such as `the sink answered 503 Service Unavailable` or `the sink didn't answer: dial tcp 10.0.0.5:4318: connect: connection refused`. It never holds the sink's answer or its URL, which can hold secrets.

## The webhook contract

This is version 1 of the contract between a Tender daemon and a webhook receiver. A later version changes `version`, and a receiver refuses a version it doesn't know with `400`.

### The request

Each batch is one request:

```http
POST /tender HTTP/1.1
Host: logs.example.com
Content-Type: application/json
User-Agent: tender
Tender-Signature: t=1763208250,v1=2fb5c3f2a793311e12f5f46614932d99fbb1d3769d06d950161220e2e9864cc2

{"version":1,"machine":"apps","records":[{"time":"2026-11-15T12:04:09.512031Z","source":"journal","unit":"nginx.service","priority":"warning","message":"upstream timed out"},{"time":"2026-11-15T12:04:10.003114Z","source":"service","service":"web","priority":"err","message":"GET /health 500"}]}
```

| Field | Type | What it is |
| --- | --- | --- |
| `version` | integer | `1`. |
| `machine` | string | The machine's name in Tender. |
| `records` | array | From 1 to 1000 records, the oldest first for each source. |
| `records[].time` | string | When the line was logged, RFC 3339 in UTC with up to nanoseconds. |
| `records[].source` | string | `journal` or `service`. |
| `records[].unit` | string | For `journal`, the systemd unit, such as `nginx.service`, or the program's name for a line without one, such as `kernel`. Left out for `service`. |
| `records[].service` | string | For `service`, the container's name. Left out for `journal`. |
| `records[].priority` | string | `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info` or `debug`. |
| `records[].message` | string | The line, without its line break, as valid UTF-8. A line over 64 KiB is cut to 64 KiB and ends in ` [truncated]`. |

A receiver answers `2xx` once it has stored the batch. It answers `400`, `413` or `422` only for a batch it will never take, because the daemon then drops it. Any other status, such as `429` or `503`, makes the daemon send the same records again later.

### Verify a signature

`Tender-Signature` is `t=<unix time>,v1=<signature>`. The time is when the daemon sent the request, in seconds. The signature is the HMAC-SHA256 of the time, a dot and the raw body, keyed with the sink's secret, in lowercase hex. A retry is signed again with its own time.

A receiver checks it in three steps:

1. Read the raw body as it came, before parsing it.
2. Compute the HMAC-SHA256 of `<t>.<body>` with the secret, and compare it with `v1` in constant time.
3. Refuse a request whose `t` is more than 5 minutes from now, so a captured request can't be replayed later.

In Python:

```python
import hashlib
import hmac
import time


def verify(secret: bytes, header: str, body: bytes, tolerance: int = 300) -> bool:
    """Checks a Tender-Signature header against the raw request body."""
    try:
        parts = dict(p.split("=", 1) for p in header.split(","))
        t = int(parts["t"])
        got = parts["v1"]
    except (KeyError, ValueError):
        return False
    if abs(time.time() - t) > tolerance:
        return False
    want = hmac.new(secret, f"{t}.".encode() + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, got)
```

To check a saved request by hand, with the body in `body.json` and the header's time in `t`:

```sh
{ printf '%s.' "$t"; cat body.json; } | openssl dgst -sha256 -hmac "$TENDER_SECRET" | sed 's/^.*= //'
```

```text
2fb5c3f2a793311e12f5f46614932d99fbb1d3769d06d950161220e2e9864cc2
```

The output matches `v1` when the request is genuine.

## How OTLP records look

Each batch is one OTLP `ExportLogsServiceRequest` in JSON. Each unit or container in the batch is one resource, with one scope named `tender`.

| OTLP field | From |
| --- | --- |
| Resource attribute `host.name` | The machine's name in Tender. |
| Resource attribute `service.name` | The systemd unit, or the container's name. |
| Record attribute `log.source` | `journal` or `service`. |
| `timeUnixNano` and `observedTimeUnixNano` | When the line was logged. |
| `severityText` | The priority, such as `warning`. |
| `severityNumber` | `24` for `emerg`, `23` for `alert`, `21` for `crit`, `17` for `err`, `13` for `warning`, `10` for `notice`, `9` for `info` and `5` for `debug`. |
| `body.stringValue` | The line. |

The headers you gave go with every request. Tender sets `Content-Type`, `Content-Length`, `Host`, `Transfer-Encoding` and `User-Agent` itself, so a sink can't set them.

## Reference

| Field | Type | What it is |
| --- | --- | --- |
| `name` | string | Lowercase letters, digits and hyphens, up to 63 characters. It can't change. |
| `type` | string | `otlp` or `webhook`. It can't change. |
| `config.endpoint` | URL | For `otlp`, the collector's OTLP/HTTP logs URL. |
| `config.headers` | object | For `otlp`, a secret. Up to 20 HTTP headers sent with each request. |
| `config.url` | URL | For `webhook`, where the batches go. |
| `config.secret` | string | For `webhook`, a secret. The signing secret, at least 16 characters; the hub makes a 64-character one when you leave it out. |
| `enabled` | boolean | Whether daemons send to it. |
| `sources.journal` | object or null | `units`, empty for all, and `min_priority`. `null` sends no journal. |
| `sources.services` | object or null | `names`, empty for every container. `null` sends no container logs. |
| `machines` | array | The machines that send to it. Empty for every machine. |
| `status[]` | array | Per machine, `records_sent`, `records_dropped`, `last_sent_at`, `last_error`, `failing_since` and `last_dropped_at`. |

Neither the URL nor the endpoint can hold a login. Put it in a header or the secret, which the hub stores encrypted.

| Limit | Value |
| --- | --- |
| Records in a batch | 1000 |
| Bytes in a batch | 1 MiB |
| Wait before a batch goes | 5 seconds |
| One line in a record | 64 KiB |
| Buffer on disk, for each sink on each machine | 64 MiB |
| Time a send may take | 30 seconds |
| Retry wait | 1 second, doubling up to 1 minute |
| Failing time before the alert | 15 minutes |

The daemon keeps each sink's buffer and read positions in `/var/lib/tender/daemon/logs/<sink id>/`. Removing or disabling a sink deletes that folder on every machine within a minute.

## Next steps

1. [Alerts](alerts.md#log-shipping-alerts) explains the `log_shipping_failing` alert.
2. [CLI reference](cli.md#tender-sinks) lists every `tender sinks` option.
3. [API reference](api.md#log-sinks) lists the log sink routes, and [MCP tools](mcp.md#log-sinks) the tools.
4. [Security](security.md#log-sink-secrets) explains how the hub keeps the sinks' secrets.
