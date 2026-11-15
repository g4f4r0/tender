-- Recipients become channels: an alert goes to each enabled channel, of a type such as email,
-- slack, webhook or exec, whose config is JSON with its secrets encrypted by the hub. The type
-- has no CHECK, so a new built-in type needs no schema change.
CREATE TABLE notification_channels (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  type         TEXT NOT NULL,
  config       TEXT NOT NULL CHECK (json_valid(config)),
  enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  min_severity TEXT NOT NULL DEFAULT 'warning' CHECK (min_severity IN ('info', 'warning', 'critical')),
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
) STRICT;

-- Each recipient becomes an email channel named after its address, lowercased, with every
-- character other than a letter or a digit turned into a dash: ops@example.com is ops-example-com.
-- A name two addresses share gets the later one's id after it. A recipient got every alert, so
-- its channel's min_severity is info.
WITH RECURSIVE spelled(id, email, i, name) AS (
  SELECT id, lower(email), 1, '' FROM notification_recipients
  UNION ALL
  SELECT id, email, i + 1,
         name || CASE WHEN substr(email, i, 1) GLOB '[a-z0-9]' THEN substr(email, i, 1) ELSE '-' END
  FROM spelled WHERE i <= length(email)
),
named(id, name) AS (
  SELECT id, substr(trim(name, '-'), 1, 50) FROM spelled WHERE i = length(email) + 1
)
INSERT INTO notification_channels (name, type, config, enabled, min_severity, created_at, updated_at)
SELECT CASE
         WHEN n.name = '' THEN 'email-' || r.id
         WHEN EXISTS (SELECT 1 FROM named o WHERE o.name = n.name AND o.id < n.id) THEN n.name || '-' || r.id
         ELSE n.name
       END,
       'email', json_object('to', r.email), 1, 'info', r.created_at, r.updated_at
FROM named n JOIN notification_recipients r ON r.id = n.id
ORDER BY r.id;

DROP TABLE notification_recipients;

-- One row per event an alert owes a channel. notification_id is null for a test. The next try
-- is due a backoff after updated_at, set at each try, so the backoff survives a restart.
CREATE TABLE notification_deliveries (
  id              INTEGER PRIMARY KEY,
  notification_id INTEGER REFERENCES notifications(id) ON DELETE CASCADE,
  channel_id      INTEGER NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  event           TEXT NOT NULL CHECK (event IN ('opened', 'escalated', 'resolved', 'test')),
  status          TEXT NOT NULL CHECK (status IN ('pending', 'delivered', 'failed')),
  attempts        INTEGER NOT NULL DEFAULT 0,
  error           TEXT,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
) STRICT;

CREATE INDEX notification_deliveries_notification ON notification_deliveries (notification_id);
CREATE INDEX notification_deliveries_channel ON notification_deliveries (channel_id, status);

-- channel_failing is about a channel, not a machine: it names the channel, so two failing
-- channels have two alerts.
ALTER TABLE notifications ADD COLUMN channel TEXT;

DROP INDEX notifications_unresolved;
CREATE UNIQUE INDEX notifications_unresolved
  ON notifications (type, coalesce(machine_id, 0), coalesce(service, ''), coalesce(channel, '')) WHERE status != 'resolved';
