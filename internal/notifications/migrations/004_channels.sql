-- Recipients become channels: a notification goes to each enabled channel, of a type such as email,
-- slack, webhook or exec, whose config is JSON with its secrets encrypted by the hub. The type
-- has no CHECK, so a new built-in type needs no schema change.
CREATE TABLE notification_channels (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  type         TEXT NOT NULL,
  config       TEXT NOT NULL CHECK (json_valid(config)),
  enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  min_severity TEXT NOT NULL DEFAULT 'warning' CHECK (min_severity IN ('info', 'warning', 'critical')),
  -- The event names it gets, as a JSON list of patterns: "*", an area such as "service.*", or
  -- an event such as "backup.failed".
  events       TEXT NOT NULL DEFAULT '["*"]' CHECK (json_valid(events) AND json_type(events) = 'array'),
  -- When a real try to deliver to it first failed since its last success, or since it was turned
  -- on, with that try's error; null while it delivers. channel.failing reads them.
  failing_since INTEGER,
  last_error    TEXT,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
) STRICT;

-- Each recipient becomes an email channel named after its address, lowercased, with every
-- character other than a letter or a digit turned into a dash: ops@example.com is ops-example-com.
-- A name two addresses share gets the later one's id after it. A recipient got every alert, so
-- its channel's min_severity is info and it gets every event.
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

-- A notification is one message, added and never changed but for its status: pending until a
-- channel delivers it (sent) or every channel's delivery of it fails (failed). Its event is
-- dotted: a problem starts as <area>.<condition>, such as service.down, gets worse as
-- service.down.escalated and clears as service.down.resolved. base is the event without
-- .escalated or .resolved. A problem is open while the latest notification of its base, machine,
-- service and channel is not a .resolved one. channel names the channel a channel.failing
-- notification is about.
--
-- A v0.2 alert becomes the notification it opened with, with its status; a resolved one also
-- gets its .resolved notification. Both were emailed. The ids follow the time, so the latest
-- notification of a problem is the one with the highest id.
CREATE TEMP TABLE v02 AS
SELECT row_number() OVER (ORDER BY ts, old_id, phase) AS id, phase, ts, machine_id, service, type, severity, status, content, created_at, updated_at
FROM (
  SELECT id AS old_id, 0 AS phase, created_at AS ts, machine_id, service, type, severity, status, content, created_at, updated_at FROM notifications
  UNION ALL
  SELECT id, 1, updated_at, machine_id, service, type, severity, status, content, created_at, updated_at FROM notifications WHERE status = 'resolved'
);

CREATE TABLE notifications_new (
  id         INTEGER PRIMARY KEY,
  machine_id INTEGER REFERENCES machines(id) ON DELETE CASCADE,
  service    TEXT,
  channel    TEXT,
  event      TEXT NOT NULL,
  severity   TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
  message    TEXT NOT NULL,
  status     TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
  sent_at    INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  base       TEXT NOT NULL GENERATED ALWAYS AS (CASE
               WHEN event LIKE '%.resolved' THEN substr(event, 1, length(event) - 9)
               WHEN event LIKE '%.escalated' THEN substr(event, 1, length(event) - 10)
               ELSE event END) VIRTUAL,
  CHECK ((status = 'sent') = (sent_at IS NOT NULL))
) STRICT;

-- v0.2's types are its event names with the first underscore a dot: machine_silent is
-- machine.silent, service_memory_high is service.memory_high.
INSERT INTO notifications_new (id, machine_id, service, channel, event, severity, message, status, sent_at, created_at, updated_at)
SELECT id, machine_id, service, NULL,
       CASE WHEN instr(type, '_') > 0 THEN substr(type, 1, instr(type, '_') - 1) || '.' || substr(type, instr(type, '_') + 1)
            ELSE type || '.event' END || CASE phase WHEN 1 THEN '.resolved' ELSE '' END,
       severity,
       CASE phase WHEN 1 THEN 'Resolved: ' || content ELSE content END,
       CASE status WHEN 'pending' THEN 'pending' ELSE 'sent' END,
       CASE WHEN status = 'pending' THEN NULL WHEN phase = 1 OR status = 'sent' THEN updated_at ELSE created_at END,
       ts, CASE WHEN phase = 0 AND status = 'resolved' THEN created_at ELSE updated_at END
FROM temp.v02
ORDER BY id;

DROP TABLE notifications;
ALTER TABLE notifications_new RENAME TO notifications;

-- The open problems, and the notifications of one, come from this index.
CREATE INDEX notifications_problem
  ON notifications (base, coalesce(machine_id, 0), coalesce(service, ''), coalesce(channel, ''), id);
CREATE INDEX notifications_created ON notifications (created_at, id);

-- One row per notification and channel it goes to. The next try is due a backoff after
-- updated_at, set at each try, so the backoff survives a restart.
CREATE TABLE notification_deliveries (
  id              INTEGER PRIMARY KEY,
  notification_id INTEGER NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
  channel_id      INTEGER NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  status          TEXT NOT NULL CHECK (status IN ('pending', 'delivered', 'failed')),
  attempts        INTEGER NOT NULL DEFAULT 0,
  error           TEXT,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
) STRICT;

CREATE INDEX notification_deliveries_notification ON notification_deliveries (notification_id);
CREATE INDEX notification_deliveries_channel ON notification_deliveries (channel_id, status);

-- A v0.2 alert still open that was emailed reached every recipient, so their channels get its
-- .resolved notification when it clears.
INSERT INTO notification_deliveries (notification_id, channel_id, status, attempts, created_at, updated_at)
SELECT v.id, c.id, 'delivered', 1, v.created_at, v.updated_at
FROM temp.v02 v CROSS JOIN notification_channels c
WHERE v.status = 'sent' AND v.phase = 0
ORDER BY v.id, c.id;

DROP TABLE temp.v02;
