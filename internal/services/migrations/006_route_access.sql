-- access says a cloudflare route requires Cloudflare Access at the origin: the machine's tunnel
-- checks each request's Access token before it reaches the container. Only a cloudflare route can
-- have it; every route before it had none.
ALTER TABLE service_routes ADD COLUMN access INTEGER NOT NULL DEFAULT 0 CHECK (access IN (0, 1) AND (access = 0 OR expose = 'cloudflare'));
