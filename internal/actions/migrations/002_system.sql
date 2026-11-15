-- Two actions on the machine itself: update, which installs a Tender release (version, such as
-- 0.3.1), and upgrade, which upgrades the system's packages (packages: security or all). Neither
-- has a service. SQLite can't change a CHECK constraint, so the table is made again.
CREATE TABLE actions_new (
  id             INTEGER PRIMARY KEY,
  machine_id     INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  type           TEXT NOT NULL CHECK (type IN ('start', 'stop', 'restart', 'logs', 'update', 'upgrade')),
  service        TEXT,
  unit           TEXT,
  priority       TEXT CHECK (priority IN ('emerg', 'alert', 'crit', 'err', 'warning', 'notice', 'info', 'debug')),
  since          TEXT,
  lines          INTEGER CHECK (lines BETWEEN 1 AND 2000),
  version        TEXT,
  packages       TEXT CHECK (packages IN ('security', 'all')),
  status         TEXT NOT NULL CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
  output         TEXT,
  output_cleared INTEGER NOT NULL DEFAULT 0 CHECK (output_cleared IN (0, 1)),
  handed_out_at  INTEGER,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  CHECK (service IS NOT NULL OR type IN ('logs', 'update', 'upgrade')),
  CHECK ((type = 'update') = (version IS NOT NULL)),
  CHECK ((type = 'upgrade') = (packages IS NOT NULL))
) STRICT;

INSERT INTO actions_new (id, machine_id, type, service, unit, priority, since, lines, status, output, output_cleared,
                         handed_out_at, created_at, updated_at)
SELECT id, machine_id, type, service, unit, priority, since, lines, status, output, output_cleared,
       handed_out_at, created_at, updated_at FROM actions;

DROP TABLE actions;
ALTER TABLE actions_new RENAME TO actions;
CREATE INDEX actions_machine ON actions (machine_id, status, id);
