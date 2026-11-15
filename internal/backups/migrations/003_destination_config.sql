-- A destination is a type and a config, so a new type needs no schema change. config is JSON without
-- the secrets. secrets is a JSON object of each secret field's sealed value (nonce || AES-256-GCM),
-- in hex. location is the restic repository without a machine's folder; two destinations can't share
-- one. Every existing destination is s3.
--
-- backup_settings points at backup_destinations with ON DELETE RESTRICT, so dropping the old table
-- while settings point at it would fail. Both tables are rebuilt: settings first point at the new
-- destinations table, and renaming that table moves the reference along.
CREATE TABLE backup_destinations_new (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  type       TEXT NOT NULL,
  config     TEXT NOT NULL,
  secrets    TEXT NOT NULL,
  password   BLOB NOT NULL,
  location   TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

INSERT INTO backup_destinations_new
  SELECT id, name, type,
    json_object('endpoint', endpoint, 'bucket', bucket, 'path', path),
    json_object('access_key_id', hex(access_key_id), 'secret_access_key', hex(secret_access_key)),
    password,
    's3:' || endpoint || '/' || bucket || CASE WHEN path = '' THEN '' ELSE '/' || path END,
    created_at, updated_at
  FROM backup_destinations;

CREATE TABLE backup_settings_new (
  id             INTEGER PRIMARY KEY,
  machine_id     INTEGER NOT NULL UNIQUE REFERENCES machines(id) ON DELETE CASCADE,
  destination_id INTEGER NOT NULL REFERENCES backup_destinations_new(id) ON DELETE RESTRICT,
  schedule       TEXT NOT NULL,
  retention_days INTEGER NOT NULL CHECK (retention_days > 0),
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
) STRICT;

INSERT INTO backup_settings_new SELECT id, machine_id, destination_id, schedule, retention_days, created_at, updated_at FROM backup_settings;

DROP TABLE backup_settings;
DROP TABLE backup_destinations;
ALTER TABLE backup_destinations_new RENAME TO backup_destinations;
ALTER TABLE backup_settings_new RENAME TO backup_settings;
