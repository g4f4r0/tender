-- The headers of a stored answer that a replay must repeat: Location and Content-Type, as a JSON
-- object of header name to value.
ALTER TABLE idempotency_keys ADD COLUMN response_headers TEXT NOT NULL DEFAULT '{}';
