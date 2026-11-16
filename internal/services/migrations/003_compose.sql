-- A service is a compose project on a machine, made of containers; a container without a compose
-- project is its own service, named after it. The containers are the rows services held until now.

ALTER TABLE services RENAME TO containers_v03;

-- managed says Tender deployed the service: compose, files and env are what it deploys, and
-- deploy_status is deploying or removing while an action for it waits or runs, then deployed, or
-- failed with last_error saying why. An unmanaged service, which Tender only found running, has
-- none of them; the hub makes it from the reports, and deletes it once its last container is gone.
-- files is a JSON object of relative path to text, for config files the compose mounts. env is a
-- JSON object of key to its value sealed (nonce || AES-256-GCM) in hex; the daemon writes it to the
-- service's .env. deployed_at is when a deploy last succeeded. action_id is the service's latest
-- deploy or remove in the actions domain (no foreign key: it is another domain's table), and
-- action_type which of the two it is; deploy_status follows that action. alerts_enabled is one
-- switch for every container of the service.
CREATE TABLE services (
  id             INTEGER PRIMARY KEY,
  machine_id     INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  managed        INTEGER NOT NULL DEFAULT 0 CHECK (managed IN (0, 1)),
  compose        TEXT,
  files          TEXT NOT NULL DEFAULT '{}',
  env            TEXT NOT NULL DEFAULT '{}',
  deploy_status  TEXT CHECK (deploy_status IN ('deploying', 'deployed', 'failed', 'removing')),
  last_error     TEXT,
  deployed_at    INTEGER,
  action_id      INTEGER,
  action_type    TEXT CHECK (action_type IN ('deploy', 'remove')),
  alerts_enabled INTEGER NOT NULL DEFAULT 1 CHECK (alerts_enabled IN (0, 1)),
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  UNIQUE (machine_id, name),
  CHECK (managed = (compose IS NOT NULL)),
  CHECK (managed = (deploy_status IS NOT NULL))
) STRICT;

-- One row per container of a service, by its name in the service: its compose service name, such as
-- web, with -2 and up for the second and later replicas, or its container name for a lone container.
-- The other columns are as before: the *_reports columns count the reports in a row in which the
-- container was down, restarting, unhealthy or near its memory limit, state is unknown while the
-- machine's Docker is unreachable and removed once a report no longer has the container, and the row
-- is deleted 7 days after removed_at.
CREATE TABLE containers (
  id                  INTEGER PRIMARY KEY,
  machine_id          INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  service_id          INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  name                TEXT NOT NULL,
  runtime             TEXT NOT NULL DEFAULT 'docker',
  container_id        TEXT NOT NULL,
  image               TEXT NOT NULL,
  state               TEXT NOT NULL CHECK (state IN ('created', 'running', 'paused', 'restarting', 'exited', 'removing', 'dead', 'unknown', 'removed')),
  health              TEXT CHECK (health IN ('healthy', 'unhealthy', 'starting')),
  exit_code           INTEGER,
  restart_count       INTEGER NOT NULL CHECK (restart_count >= 0),
  restart_policy      TEXT NOT NULL DEFAULT 'no' CHECK (restart_policy IN ('no', 'always', 'unless-stopped', 'on-failure')),
  started_at          INTEGER,
  memory_bytes        INTEGER CHECK (memory_bytes >= 0),
  memory_limit_bytes  INTEGER CHECK (memory_limit_bytes > 0),
  cpu_percent         REAL CHECK (cpu_percent >= 0),
  stopped_by_tender   INTEGER NOT NULL DEFAULT 0 CHECK (stopped_by_tender IN (0, 1)),
  down_reports        INTEGER NOT NULL DEFAULT 0,
  restarting_reports  INTEGER NOT NULL DEFAULT 0,
  unhealthy_reports   INTEGER NOT NULL DEFAULT 0,
  memory_high_reports INTEGER NOT NULL DEFAULT 0,
  removed_at          INTEGER,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL,
  UNIQUE (service_id, name)
) STRICT;

CREATE INDEX containers_machine ON containers (machine_id);

-- Each container's service: its compose project, or itself without one. The service's alerts are
-- off only when every one of its containers had them off.
INSERT INTO services (machine_id, name, alerts_enabled, created_at, updated_at)
SELECT machine_id, coalesce(project, name), max(alerts_enabled), min(created_at), max(updated_at)
FROM containers_v03 GROUP BY machine_id, coalesce(project, name);

-- A container in a project is named as the hub names it from a report: myapp-web-1 is web, and
-- myapp-web-2 is web-2. A name that doesn't start with its project's stays whole. Two containers
-- that would get one name keep it apart with the start of their container id.
WITH named AS (
  SELECT c.*, s.id AS sid,
    CASE
      WHEN c.project IS NULL OR substr(c.name, 1, length(c.project) + 1) != c.project || '-' THEN c.name
      WHEN substr(c.name, length(c.project) + 2) LIKE '%-1' THEN substr(c.name, length(c.project) + 2, length(c.name) - length(c.project) - 3)
      ELSE substr(c.name, length(c.project) + 2)
    END AS short
  FROM containers_v03 c JOIN services s ON s.machine_id = c.machine_id AND s.name = coalesce(c.project, c.name)
), numbered AS (
  SELECT *, row_number() OVER (PARTITION BY sid, short ORDER BY id) AS n FROM named
)
INSERT INTO containers (id, machine_id, service_id, name, runtime, container_id, image, state, health, exit_code, restart_count,
  restart_policy, started_at, memory_bytes, memory_limit_bytes, cpu_percent, stopped_by_tender, down_reports, restarting_reports,
  unhealthy_reports, memory_high_reports, removed_at, created_at, updated_at)
SELECT id, machine_id, sid, CASE WHEN n = 1 AND short != '' THEN short ELSE short || '-' || substr(container_id, 1, 12) END,
  runtime, container_id, image, state, health, exit_code, restart_count, restart_policy, started_at, memory_bytes, memory_limit_bytes,
  cpu_percent, stopped_by_tender, down_reports, restarting_reports, unhealthy_reports, memory_high_reports, removed_at, created_at, updated_at
FROM numbered;

DROP TABLE containers_v03;

-- A route makes one container port of a managed service reachable: over the machine's tailnet name
-- (tailscale, on https_port), or at host through a Cloudflare tunnel (cloudflare) or the machine's
-- own Caddy (public). container is a service in the compose file. It is published on
-- 127.0.0.1:local_port only, which the hub assigns per machine from 20000 to 29999. A host is unique
-- across the hub; the code keeps local_port and https_port unique per machine.
CREATE TABLE service_routes (
  id         INTEGER PRIMARY KEY,
  service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  container  TEXT NOT NULL,
  port       INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
  expose     TEXT NOT NULL CHECK (expose IN ('tailscale', 'cloudflare', 'public')),
  host       TEXT,
  https_port INTEGER CHECK (https_port BETWEEN 1 AND 65535),
  local_port INTEGER NOT NULL CHECK (local_port BETWEEN 20000 AND 29999),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK ((expose = 'tailscale') = (host IS NULL)),
  CHECK ((expose = 'tailscale') = (https_port IS NOT NULL))
) STRICT;

CREATE UNIQUE INDEX service_routes_host ON service_routes (host) WHERE host IS NOT NULL;
CREATE INDEX service_routes_service ON service_routes (service_id);

-- What each machine's daemon last said about its exposures: the machine's name on its tailnet, such
-- as apps.tail1234.ts.net, which a tailscale route's URL is on. Null until the daemon reports it.
CREATE TABLE service_exposures (
  id                 INTEGER PRIMARY KEY,
  machine_id         INTEGER NOT NULL UNIQUE REFERENCES machines(id) ON DELETE CASCADE,
  tailscale_dns_name TEXT,
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL
) STRICT;

-- The registries managed services pull private images from. password is sealed like a service's
-- env values, in hex. The daemon gets the logins with a deploy, and uses those of its images' hosts.
CREATE TABLE registries (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  host       TEXT NOT NULL UNIQUE,
  username   TEXT NOT NULL,
  password   TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
