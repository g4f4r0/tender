package tokens

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(context.Background(), Migrations); err != nil {
		t.Fatal(err)
	}
	return NewStore(d)
}

func status(err error) int {
	var e *server.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// A token is tender_ plus 32 random bytes in base64url, shown once; the database keeps only its SHA-256.
func TestCreateShowsTheTokenOnceAndKeepsItsHash(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	c, err := s.Create(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(c.Value, Prefix))
	if !strings.HasPrefix(c.Value, Prefix) || err != nil || len(raw) != 32 {
		t.Errorf("token %q is not tender_ and 32 bytes in base64url", c.Value)
	}
	if c.Name != "laptop" || c.ID == 0 || c.LastUsedAt != nil || c.CreatedAt.IsZero() {
		t.Errorf("created = %+v", c.Token)
	}
	var hash []byte
	if err := s.db.Read.QueryRow(`SELECT token_hash FROM api_tokens`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256([]byte(c.Value)); string(hash) != string(sum[:]) {
		t.Error("the database keeps something other than the token's SHA-256")
	}
	other, err := s.Create(ctx, "claude-code")
	if err != nil || other.Value == c.Value {
		t.Errorf("second token: %v, same value %v", err, other.Value == c.Value)
	}
	if _, err := s.Create(ctx, "laptop"); status(err) != http.StatusConflict {
		t.Errorf("same name: %v, want 409", err)
	}
	for _, bad := range []string{"", "Laptop", "-x", "a b", strings.Repeat("a", 64)} {
		if _, err := s.Create(ctx, bad); status(err) != http.StatusUnprocessableEntity {
			t.Errorf("name %q: %v, want 422", bad, err)
		}
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "claude-code" || list[1].Name != "laptop" {
		t.Errorf("list = %+v, %v", list, err)
	}
}

// Authenticate knows a token by its value, notes when it was last used, at most once a minute, and
// stops knowing it once it is deleted.
func TestAuthenticateAndDelete(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if has, err := s.Any(ctx); has || err != nil {
		t.Fatalf("an empty hub has tokens: %v %v", has, err)
	}
	c, err := s.Create(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if has, err := s.Any(ctx); !has || err != nil {
		t.Fatalf("Any after create: %v %v", has, err)
	}
	for _, wrong := range []string{"", "tender_", c.Value + "x", strings.TrimPrefix(c.Value, Prefix)} {
		if _, err := s.Authenticate(ctx, wrong); status(err) != http.StatusNotFound {
			t.Errorf("token %q: %v, want not found", wrong, err)
		}
	}
	lastUsed := func() time.Time {
		t.Helper()
		got, err := s.Get(ctx, "laptop")
		if err != nil || got.LastUsedAt == nil {
			t.Fatalf("get: %+v %v", got, err)
		}
		return *got.LastUsedAt
	}
	if name, err := s.Authenticate(ctx, c.Value); name != "laptop" || err != nil {
		t.Fatalf("authenticate: %q %v", name, err)
	}
	if !lastUsed().Equal(now) {
		t.Errorf("last_used_at = %v, want %v", lastUsed(), now)
	}
	first := now
	now = now.Add(30 * time.Second)
	if _, err := s.Authenticate(ctx, c.Value); err != nil {
		t.Fatal(err)
	}
	if !lastUsed().Equal(first) {
		t.Errorf("last_used_at moved within the minute: %v", lastUsed())
	}
	now = now.Add(time.Minute)
	if _, err := s.Authenticate(ctx, c.Value); err != nil {
		t.Fatal(err)
	}
	if !lastUsed().Equal(now) {
		t.Errorf("last_used_at = %v, want %v", lastUsed(), now)
	}

	if err := s.Delete(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, c.Value); status(err) != http.StatusNotFound {
		t.Errorf("a deleted token still works: %v", err)
	}
	if err := s.Delete(ctx, "laptop"); status(err) != http.StatusNotFound {
		t.Errorf("delete twice: %v, want 404", err)
	}
}

// The routes create (201, the token once), list (no token, no hash) and delete (204) by name.
func TestRoutes(t *testing.T) {
	s := newStore(t)
	srv := server.New(s.db, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	s.Routes(srv)
	call := func(method, path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := call("POST", "/v1/tokens", `{"name":"laptop"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"token":"tender_`) || strings.Contains(body, "hash") {
		t.Fatalf("create: %d %s", code, body)
	}
	if code, body := call("POST", "/v1/tokens", `{"name":"laptop","extra":1}`); code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
		t.Errorf("unknown field: %d %s", code, body)
	}
	code, body = call("GET", "/v1/tokens", "")
	if code != http.StatusOK || !strings.Contains(body, `"name":"laptop"`) || strings.Contains(body, "tender_") || strings.Contains(body, "hash") {
		t.Errorf("list: %d %s", code, body)
	}
	if code, body := call("DELETE", "/v1/tokens/laptop", ""); code != http.StatusNoContent {
		t.Errorf("delete: %d %s", code, body)
	}
	if code, body := call("DELETE", "/v1/tokens/laptop", ""); code != http.StatusNotFound || !strings.Contains(body, "tender tokens list") {
		t.Errorf("delete again: %d %s", code, body)
	}
}
