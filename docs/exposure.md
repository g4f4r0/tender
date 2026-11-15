# Exposure

A route makes one container port of a managed service reachable. The container's port is published on `127.0.0.1` only, so nothing reaches it from outside the machine until an exposure adapter serves it.

This page explains routes and the three ways to expose one. To deploy the service a route belongs to, see [Services](services.md#deploy-a-service).

## Routes

A service's routes come with its deploy: `--route CONTAINER:PORT:EXPOSE[:HOST]` on `tender services deploy`, or `routes` in the API and MCP. A route has these fields:

| Field | What it holds |
| --- | --- |
| `container` | The service in the compose file, such as `web`. |
| `port` | Its container port, such as `8080`. |
| `expose` | `tailscale`, `cloudflare` or `public`. |
| `host` | The full hostname for a `cloudflare` or `public` route, such as `notes.example.com`, and `null` for `tailscale`. A host routes to one service across the hub. |
| `https_port` | For a `tailscale` route, the HTTPS port on the machine's tailnet name. Left out, the hub picks the lowest free one from `10000`. It is unique per machine. |
| `local_port` | The `127.0.0.1` port the container is published on, which the hub assigns per machine from `20000` to `29999`. A route the service keeps keeps its port across changes. |
| `url` | Where the route answers, such as `https://apps.tail1234.ts.net:10000` or `https://notes.example.com`. It is `null` for a `tailscale` route until the daemon has told the hub the machine's tailnet name. |

For example, this deploy exposes `web` on the tailnet at port 443 and `api` at a public host:

```sh
tender services deploy apps notes -f compose.yaml \
  --route web:8080:tailscale:443 \
  --route api:9000:public:api.example.com
```

A route to a container that the compose file doesn't have fails the deploy with `a route goes to api, which the compose file has no service for. Its services are web: route to one of them`.

## How the daemon applies them

Each adapter takes the whole set of routes of its kind on the machine, every managed service's, and makes the machine match it. It changes only what it created, so running it again changes nothing, and a port or record you set up yourself stays.

The daemon applies the exposures after every deploy and remove, and once when it starts, from `GET /v1/machines/{machine}/exposures`. A service being removed has its routes taken out first. A deploy whose containers run but whose routes could not all be exposed fails with `the service runs, but its routes could not all be exposed:` and why, and one adapter's failure doesn't stop the others.

## Tailscale

A `tailscale` route answers over HTTPS on the machine's tailnet name, for the devices on your tailnet only. The daemon runs, per route:

```sh
tailscale serve --bg --https=<https_port> http://127.0.0.1:<local_port>
```

and `tailscale serve --https=<port> off` for a port it served before that no route has any more. It keeps the ports it serves in `/var/lib/tender/daemon/tailscale-ports.json` and never touches another port. A route whose port Tailscale Serve already uses for something else fails with a sentence that names the port and how to free it:

```text
tailscale serve on this machine already uses HTTPS port 8443 for something else, so the route to web in notes can't have it. Give the route another https_port, or free the port with: tailscale serve --https=8443 off
```

The daemon reads the machine's tailnet name, such as `apps.tail1234.ts.net`, with `tailscale status --json`, and sends it to the hub with `PUT /v1/machines/{machine}/exposures/status`, which gives the routes their `url`. The machine needs Tailscale installed and logged in, with HTTPS certificates turned on for the tailnet. Without Tailscale, a `tailscale` route fails with `Tailscale isn't installed on this machine, so its tailscale routes can't be served.`

## Cloudflare

A `cloudflare` route answers at its host on the public internet, through a Cloudflare tunnel, without opening a port on the machine. The hub keeps the Cloudflare account and an API token, and runs one tunnel per machine named `tender-<machine>`, with an ingress rule and a proxied DNS record for each host. The daemon runs `cloudflared` with the tunnel's token as service `cloudflared` in project `tender`. The token reaches only the machine's own daemon, with its exposures. Until the hub has the Cloudflare settings, a `cloudflare` route is refused with `This hub can't route through Cloudflare yet: set it up with PUT /v1/exposures/cloudflare first.`

## Public

A `public` route answers at its host on the public internet, through one Caddy container on the machine, which gets its certificates automatically. Caddy runs as service `caddy` in project `tender`, on ports 80 and 443, from the first `public` route on the machine to the last. While the machine has `public` routes and ufw is active, the daemon allows 80/tcp and 443/tcp with the comment `tender public`. The host's DNS record and the provider's firewall are yours to set.

## Next steps

1. [Services](services.md#deploy-a-service) explains how a deploy runs.
2. [REST API](api.md) has the services routes with their `routes`.
