-- version is the Tender version the machine's daemon last reported with its usage, or null before
-- a daemon from v0.3.0 on reported. updates says whether the machine follows the hub's update mode
-- (inherit), or updates automatically (auto) or only when asked (manual). security_allowed_ports is
-- a JSON list of the ports that may listen on a public address, such as ["41641/udp"], and
-- security_alerts turns the machine's security alerts off and on.
ALTER TABLE machines ADD COLUMN version TEXT;
ALTER TABLE machines ADD COLUMN updates TEXT NOT NULL DEFAULT 'inherit' CHECK (updates IN ('inherit', 'auto', 'manual'));
ALTER TABLE machines ADD COLUMN security_allowed_ports TEXT NOT NULL DEFAULT '[]';
ALTER TABLE machines ADD COLUMN security_alerts INTEGER NOT NULL DEFAULT 1 CHECK (security_alerts IN (0, 1));
