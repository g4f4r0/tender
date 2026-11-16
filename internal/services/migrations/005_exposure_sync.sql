-- What the hub keeps of each machine's exposures between deploys. last_local_port is the local port
-- the hub assigned last on the machine: the next route gets the next free one after it, so a port a
-- failed deploy freed isn't handed out again while old exposures may still point at it.
-- cloudflare_tunnel_token is the token of the machine's Cloudflare tunnel from its last sync, sealed
-- like a service's env values, in hex; the daemon runs cloudflared with it.
ALTER TABLE service_exposures ADD COLUMN last_local_port INTEGER CHECK (last_local_port BETWEEN 20000 AND 29999);
ALTER TABLE service_exposures ADD COLUMN cloudflare_tunnel_token TEXT;

-- cloudflare_error is why the hub's sync of the machine's tunnel, when the service's deploy or remove
-- was queued, couldn't route the service's cloudflare routes; null when it could.
ALTER TABLE services ADD COLUMN cloudflare_error TEXT;
