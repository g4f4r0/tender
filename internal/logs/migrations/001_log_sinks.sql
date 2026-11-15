-- A log sink is where daemons send their logs. config is JSON; each of the type's secret fields
-- holds the field's JSON value sealed (nonce || AES-256-GCM) in hex. sources says which logs go:
-- {"journal": {"units": [], "min_priority": "info"}, "services": {"names": []}}, where an empty
-- list means all and a source left out sends nothing. machines is a JSON list of machine names;
-- an empty list means every machine.
CREATE TABLE log_sinks (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  type       TEXT NOT NULL,
  config     TEXT NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  sources    TEXT NOT NULL,
  machines   TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

-- What each machine's daemon last said about sending to a sink. The logs themselves never reach
-- the hub. records_sent and records_dropped count since the daemon first sent to the sink.
-- failing_since is when sending started to fail, null while it works; last_dropped_at is when the
-- daemon last dropped records, because its buffer was full or the sink refused them for good.
CREATE TABLE log_sink_status (
  id              INTEGER PRIMARY KEY,
  sink_id         INTEGER NOT NULL REFERENCES log_sinks(id) ON DELETE CASCADE,
  machine_id      INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  records_sent    INTEGER NOT NULL CHECK (records_sent >= 0),
  records_dropped INTEGER NOT NULL CHECK (records_dropped >= 0),
  last_sent_at    INTEGER,
  last_error      TEXT,
  failing_since   INTEGER,
  last_dropped_at INTEGER,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  UNIQUE (sink_id, machine_id)
) STRICT;
