# Exposure

A route makes one container port of a managed service reachable. The container's port is published on `127.0.0.1` only, so nothing reaches it from outside the machine until an exposure adapter serves it.

This page explains routes and the three ways to expose one. To deploy the service a route belongs to, see [Services](services.md#deploy-a-service).

## Routes

A service's routes come with its deploy: `--route CONTAINER:PORT:EXPOSE[:HOST[:access]]` on `tender services deploy`, or `routes` in the API and MCP. A route has these fields:

| Field | What it holds |
| --- | --- |
| `container` | The service in the compose file, such as `web`. |
| `port` | Its container port, such as `8080`. |
| `expose` | `tailscale`, `cloudflare` or `public`. |
| `host` | The full hostname for a `cloudflare` or `public` route, such as `notes.example.com`, and `null` for `tailscale`. A host routes to one service across the hub. |
| `https_port` | For a `tailscale` route, the HTTPS port on the machine's tailnet name. Left out, the hub picks the lowest free one from `10000`. It is unique per machine. |
| `access` | `true` when a `cloudflare` route requires Cloudflare Access at the machine. It defaults to `false`, and any other route with `true` is refused. See [Require Cloudflare Access](#require-cloudflare-access). |
| `local_port` | The `127.0.0.1` port the container is published on, which the hub assigns per machine from `20000` to `29999`. A route the service keeps keeps its port across changes. A new route gets the next free port after the last one the machine got, and the lowest free one once `29999` is taken, so a port a route let go of isn't handed out again right away. |
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

The daemon applies the exposures after every deploy and remove, and once when it starts, from `GET /v1/machines/{machine}/exposures`. A service being removed has its routes taken out first. Each adapter sets every route it can. A route that can't be set fails only the deploy of the service it belongs to, with `the service runs, but its routes could not all be exposed:` and a sentence naming the route, so that service's `deploy_status` is `failed` and its `last_error` says why. Every other route stays up, and the other services on the machine still deploy and remove; the daemon logs the failed route as a warning. An adapter that fails as a whole, such as Tailscale missing, fails the deploys of the services with routes on it, or that had routes on it before this deploy. One adapter's failure doesn't stop the others.

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

## Public

A `public` route serves `https://<host>` from the machine itself, through Caddy with automatic HTTPS. Use it on a machine with a public address when you don't want Cloudflare in front.

### Prerequisites

DNS and the provider's firewall are yours. Tender never changes either.

1. A DNS record for the host, such as an `A` record for `blog.example.com`, that points at the machine's public address.
2. Inbound TCP 80 and 443 to the machine in the provider's firewall, such as a Hetzner Cloud firewall or an AWS security group. Caddy needs port 80 to get the certificate and to redirect to HTTPS.
3. Nothing else on the machine listening on port 80 or 443.

### Add a public route

1. Deploy the service with a route to its container `web`, whose expose is `public` and whose host is the full hostname:

   ```sh
   tender services deploy apps blog -f compose.yaml --route web:80:public:blog.example.com
   ```

2. Check it from anywhere. The first request can take a few seconds while Caddy gets the certificate:

   ```sh
   curl -sI https://blog.example.com
   ```

   ```text
   HTTP/2 200
   ```

### What Tender does on the machine

1. On the first public route it starts one Caddy container, service `caddy` of the Compose project `tender`, with host networking. It isn't a service you deploy, and the rules for those don't apply to it.
2. It writes `/var/lib/tender/daemon/caddy/Caddyfile` with one site per route, `<host> { reverse_proxy 127.0.0.1:<local_port> }`, and reloads Caddy inside the container on every deploy and remove. Caddy keeps its certificates in `/var/lib/tender/daemon/caddy/data`, which the whole-server backup includes.
3. While ufw is active, it adds `ufw allow 80/tcp` and `ufw allow 443/tcp`, each with the comment `tender public`. A port that already has an allow rule of yours keeps it, and Tender adds none for it. Without ufw, or with ufw inactive, it adds nothing.
4. Meanwhile the [security checks](security.md#security-checks) count 80/tcp and 443/tcp as allowed on that machine, so they raise no `security.port_exposed`.
5. After the last public route it removes the container and deletes only the ufw rules with the comment `tender public`. The certificates stay in `data`, so a later route doesn't ask for them again. Delete the folder to forget them.

A route with a bad host, or a host another route also has, fails on its own and stays out of the Caddyfile. Caddy serves the other routes.

Caddy serves HTTP/1.1 and HTTP/2 only, so UDP 443 stays closed. Its admin API is a Unix socket inside the container, so no process on the machine can reconfigure it through a port. The image is `caddy:2.11.9`, pinned by digest in Tender, and a Tender update moves it.

## Cloudflare

A `cloudflare` route serves `https://<host>` through a Cloudflare tunnel. The machine dials out to Cloudflare, so it needs no public address and no inbound port.

### Prerequisites

1. The host's domain is a zone in your Cloudflare account, on Cloudflare's DNS.
2. A Cloudflare API token with these permissions, for the account and its zones, or the zones you use:
   1. Account, Cloudflare Tunnel, Edit, to create the machine's tunnel and set its routes.
   2. Zone, DNS, Edit, to keep a DNS record for each host.
   3. Zone, Zone, Read, to find the zone of each host.

   A route that [requires Cloudflare Access](#require-cloudflare-access) also needs two more.
3. Outbound access from the machine to Cloudflare on port 7844, TCP and UDP, which cloudflared uses.

### Set up Cloudflare

1. In the Cloudflare dashboard, open My Profile, then API Tokens, and create a custom token with the three permissions above, and the two Access ones if a route will require Access. Save it in a file, such as `cloudflare-token`.
2. Copy the account ID from the dashboard's Account home.
3. Give both to the hub. It lists the account's tunnels and zones with the token first, and stores nothing if Cloudflare refuses:

   ```sh
   tender exposures cloudflare set --account-id 0123456789abcdef0123456789abcdef --api-token-file cloudflare-token
   ```

   ```text
   Saved the Cloudflare settings. Cloudflare accepted the API token for account 0123456789abcdef0123456789abcdef.
   ```

4. Check the settings. The token is never shown again:

   ```sh
   tender exposures cloudflare show
   ```

   ```text
   Cloudflare account: 0123456789abcdef0123456789abcdef
   API token: set, hidden
   Updated 1 minute ago.
   ```

The hub stores the token encrypted under `TENDER_ENCRYPTION_KEY`, like the other secrets, and every route, tool and command shows it as `"***"`.

### Add a Cloudflare route

1. Deploy the service with a route whose expose is `cloudflare`:

   ```sh
   tender services deploy apps blog -f compose.yaml --route web:80:cloudflare:blog.example.com
   ```

2. Check it from anywhere:

   ```sh
   curl -sI https://blog.example.com
   ```

   ```text
   HTTP/2 200
   ```

### What Tender does in Cloudflare

The hub does this through the Cloudflare API when it queues every deploy and remove, before the daemon gets the action:

1. It keeps one remotely managed tunnel per machine, named `tender-<machine>`, created on the machine's first `cloudflare` route.
2. It replaces the tunnel's routes with every `cloudflare` route of the machine, each `<host>` to `http://127.0.0.1:<local_port>`, then a `404` for any other host.
3. For each host it keeps a proxied `CNAME` to `<tunnel id>.cfargotunnel.com`, with the comment `managed by tender`, in the zone that is the host's longest matching suffix. For `api.shop.example.com`, that is the zone `shop.example.com` if the account has it, else `example.com`.
4. It deletes only records with the comment `managed by tender` that point at that machine's tunnel. Your own records, and other machines' records, stay.
5. It refuses a host that already has a record it didn't make. That route fails with a sentence naming the host and the route, and stays out of the tunnel's routes. Delete that record in the dashboard, or pick another host.

One route's failure never touches another's. A route the hub can't set in Cloudflare fails only the deploy or remove of its own service, after its containers are done, with the route's sentence in `last_error`; the hub logs it for the other services' deploys. Cloudflare failing as a whole also fails the deploy of a service that drops its last `cloudflare` route. The hub syncs one machine's tunnel at a time, and gives each sync 1 minute; a sync that takes longer stops and fails the service's deploy, so deploy again later. It keeps the tunnel's token encrypted under `TENDER_ENCRYPTION_KEY`, so the daemon gets it without the hub asking Cloudflare again. A remove takes the service's routes out of the tunnel when it is queued; when the remove fails, the service is still there, and its routes go back in. A `cloudflare` route on a hub without the Cloudflare settings is refused with `A cloudflare route needs the hub's Cloudflare settings, and it has none yet.` and how to set them. A host in no zone of the account, a host with a record Tender didn't make, or a record Cloudflare refuses with a `4xx` fails that route alone, and every other route and record stays in place. A failing route's host also keeps its record of Tender's. A `5xx`, a `429` or no answer is tried 3 times, then fails the routes of every service on that machine; deploy again later.

### Require Cloudflare Access

A Cloudflare Access application checks who signs in at Cloudflare's edge. Without more, cloudflared passes on every request the tunnel brings it, so a gap in the edge check, such as a policy or path that leaves part of the host open, reaches the container. A route with `access` adds the check at the origin too: cloudflared checks each request's Access token, the `Cf-Access-Jwt-Assertion` header, against the account's team and the host's applications before it proxies the request. A request without a valid Access token for the host never reaches the container through the tunnel.

The route's port on `127.0.0.1` stays open to processes on the machine itself, as with every route. The check is cloudflared's, so it doesn't cover them.

It is off by default. A route without it works as before.

#### What it needs

1. A self-hosted Access application in the account that protects the route's host, as its domain or one of its public hostnames.
2. Two more permissions on the API token, both Account permissions:
   1. Access: Apps and Policies, Read, to find the applications that protect the host.
   2. Access: Organizations, Identity Providers, and Groups, Read, to read the team name.

#### Add Access to a route

Give the `cloudflare` route a fifth part, the word `access`:

```sh
tender services deploy apps blog -f compose.yaml --route web:80:cloudflare:blog.example.com:access
```

In the API and MCP, the route has `"access": true`. A change of `access` alone is a route change, so the hub syncs the tunnel again. Only a `cloudflare` route can have it. Another route with it is refused, such as `--route "web:80:public:blog.example.com:access": only a cloudflare route has a fifth part, access`.

#### What the hub sets

The hub lists the account's Access applications, `GET /accounts/{account_id}/access/apps`, and takes the `aud` tag of every one whose domain or public hostname is the route's host. It reads the team name from the account's Zero Trust organization, `GET /accounts/{account_id}/access/organizations`, such as `myteam` for `myteam.cloudflareaccess.com`. The route's rule in the tunnel then gets:

```json
{"hostname": "blog.example.com", "service": "http://127.0.0.1:20001", "originRequest": {"access": {"required": true, "teamName": "myteam", "audTag": ["<aud>"]}}}
```

The hub calls them only while a route of the machine has `access`. Without one, it calls neither, and the tunnel's routes are as before.

#### When it fails

A route with `access` fails on its own, and stays out of the tunnel's routes, when the hub can't set it. Its DNS record of Tender's stays, and every other route is set as before.

1. No Access application the API token can see protects the host. Cloudflare shows a token without the two permissions no applications at all, instead of refusing it, so this sentence names them too:

   ```text
   the route to web in blog requires Cloudflare Access, but no Access application the API token can see protects blog.example.com. Add a self-hosted Access application for it, give the API token Access: Apps and Policies Read and Access: Organizations, Identity Providers, and Groups Read, or deploy the route without access
   ```

2. Cloudflare refuses the token with `401` or `403`. The sentence says the token also needs `Access: Apps and Policies Read` and `Access: Organizations, Identity Providers, and Groups Read`.

A `5xx`, a `429` or no answer from either endpoint is Cloudflare failing as a whole, as with the other calls.

### What Tender does on the machine

1. On the first `cloudflare` route it starts cloudflared, service `cloudflared` of the Compose project `tender`, with host networking, running `cloudflared tunnel run`. The image is `cloudflare/cloudflared:2026.11.0`, pinned by digest in Tender.
2. The hub sends the tunnel's token with the deploy. The daemon passes it to cloudflared as `TUNNEL_TOKEN` in the environment, never as an argument or in a file.
3. cloudflared's metrics listen on a loopback port the kernel picks, so they never reach a public address.
4. After the last `cloudflare` route it removes the container. The tunnel stays in Cloudflare, with only its `404`.

### Remove the settings

```sh
tender exposures cloudflare remove
```

```text
Removed the Cloudflare settings. The tunnels and DNS records in Cloudflare stay.
```

It refuses while a service has a `cloudflare` route. The tunnels stay in Cloudflare: delete one under Zero Trust, Networks, Tunnels.

## Next steps

1. [Services](services.md#deploy-a-service) explains how a deploy runs.
2. [REST API](api.md) has the services routes with their `routes`.
