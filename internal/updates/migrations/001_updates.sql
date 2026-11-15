-- The hub's update settings, one row: mode is auto or manual, and "window" is when auto mode may
-- start a rollout, such as 02:00-05:00 UTC, or null for any time. The latest_ columns are what the
-- hub last learned from GitHub: the newest stable release, whether auto mode may roll it out (its
-- tender-release.json can say no), when it asked, and the error of that check.
CREATE TABLE update_settings (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  mode              TEXT NOT NULL DEFAULT 'auto' CHECK (mode IN ('auto', 'manual')),
  "window"          TEXT,
  latest_version    TEXT,
  latest_auto       INTEGER NOT NULL DEFAULT 1 CHECK (latest_auto IN (0, 1)),
  latest_checked_at INTEGER,
  latest_error      TEXT,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL
) STRICT;

INSERT INTO update_settings (id, created_at, updated_at) VALUES (1, unixepoch(), unixepoch());

-- A rollout updates its machines to one Tender release, one at a time in position order, and stops
-- at the first that fails. auto says auto mode started it.
CREATE TABLE update_rollouts (
  id         INTEGER PRIMARY KEY,
  version    TEXT NOT NULL,
  status     TEXT NOT NULL CHECK (status IN ('in_progress', 'completed', 'failed')),
  auto       INTEGER NOT NULL CHECK (auto IN (0, 1)),
  error      TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

-- One machine of a rollout. action_id is its update action, once queued.
CREATE TABLE update_rollout_machines (
  id         INTEGER PRIMARY KEY,
  rollout_id INTEGER NOT NULL REFERENCES update_rollouts(id) ON DELETE CASCADE,
  machine_id INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  position   INTEGER NOT NULL,
  status     TEXT NOT NULL CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
  action_id  INTEGER,
  error      TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (rollout_id, machine_id)
) STRICT;

CREATE INDEX update_rollout_machines_machine ON update_rollout_machines (machine_id, id);
