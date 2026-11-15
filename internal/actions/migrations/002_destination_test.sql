-- A test action tries a backup destination from the machine: destination names it. The checks on
-- type move to the code, so a new type of action needs no schema change; nothing references this
-- table, so it is rebuilt.
CREATE TABLE actions_new (
  id             INTEGER PRIMARY KEY,
  machine_id     INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  type           TEXT NOT NULL,
  service        TEXT,
  unit           TEXT,
  priority       TEXT CHECK (priority IN ('emerg', 'alert', 'crit', 'err', 'warning', 'notice', 'info', 'debug')),
  since          TEXT,
  lines          INTEGER CHECK (lines BETWEEN 1 AND 2000),
  destination    TEXT,
  status         TEXT NOT NULL CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
  output         TEXT,
  output_cleared INTEGER NOT NULL DEFAULT 0 CHECK (output_cleared IN (0, 1)),
  handed_out_at  INTEGER,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
) STRICT;

INSERT INTO actions_new (id, machine_id, type, service, unit, priority, since, lines, status, output, output_cleared, handed_out_at, created_at, updated_at)
  SELECT id, machine_id, type, service, unit, priority, since, lines, status, output, output_cleared, handed_out_at, created_at, updated_at FROM actions;

DROP TABLE actions;
ALTER TABLE actions_new RENAME TO actions;
CREATE INDEX actions_machine ON actions (machine_id, status, id);
