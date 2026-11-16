-- People's API tokens. Only the SHA-256 of a token is kept; the token is shown once, on create.
CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  token_hash   BLOB NOT NULL UNIQUE,
  last_used_at INTEGER,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
) STRICT;
