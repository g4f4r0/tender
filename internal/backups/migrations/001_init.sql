CREATE TABLE backup_destinations (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  type       TEXT NOT NULL CHECK (type IN ('s3')),
  endpoint   TEXT NOT NULL,
  bucket     TEXT NOT NULL,
  path       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (endpoint, bucket, path)
) STRICT;

CREATE TABLE backup_settings (
  id             INTEGER PRIMARY KEY,
  machine_id     INTEGER NOT NULL UNIQUE REFERENCES machines(id) ON DELETE CASCADE,
  destination_id INTEGER NOT NULL REFERENCES backup_destinations(id) ON DELETE RESTRICT,
  schedule       TEXT NOT NULL,
  retention_days INTEGER NOT NULL CHECK (retention_days > 0),
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
) STRICT;

CREATE TABLE backup_snapshots (
  id         INTEGER PRIMARY KEY,
  machine_id INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  restic_id  TEXT NOT NULL,
  size_bytes INTEGER,
  paths      TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (machine_id, restic_id)
) STRICT;

CREATE INDEX backup_snapshots_machine ON backup_snapshots (machine_id, created_at DESC);

CREATE TABLE backup_history (
  id          INTEGER PRIMARY KEY,
  machine_id  INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  snapshot_id INTEGER REFERENCES backup_snapshots(id) ON DELETE SET NULL,
  type        TEXT NOT NULL CHECK (type IN ('backup', 'cleanup', 'check')),
  status      TEXT NOT NULL CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
  error       TEXT,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX backup_history_machine ON backup_history (machine_id, created_at DESC);
