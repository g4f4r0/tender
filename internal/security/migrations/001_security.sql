-- Each machine's latest security report, as its daemon collected it, and the one before, for the
-- rule that an alert opens only when two reports in a row find the problem. firewall, listeners,
-- ssh, updates and reboot hold each check's JSON. security_updates_since is since when the reports
-- have found security updates pending, or null.
CREATE TABLE security_states (
  id                     INTEGER PRIMARY KEY,
  machine_id             INTEGER NOT NULL UNIQUE REFERENCES machines(id) ON DELETE CASCADE,
  firewall               TEXT NOT NULL,
  listeners              TEXT NOT NULL,
  ssh                    TEXT NOT NULL,
  updates                TEXT NOT NULL,
  reboot                 TEXT NOT NULL,
  previous               TEXT,
  security_updates_since INTEGER,
  collected_at           INTEGER NOT NULL,
  created_at             INTEGER NOT NULL,
  updated_at             INTEGER NOT NULL
) STRICT;
