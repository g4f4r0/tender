CREATE TABLE idempotency_keys (
  id            INTEGER PRIMARY KEY,
  machine_id    INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  key           TEXT NOT NULL,
  method        TEXT NOT NULL,
  path          TEXT NOT NULL,
  status_code   INTEGER NOT NULL,
  response_body BLOB NOT NULL,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  UNIQUE (machine_id, key)
) STRICT;
