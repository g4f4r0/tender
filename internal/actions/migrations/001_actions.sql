-- What people and AI clients asked a machine's daemon to do: start, stop or restart a service, or
-- read logs, a service's (service set) or the machine's journal (service null). An action keeps
-- the service's name, so it reads well after the service is gone. since is a duration such as 1h,
-- as it was asked. output is the logs, or why the action failed; a logs action's output is cleared
-- some time after it finished, because logs can hold secrets, and output_cleared says so.
CREATE TABLE actions (
  id             INTEGER PRIMARY KEY,
  machine_id     INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  type           TEXT NOT NULL CHECK (type IN ('start', 'stop', 'restart', 'logs')),
  service        TEXT,
  unit           TEXT,
  priority       TEXT CHECK (priority IN ('emerg', 'alert', 'crit', 'err', 'warning', 'notice', 'info', 'debug')),
  since          TEXT,
  lines          INTEGER CHECK (lines BETWEEN 1 AND 2000),
  status         TEXT NOT NULL CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
  output         TEXT,
  output_cleared INTEGER NOT NULL DEFAULT 0 CHECK (output_cleared IN (0, 1)),
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  CHECK (service IS NOT NULL OR type = 'logs')
) STRICT;

CREATE INDEX actions_machine ON actions (machine_id, status, id);
