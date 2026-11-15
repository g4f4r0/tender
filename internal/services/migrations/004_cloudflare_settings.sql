-- The hub's Cloudflare settings for the services' cloudflare routes, one row: the account the
-- tunnels and DNS records are in, and its API token, sealed with TENDER_ENCRYPTION_KEY.
CREATE TABLE service_cloudflare_settings (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  account_id TEXT NOT NULL,
  api_token  BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
