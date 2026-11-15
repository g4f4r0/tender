-- The Docker engine of each machine whose daemon reports services: running, or unreachable when
-- its socket refuses connections. unreachable_reports counts those reports in a row.
CREATE TABLE service_engines (
  id                  INTEGER PRIMARY KEY,
  machine_id          INTEGER NOT NULL UNIQUE REFERENCES machines(id) ON DELETE CASCADE,
  docker              TEXT NOT NULL CHECK (docker IN ('running', 'unreachable')),
  unreachable_reports INTEGER NOT NULL DEFAULT 0,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
) STRICT;

-- One row per container, keyed by its name, so settings such as alerts_enabled follow the name
-- when the container is made again. The *_reports columns count the reports in a row in which
-- the container was down, restarting or unhealthy; the alert rules read them. state is unknown
-- while the machine's Docker is unreachable.
CREATE TABLE services (
  id                 INTEGER PRIMARY KEY,
  machine_id         INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  name               TEXT NOT NULL,
  project            TEXT,
  container_id       TEXT NOT NULL,
  image              TEXT NOT NULL,
  state              TEXT NOT NULL CHECK (state IN ('created', 'running', 'paused', 'restarting', 'exited', 'removing', 'dead', 'unknown')),
  health             TEXT CHECK (health IN ('healthy', 'unhealthy', 'starting')),
  exit_code          INTEGER,
  restart_count      INTEGER NOT NULL CHECK (restart_count >= 0),
  restart_policy     TEXT NOT NULL DEFAULT 'no' CHECK (restart_policy IN ('no', 'always', 'unless-stopped', 'on-failure')),
  started_at         INTEGER,
  alerts_enabled     INTEGER NOT NULL DEFAULT 1 CHECK (alerts_enabled IN (0, 1)),
  stopped_by_tender  INTEGER NOT NULL DEFAULT 0 CHECK (stopped_by_tender IN (0, 1)),
  down_reports       INTEGER NOT NULL DEFAULT 0,
  restarting_reports INTEGER NOT NULL DEFAULT 0,
  unhealthy_reports  INTEGER NOT NULL DEFAULT 0,
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  UNIQUE (machine_id, name)
) STRICT;

CREATE TABLE service_actions (
  id         INTEGER PRIMARY KEY,
  machine_id INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  type       TEXT NOT NULL CHECK (type IN ('start', 'stop', 'restart', 'logs')),
  status     TEXT NOT NULL CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
  lines      INTEGER CHECK (lines BETWEEN 1 AND 2000),
  output     TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX service_actions_machine ON service_actions (machine_id, status, id);
