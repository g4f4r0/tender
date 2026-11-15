-- The runtime that runs each service. Only docker exists; the column is free text so another
-- runtime needs no schema change. Every service before it ran on Docker.
ALTER TABLE services ADD COLUMN runtime TEXT NOT NULL DEFAULT 'docker';
