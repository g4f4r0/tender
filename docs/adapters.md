# Adapters

Tender sends its notifications to email, Slack, a signed webhook or a program of your own, through notification channels that each have a `type` and a `config`.

This page explains the adapter pattern, the event every channel gets, and how to write a webhook receiver or an exec adapter. To add an email channel, see [Set up email alerts](guides/set-up-email-alerts.md).

## The pattern

Every part of Tender that talks to the outside works the same way. Each item, such as a notification channel, has a `type` and a `config` object:

1. A built-in type is compiled into Tender, such as `email` or `slack`.
2. The `webhook` type posts a documented, signed JSON body to your URL.
3. The `exec` type runs a program you installed on the hub, and writes the same JSON to its standard input.

Each area has one JSON contract with a `version`, so a receiver can tell which shape it got. Tender never loads outside code into its own process: a webhook runs on your server, and an exec adapter runs as its own process.

Secrets in a `config`, such as a Slack webhook URL or a webhook's signing secret, are encrypted in the hub's database with `TENDER_ENCRYPTION_KEY`. The API, the CLI and the MCP tools never show them again: a secret reads as `"***"`, with a `<field>_set` field that is `true`. A `PATCH` can replace a secret but never read it. See [Security](security.md#how-tender-stores-secrets).

## Channel types

| Type | `config` | What it sends |
| --- | --- | --- |
| `email` | `{"to": "ops@example.com"}` | An email through the hub's SMTP settings. See [Set up email alerts](guides/set-up-email-alerts.md). |
| `slack` | `{"webhook_url": "https://hooks.slack.com/services/..."}` | A Slack message to an [incoming webhook](https://api.slack.com/messaging/webhooks). The URL is a secret, and must start with `https://hooks.slack.com/`, so its key only ever goes to Slack. |
| `webhook` | `{"url": "https://alerts.example.com/tender"}` | The [event](#the-event) as JSON in a `POST`, signed with `Tender-Signature`. The hub makes the `secret` and shows it once, when you add the channel. |
| `exec` | `{"command": "my-adapter"}` | The [event](#the-event) as JSON on the standard input of `/etc/tender/adapters/my-adapter`. |

Every channel also has `enabled`, `true` unless you turn it off, `min_severity`, the least severe notification it gets: `info`, `warning` (the default) or `critical`, and `events`, the events it gets. `events` is a list of patterns: `*` for every event (the default), an area such as `service.*`, or one event such as `backup.failed`. [Notifications](notifications.md#events) lists the events.

### Where a webhook may send

The hub checks the address a webhook's URL leads to each time it connects, after DNS, so a name that later resolves somewhere else can't get around the check:

1. Loopback (`127.0.0.0/8`, `::1`), link-local (`169.254.0.0/16`, `fe80::/10`), multicast and unspecified addresses are refused. They would let a channel reach the hub's own services or a cloud's metadata endpoint.
2. Private addresses (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`) and Tailscale addresses (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`) are allowed. A self-hosted receiver, such as ntfy or a pager bridge, usually lives on your own network or tailnet, and Tender runs on a tailnet itself.
3. Plain `http` goes only to private and Tailscale addresses. A public address needs `https`.

The hub uses no proxy for webhooks and Slack, and never follows a redirect, so a body and its signature only go to the URL in the config.

## The event

A webhook and an exec adapter get the same JSON, version 1, for each notification. A Slack message and an email are made from it too.

```json
{
  "version": 1,
  "event": "service.down",
  "notification": {
    "id": 42,
    "event": "service.down",
    "severity": "critical",
    "machine": "apps",
    "service": "notes",
    "container": "web",
    "channel": null,
    "message": "web in notes on apps stopped with exit code 137.",
    "created_at": "2026-11-15T09:12:00Z"
  },
  "hub": "https://apps.your-tailnet.ts.net"
}
```

| Field | What it holds |
| --- | --- |
| `version` | `1`. A later, incompatible shape gets a new number. Fields may be added within version 1, so ignore the ones you don't know. |
| `event` | The notification's event, such as `service.down`, `service.down.escalated`, `service.down.resolved` or `notification.test`. [Notifications](notifications.md#events) lists them all. |
| `notification.id` | The notification's id on the hub, the same as in `GET /v1/notifications`. |
| `notification.event` | The same as `event`. |
| `notification.severity` | `info`, `warning` or `critical`. |
| `notification.machine` | The machine it is about, or `null`, such as for `channel.failing` or a test. |
| `notification.service` | The service it is about, such as `notes`, or `null`. |
| `notification.container` | The container in that service it is about, such as `web`, or `null` for one about the whole service or something else. Added in v0.4. |
| `notification.channel` | The channel a `channel.failing` notification is about, or `null`. |
| `notification.message` | One sentence that says what happened. It never holds log text. |
| `notification.created_at` | When the hub added the notification, in RFC 3339 UTC. |
| `hub` | The hub's address, from `TENDER_HUB_URL`, so a message can link back to it. |

A problem sends up to three events, each its own notification with its own id: `<area>.<condition>` when it starts, `<area>.<condition>.escalated` the first time it gets worse, and `<area>.<condition>.resolved` when it clears. A channel gets the resolved one only if it got one of the others. A test is `notification.test`.

### How delivery works

Each channel has its own deliveries, so one failing channel never delays or repeats another. A channel gets its notifications in order: a newer one waits while an older one is tried again, so `service.down.resolved` never arrives before `service.down`.

1. A webhook or Slack answer of `2xx` within 10 seconds is delivered. An exec adapter that exits with status `0` within 10 seconds is delivered, even when a program it started keeps running; the hub then stops that program.
2. A `4xx` answer other than `429` fails at once, because sending it again would get the same answer. The error is the status alone, such as `HTTP 404 Not Found`: the hub never reads or keeps the answer's body.
3. Anything else is tried again after 1, 2, 5, 10 and 30 minutes, then every 30 minutes. After 24 hours the delivery fails.
4. When every try to deliver to a channel has failed for an hour, the hub sends `channel.failing` to every other enabled channel. It resolves once the channel delivers again.

`GET /v1/notifications` shows each notification's `deliveries`, with their `status`, `attempts` and `error`. A notification is `sent` once a channel delivered it, and `failed` once every channel's delivery failed.

A delivery can arrive twice, such as when the hub stops after your receiver answered but before it recorded the answer. Use `notification.id` to drop a repeat.

## Verify a webhook

Every webhook request carries a `Tender-Signature` header, made like Stripe's:

```text
Tender-Signature: t=1794733920,v1=5257a869e7ecebeda32affa62cdca3fa51cad7e77a0e56ff536d0ce8e108d8bd
```

`t` is when the hub signed it, in Unix seconds. `v1` is the hex HMAC-SHA256 of `<t>.<body>`, keyed with the channel's secret, where `<body>` is the request body exactly as it arrived. To verify a request:

1. Split the header on `,` and read `t` and `v1`.
2. Compute the HMAC-SHA256 of the bytes of `t`, a `.` and the raw body, with the secret as the key, in hex.
3. Compare it with `v1` in constant time. Refuse the request when they differ.
4. Refuse a `t` more than 5 minutes from your clock, so a recorded request can't be replayed later.

Answer `2xx` once you have the event. Answer `400` to a bad signature: a `4xx` fails the delivery at once, and the hub's `error` shows its status, such as `HTTP 400 Bad Request`.

### A webhook receiver in Python

This receiver uses only Python's standard library. It verifies each request, drops repeats and prints one line per notification.

1. Add the channel and keep the secret the hub prints. It is shown only once:

   ```sh
   tender channels add receiver --type webhook --url https://alerts.example.com/tender
   ```

   ```text
   Added channel receiver. It gets the notifications of * from warning up. Check that it arrives with: tender channels test receiver

   Its signing secret, made by the hub and shown only now:
     whsec_3q2nVZ0x8Yb5u1lHnN2Gx3cBVQ0N8m0ukq3Zq8m6c1Y

   Give it to the webhook's receiver, which checks the Tender-Signature header with it. Tender cannot
   show it again; replace it with: tender channels update receiver --secret-file PATH
   ```

2. Save this as `tender_receiver.py` on the server behind that URL:

   <!-- example: tender_receiver.py -->
   ```python
   #!/usr/bin/env python3
   """Receive Tender's notification webhooks and verify their Tender-Signature."""
   import hashlib
   import hmac
   import json
   import os
   import sys
   import time
   from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

   SECRET = os.environ["TENDER_WEBHOOK_SECRET"].encode()
   PORT = int(os.environ.get("PORT", "8000"))
   TOLERANCE_SECONDS = 300
   MAX_BODY_BYTES = 1 << 20
   seen = set()  # the ids of the notifications already handled


   def verify(header, body):
       """Return True when header signs body with SECRET, within the tolerance."""
       parts = dict(p.split("=", 1) for p in header.split(",") if "=" in p)
       try:
           t = int(parts["t"])
           v1 = parts["v1"]
       except (KeyError, ValueError):
           return False
       if abs(time.time() - t) > TOLERANCE_SECONDS:
           return False
       want = hmac.new(SECRET, str(t).encode() + b"." + body, hashlib.sha256).hexdigest()
       return hmac.compare_digest(want, v1)


   class Receiver(BaseHTTPRequestHandler):
       def do_POST(self):
           length = int(self.headers.get("Content-Length", "0"))
           if length <= 0 or length > MAX_BODY_BYTES:
               return self.answer(400, "The body is missing or too large.")
           body = self.rfile.read(length)
           if not verify(self.headers.get("Tender-Signature", ""), body):
               return self.answer(400, "The Tender-Signature doesn't match this body.")
           event = json.loads(body)
           if event.get("version") != 1:
               return self.answer(400, "This receiver reads version 1 events only.")
           n = event["notification"]
           if n["id"] not in seen:
               seen.add(n["id"])
               where = "/".join(x for x in (n["machine"], n["service"]) if x)
               on = f" on {where}" if where else ""
               print(f'{n["severity"]} {event["event"]}{on}: {n["message"]}', flush=True)
           self.answer(200, "ok")

       def answer(self, status, text):
           self.send_response(status)
           self.send_header("Content-Type", "text/plain")
           self.end_headers()
           self.wfile.write(text.encode() + b"\n")

       def log_message(self, *args):
           pass  # the events printed above are the log


   if __name__ == "__main__":
       print(f"listening on port {PORT}", file=sys.stderr, flush=True)
       ThreadingHTTPServer(("", PORT), Receiver).serve_forever()
   ```

3. Run it with the secret from step 1, behind your HTTPS proxy:

   ```sh
   TENDER_WEBHOOK_SECRET='whsec_3q2nVZ0x8Yb5u1lHnN2Gx3cBVQ0N8m0ukq3Zq8m6c1Y' PORT=8000 python3 tender_receiver.py
   ```

4. Send a test from any machine that reaches the hub:

   ```sh
   tender channels test receiver
   ```

   ```text
   CHANNEL    RESULT
   receiver   delivered
   ```

   The receiver prints:

   ```text
   info notification.test: This is a test from the Tender hub at https://apps.your-tailnet.ts.net. If you can read it, notifications reach this channel.
   ```

To check a signature by hand, such as for a body you saved to `body.json`, compute it with `openssl` and compare it with `v1`:

```sh
printf '%s.' 1794733920 | cat - body.json | openssl dgst -sha256 -hmac "$TENDER_WEBHOOK_SECRET" -hex
```

## Write an exec adapter

An exec adapter is a program on the hub that gets each event on its standard input. Use one to reach something Tender has no type for, such as a pager, a chat without incoming webhooks, or a script of your own.

The hub runs it with these rules, so the API can't run anything you didn't install:

1. `config.command` is only a name. The program must be an executable file at `/etc/tender/adapters/<name>`, and the folder and the file must be owned by root and not writable by their group or others, so only root on the hub can install or change one.
2. It runs as the hub's `tender` user, inside the hub's sandbox, in `/etc/tender/adapters`.
3. Its environment holds only `PATH` and `LANG`. The hub doesn't pass it its own settings, such as `TENDER_ENCRYPTION_KEY`, so keep the adapter's settings in the file or in a file it reads. It runs as the hub's user, though, so install only programs you trust; see [Security](security.md#what-an-exec-adapter-can-do).
4. It gets the event and a newline on standard input. It has 10 seconds, after which the hub kills it and everything it started. Once it exits, the hub also stops anything it left running.
5. Exit status `0` is delivered. Any other status is tried again, as [How delivery works](#how-delivery-works) says, and the first 1 KiB of its standard error, or else its standard output, is the delivery's `error`. The hub keeps at most 64 KiB of each.

### An exec adapter in shell

This adapter posts each notification to an [ntfy](https://ntfy.sh) topic, with its severity as the priority. It needs `curl` and `jq` on the hub.

1. Make the adapters folder, as root on the hub:

   ```sh
   sudo install -d -m 755 -o root -g root /etc/tender/adapters
   ```

2. Save this as `/etc/tender/adapters/ntfy`, and set `NTFY_URL` to your topic:

   <!-- example: ntfy -->
   ```sh
   #!/bin/sh
   # Tender exec adapter: post each notification to an ntfy topic.
   set -eu
   NTFY_URL="https://ntfy.sh/your-secret-topic"

   event=$(cat)
   [ "$(printf '%s' "$event" | jq -r .version)" = 1 ] || { echo "this adapter reads version 1 events only" >&2; exit 1; }
   kind=$(printf '%s' "$event" | jq -r .event)
   severity=$(printf '%s' "$event" | jq -r .notification.severity)
   title=$(printf '%s' "$event" | jq -r '"Tender: \(.event) on \(.notification.machine // "the hub")"')
   message=$(printf '%s' "$event" | jq -r .notification.message)

   case "$kind:$severity" in
     *.resolved:*) priority=2 ;;
     *:critical) priority=5 ;;
     *:warning) priority=4 ;;
     *) priority=3 ;;
   esac

   # A 4xx or 5xx answer fails curl, so the hub tries the event again.
   curl -fsS --max-time 8 -H "Title: $title" -H "Priority: $priority" -d "$message" "$NTFY_URL" >/dev/null
   ```

3. Make root its owner, and let others read and run it but not change it:

   ```sh
   sudo chown root:root /etc/tender/adapters/ntfy
   sudo chmod 755 /etc/tender/adapters/ntfy
   ```

4. Add the channel and test it:

   ```sh
   tender channels add phone --type exec --command ntfy
   tender channels test phone
   ```

   ```text
   CHANNEL   RESULT
   phone     delivered
   ```

   A failure shows what the program printed, such as `phone     failed: ntfy exited with status 22: curl: (22) The requested URL returned error: 403`, and the command exits with status 1.

To try an adapter without the hub, pipe it an event:

```sh
echo '{"version":1,"event":"notification.test","notification":{"id":1,"event":"notification.test","severity":"info","machine":null,"service":null,"container":null,"channel":null,"message":"Hello.","created_at":"2026-11-15T09:12:00Z"},"hub":"https://apps.your-tailnet.ts.net"}' | sudo -u tender /etc/tender/adapters/ntfy; echo "exit $?"
```

## Log sinks

<!-- Helper b: the log sinks contract, its types and its example adapters go in this section. -->

The log sinks section is added with log shipping.

## Next steps

1. [Set up email alerts](guides/set-up-email-alerts.md) adds an email channel and tests it.
2. [Notifications](notifications.md#events) lists every event and when it fires and resolves.
3. [REST API](api.md#notifications) has the channel routes.
4. [Security](security.md) explains how channel secrets and exec adapters are kept safe.
