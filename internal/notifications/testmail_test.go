package notifications

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

// noMachines is the identity this domain's routes need when no route has {machine}.
type noMachines struct{}

func (noMachines) Authenticate(context.Context, string) (int64, string, error) {
	return 0, "", server.NotFound("no such token")
}
func (noMachines) Lookup(context.Context, string) (int64, error) {
	return 0, server.NotFound("no machine")
}

// The test goes through the real mailer to the fake SMTP server, one message per email channel.
func TestTestEmailReachesEveryChannel(t *testing.T) {
	smtpd := newFakeSMTP(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = d.Write.Exec(`CREATE TABLE machines (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, token_hash BLOB NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL) STRICT`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background(), Migrations, server.Migrations); err != nil {
		t.Fatal(err)
	}
	s := server.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), noMachines{})
	NewStore(d, testKey).Routes(s, smtpd.mailer("right"), testHub)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	post := func(path, body string) int {
		t.Helper()
		res, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	if code := post("/v1/notifications/test", ""); code != 422 {
		t.Fatalf("test without channels: %d, want 422", code)
	}
	for _, c := range []string{`{"name":"johan","type":"email","config":{"to":"johan@example.com"}}`, `{"name":"ops","type":"email","config":{"to":"ops@example.com"}}`} {
		if code := post("/v1/notifications/channels", c); code != 201 {
			t.Fatalf("add %s: %d", c, code)
		}
	}
	if code := post("/v1/notifications/test", ""); code != 200 {
		t.Fatalf("test: %d, want 200", code)
	}
	var rcpts []string
	for range 2 {
		got := <-smtpd.got
		subject, text, _ := parseEmail(t, got)
		if subject != "Tender test email" || !strings.Contains(text, "hub on hub1") {
			t.Errorf("got %q %q", subject, text)
		}
		rcpts = append(rcpts, got.rcpts...)
	}
	slices.Sort(rcpts)
	if !slices.Equal(rcpts, []string{"TO:<johan@example.com>", "TO:<ops@example.com>"}) {
		t.Errorf("recipients = %q, want one message to each", rcpts)
	}
}
