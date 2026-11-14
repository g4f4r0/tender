-- Destinations hold their credentials, encrypted by the hub (nonce || AES-256-GCM). No rows exist in
-- any install yet, so the table is rebuilt, which puts the new columns before the timestamps.
-- backup_settings points at this table by name and keeps pointing at the rebuilt one.
CREATE TABLE backup_destinations_new (
  id                INTEGER PRIMARY KEY,
  name              TEXT NOT NULL UNIQUE,
  type              TEXT NOT NULL CHECK (type IN ('s3')),
  endpoint          TEXT NOT NULL,
  bucket            TEXT NOT NULL,
  path              TEXT NOT NULL DEFAULT '',
  access_key_id     BLOB NOT NULL,
  secret_access_key BLOB NOT NULL,
  password          BLOB NOT NULL,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  UNIQUE (endpoint, bucket, path)
) STRICT;

INSERT INTO backup_destinations_new
  SELECT id, name, type, endpoint, bucket, path, x'', x'', x'', created_at, updated_at FROM backup_destinations;

DROP TABLE backup_destinations;
ALTER TABLE backup_destinations_new RENAME TO backup_destinations;
