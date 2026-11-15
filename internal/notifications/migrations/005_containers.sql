-- A service is a compose project now, made of containers. A problem of one container names its
-- service in service and the container in container; a problem of the whole service, such as a
-- failed deploy, leaves container null.
ALTER TABLE notifications ADD COLUMN container TEXT;

DROP INDEX notifications_problem;
CREATE INDEX notifications_problem
  ON notifications (base, coalesce(machine_id, 0), coalesce(service, ''), coalesce(container, ''), coalesce(channel, ''), id);
