CREATE TABLE machines (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  token_hash BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE machine_usage (
  id                  INTEGER PRIMARY KEY,
  machine_id          INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  cpu_load            REAL,
  memory_used_percent REAL,
  disk_used_percent   REAL,
  uptime_seconds      INTEGER,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
) STRICT;

CREATE INDEX machine_usage_machine ON machine_usage (machine_id, created_at DESC);
