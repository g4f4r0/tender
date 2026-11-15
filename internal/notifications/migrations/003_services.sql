-- Alerts about a Docker service name it, so two services on one machine have two alerts.
ALTER TABLE notifications ADD COLUMN service TEXT;

DROP INDEX notifications_unresolved;
CREATE UNIQUE INDEX notifications_unresolved
  ON notifications (type, coalesce(machine_id, 0), coalesce(service, '')) WHERE status != 'resolved';

-- escalated says the alert's one "now critical" email went out, so a severity that flaps doesn't
-- send another.
ALTER TABLE notifications ADD COLUMN escalated INTEGER NOT NULL DEFAULT 0 CHECK (escalated IN (0, 1));
