# Exposure

A route makes one container of a service reachable through Tailscale, a Cloudflare tunnel, or Caddy on the public internet.

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

Caddy serves HTTP/1.1 and HTTP/2 only, so UDP 443 stays closed. Its admin API is a Unix socket inside the container, so no process on the machine can reconfigure it through a port. The image is `caddy:2.11.9`, pinned by digest in Tender, and a Tender update moves it.

## Cloudflare

A `cloudflare` route serves `https://<host>` through a Cloudflare tunnel. The machine dials out to Cloudflare, so it needs no public address and no inbound port.

### Prerequisites

1. The host's domain is a zone in your Cloudflare account, on Cloudflare's DNS.
2. A Cloudflare API token with these permissions, for the account and its zones, or the zones you use:
   1. Account, Cloudflare Tunnel, Edit, to create the machine's tunnel and set its routes.
   2. Zone, DNS, Edit, to keep a DNS record for each host.
   3. Zone, Zone, Read, to find the zone of each host.
3. Outbound access from the machine to Cloudflare on port 7844, TCP and UDP, which cloudflared uses.

### Set up Cloudflare

1. In the Cloudflare dashboard, open My Profile, then API Tokens, and create a custom token with the three permissions above. Save it in a file, such as `cloudflare-token`.
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

The hub does this through the Cloudflare API after every deploy and remove:

1. It keeps one remotely managed tunnel per machine, named `tender-<machine>`, created on the machine's first `cloudflare` route.
2. It replaces the tunnel's routes with every `cloudflare` route of the machine, each `<host>` to `http://127.0.0.1:<local_port>`, then a `404` for any other host.
3. For each host it keeps a proxied `CNAME` to `<tunnel id>.cfargotunnel.com`, with the comment `managed by tender`, in the zone that is the host's longest matching suffix. For `api.shop.example.com`, that is the zone `shop.example.com` if the account has it, else `example.com`.
4. It deletes only records with the comment `managed by tender` that point at that machine's tunnel. Your own records, and other machines' records, stay.
5. It refuses a host that already has a record it didn't make, and the deploy fails with a sentence naming the host. Delete that record in the dashboard, or pick another host.

A Cloudflare error that says the request is wrong, such as a `403` for a token without a permission, fails the deploy at once with Cloudflare's reason. A `5xx`, a `429` or no answer is tried 3 times, then fails the deploy; deploy again later.

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
