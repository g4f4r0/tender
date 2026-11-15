package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

// usedEvery is how stale last_used_at may get: a request writes it only when it is older, so
// a busy client doesn't write to the database on every request.
const usedEvery = time.Minute

// Store reads and writes API tokens.
type Store struct {
	db  *db.DB
	now func() time.Time
}

// NewStore returns a store on d.
func NewStore(d *db.DB) *Store { return &Store{db: d, now: time.Now} }

// newToken returns a random token and its SHA-256, the only form the database keeps.
func newToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = Prefix + base64.RawURLEncoding.EncodeToString(raw)
	return token, hashOf(token), nil
}

func hashOf(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Create adds a token called name and returns it, with the token itself shown this once.
func (s *Store) Create(ctx context.Context, name string) (TokenCreated, error) {
	if !server.ValidName(name) {
		return TokenCreated{}, server.Invalid("Token names are lowercase letters, digits and hyphens, up to 63 characters, such as laptop or claude-code.")
	}
	token, hash, err := newToken()
	if err != nil {
		return TokenCreated{}, err
	}
	now := s.now().Unix()
	res, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO api_tokens (name, token_hash, created_at, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT (name) DO NOTHING`,
		name, hash, now, now)
	if err != nil {
		return TokenCreated{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return TokenCreated{}, err
	} else if n == 0 {
		return TokenCreated{}, server.Conflict("Token %q already exists. Choose another name, or remove it first with: tender tokens remove %s", name, name)
	}
	t, err := s.Get(ctx, name)
	return TokenCreated{Token: t, Value: token}, err
}

const columns = `id, name, last_used_at, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Token, error) {
	var t Token
	var used *int64
	var created, updated int64
	if err := row.Scan(&t.ID, &t.Name, &used, &created, &updated); err != nil {
		return t, err
	}
	if used != nil {
		u := time.Unix(*used, 0).UTC()
		t.LastUsedAt = &u
	}
	t.CreatedAt, t.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return t, nil
}

// Get returns the token called name.
func (s *Store) Get(ctx context.Context, name string) (Token, error) {
	t, err := scan(s.db.Read.QueryRowContext(ctx, `SELECT `+columns+` FROM api_tokens WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return t, server.NotFound("Token %q doesn't exist. List the tokens with: tender tokens list", name)
	}
	return t, err
}

// List returns every token by name.
func (s *Store) List(ctx context.Context) ([]Token, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT `+columns+` FROM api_tokens ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Token{}
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Delete removes the token called name. It stops working at once.
func (s *Store) Delete(ctx context.Context, name string) error {
	res, err := s.db.Write.ExecContext(ctx, `DELETE FROM api_tokens WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return server.NotFound("Token %q doesn't exist. List the tokens with: tender tokens list", name)
	}
	return nil
}

// Authenticate returns the name of the token, or a NotFound error when no token is the one given.
// It looks the token up by its SHA-256: the hash of a random 32-byte token tells an attacker
// nothing, so the lookup needs no constant-time compare. It notes when the token was last used.
func (s *Store) Authenticate(ctx context.Context, token string) (string, error) {
	hash := hashOf(token)
	var id int64
	var name string
	var used *int64
	err := s.db.Read.QueryRowContext(ctx, `SELECT id, name, last_used_at FROM api_tokens WHERE token_hash = ?`, hash).Scan(&id, &name, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return "", server.NotFound("No API token is this one.")
	}
	if err != nil {
		return "", err
	}
	now := s.now().Unix()
	if used != nil && *used > now-int64(usedEvery/time.Second) {
		return name, nil // noted less than a minute ago: no write
	}
	_, err = s.db.Write.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now, id)
	return name, err
}

// Any reports whether at least one token exists. Until one does, the hub refuses every people request.
func (s *Store) Any(ctx context.Context) (bool, error) {
	var n int
	err := s.db.Read.QueryRowContext(ctx, `SELECT count(*) FROM api_tokens`).Scan(&n)
	return n > 0, err
}
