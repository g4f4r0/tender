CREATE TABLE notifications (
  id         INTEGER PRIMARY KEY,
  machine_id INTEGER REFERENCES machines(id) ON DELETE CASCADE,
  type       TEXT NOT NULL,
  severity   TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
  status     TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'resolved')),
  content    TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

-- One unresolved notification per type and machine. coalesce makes machine-less ones unique too.
CREATE UNIQUE INDEX notifications_unresolved
  ON notifications (type, coalesce(machine_id, 0)) WHERE status != 'resolved';
