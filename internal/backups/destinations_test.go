package backups_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/server"
)

type problem struct{ Detail string }

// create posts a destination and returns the response body, for checks on its exact fields.
func (h *hub) create(body any) (int, map[string]any) {
	h.t.Helper()
	var out map[string]any
	res := h.do("POST", "/v1/backups/destinations", "", body, &out)
	return res.StatusCode, out
}

func TestCreateDestinationRoute(t *testing.T) {
	h := newHub(t)

	// With a password of the owner's: it is stored, not returned.
	code, out := h.create(backups.DestinationInput{Name: "r2-code", Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "code", Path: "/hosts/",
		AccessKeyID: "key-id", SecretAccessKey: "key-secret", Password: "mine"})
	if code != 201 || out["name"] != "r2-code" || out["type"] != "s3" || out["path"] != "hosts" || out["id"] == nil || out["created_at"] == nil {
		t.Fatalf("create: %d %v", code, out)
	}
	for _, field := range []string{"password", "access_key_id", "secret_access_key"} {
		if _, ok := out[field]; ok {
			t.Errorf("response has %s: %v", field, out)
		}
	}

	// Without one: the hub makes 32 random bytes and returns them once, here.
	var made backups.DestinationCreated
	res := h.do("POST", "/v1/backups/destinations", "", map[string]any{"name": "b-two", "endpoint": "https://x.example.com", "bucket": "b",
		"access_key_id": "key-id-2", "secret_access_key": "key-secret-2"}, &made)
	if res.StatusCode != 201 || res.Header.Get("Location") != "/v1/backups/destinations/b-two" {
		t.Fatalf("create without password: %d location %q", res.StatusCode, res.Header.Get("Location"))
	}
	raw, err := base64.StdEncoding.DecodeString(made.Password)
	if err != nil || len(raw) != 32 {
		t.Fatalf("generated password %q is not 32 random bytes in base64 (%v)", made.Password, err)
	}
	// It is what the agent gets later; no read shows it.
	h.status(200, "PUT", "/v1/machines/a/backups/settings", "", backups.SettingsInput{Destination: "b-two", Schedule: "30 3 * * *", RetentionDays: 30})
	var st backups.Settings
	h.do("GET", "/v1/machines/a/backups/settings", "tok-a", nil, &st)
	if st.Credentials == nil || st.Credentials.Password != made.Password {
		t.Errorf("agent credentials = %+v, want the generated password", st.Credentials)
	}
	var body json.RawMessage
	for _, path := range []string{"/v1/backups/destinations", "/v1/backups/destinations/b-two", "/v1/machines/a/backups", "/v1/backups"} {
		h.do("GET", path, "", nil, &body)
		if bytes.Contains(body, []byte(made.Password)) || bytes.Contains(body, []byte("key-secret")) {
			t.Errorf("GET %s shows a secret: %s", path, body)
		}
	}
}

func TestCreateDestinationRejects(t *testing.T) {
	h := newHub(t)
	good := func() map[string]any {
		return map[string]any{"name": "r2", "endpoint": "https://x.example.com", "bucket": "b", "access_key_id": "k", "secret_access_key": "s"}
	}
	if code, _ := h.create(good()); code != 201 {
		t.Fatalf("good destination: %d", code)
	}
	with := func(k string, v any) map[string]any {
		m := good()
		m[k] = v
		return m
	}
	for _, c := range []struct {
		name string
		body any
		want int
	}{
		{"same name", with("endpoint", "https://other.example.com"), 409},
		{"same location", with("name", "other"), 409},
		{"bad name", with("name", "R2"), 422},
		{"bad type", with("type", "gcs"), 422},
		{"bad endpoint", with("endpoint", "nope"), 422},
		{"no bucket", with("bucket", ""), 422},
		{"no key id", with("access_key_id", ""), 422},
		{"no secret key", with("secret_access_key", ""), 422},
		{"unknown field", with("region", "auto"), 400},
		{"wrong type", with("bucket", 5), 422},
	} {
		var p problem
		if res := h.do("POST", "/v1/backups/destinations", "", c.body, &p); res.StatusCode != c.want || p.Detail == "" {
			t.Errorf("%s: %d %q, want %d with a sentence", c.name, res.StatusCode, p.Detail, c.want)
		}
	}
	var list server.List[backups.Destination]
	h.do("GET", "/v1/backups/destinations", "", nil, &list)
	if len(list.Data) != 1 {
		t.Errorf("%d destinations after the refused creates, want 1", len(list.Data))
	}
}

// The agent gets the decrypted credentials, only with its own token; the database holds none of them in the clear.
func TestAgentCredentials(t *testing.T) {
	h := newHub(t)
	var made backups.DestinationCreated
	h.do("POST", "/v1/backups/destinations", "", map[string]any{"name": "r2", "endpoint": "https://x.example.com", "bucket": "b",
		"access_key_id": "marker-key-id", "secret_access_key": "marker-secret-key"}, &made)
	h.status(200, "PUT", "/v1/machines/a/backups/settings", "", backups.SettingsInput{Destination: "r2", Schedule: "30 3 * * *", RetentionDays: 30})

	var st backups.Settings
	if res := h.do("GET", "/v1/machines/a/backups/settings", "tok-a", nil, &st); res.StatusCode != 200 || st.Credentials == nil ||
		*st.Credentials != (backups.Credentials{AccessKeyID: "marker-key-id", SecretAccessKey: "marker-secret-key", Password: made.Password}) {
		t.Fatalf("agent settings: %d %+v", res.StatusCode, st.Credentials)
	}

	// Without the token: settings, and no credentials key at all.
	var plain map[string]any
	if res := h.do("GET", "/v1/machines/a/backups/settings", "", nil, &plain); res.StatusCode != 200 || plain["schedule"] != "30 3 * * *" {
		t.Fatalf("people settings: %d %v", res.StatusCode, plain)
	}
	if _, ok := plain["credentials"]; ok {
		t.Errorf("people settings show credentials: %v", plain)
	}
	// Another machine's token gets nothing, and a wrong one is refused.
	h.status(403, "GET", "/v1/machines/a/backups/settings", "tok-b", nil)
	h.status(401, "GET", "/v1/machines/a/backups/settings", "wrong", nil)

	// What the database holds is sealed: no plaintext, a nonce and a tag around it.
	var keyID, secretKey, password []byte
	err := h.db.Read.QueryRow(`SELECT access_key_id, secret_access_key, password FROM backup_destinations WHERE name = 'r2'`).Scan(&keyID, &secretKey, &password)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string][]byte{"access_key_id": keyID, "secret_access_key": secretKey, "password": password} {
		if len(v) < 12+16 || bytes.Contains(v, []byte("marker")) || bytes.Contains(v, []byte(made.Password)) {
			t.Errorf("%s column holds %d bytes that look like plaintext", name, len(v))
		}
	}
}

func TestWrongKeyFailsCleanly(t *testing.T) {
	h := newHub(t)
	h.setup()
	ctx := context.Background()

	if err := h.store.CheckKey(ctx); err != nil {
		t.Errorf("the right key: %v", err)
	}
	other := make([]byte, 32)
	wrong := backups.NewStore(h.db, other)
	err := wrong.CheckKey(ctx)
	if err == nil || !strings.Contains(err.Error(), "r2-code") || !strings.Contains(err.Error(), "wrong key") {
		t.Errorf("the wrong key: %v, want an error that names the destination and the key", err)
	}
	_, err = wrong.Credentials(ctx, 1)
	var e *server.Error
	if err == nil || errors.As(err, &e) || !strings.Contains(err.Error(), "TENDER_ENCRYPTION_KEY") || strings.Contains(err.Error(), "repo-password") {
		t.Errorf("Credentials with the wrong key: %v, want a plain error that names the key and shows no secret", err)
	}

	// Over HTTP an agent gets a 500 with a sentence and no detail; the people routes still work.
	s := server.New(h.db, slog.New(slog.NewTextHandler(io.Discard, nil)), identity{h.db})
	wrong.Routes(s)
	srv := httptest.NewServer(s)
	defer srv.Close()
	res, err := srv.Client().Do(mustRequest(t, srv.URL+"/v1/machines/a/backups/settings", "tok-a"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 500 || strings.Contains(string(body), "TENDER_ENCRYPTION_KEY") || strings.Contains(string(body), "repo-password") {
		t.Errorf("agent with a wrong hub key: %d %s, want a plain 500", res.StatusCode, body)
	}

	// A store with no key at all refuses writes with a sentence that says what to set.
	none := backups.NewStore(h.db, nil)
	_, err = none.AddDestination(ctx, input("x", "https://y.example.com", "b", ""))
	if !errors.As(err, &e) || e.Status != 503 || !strings.Contains(e.Detail, "TENDER_ENCRYPTION_KEY") {
		t.Errorf("no key: %v, want 503 naming TENDER_ENCRYPTION_KEY", err)
	}
	// With nothing stored, any key is fine.
	empty := newHub(t)
	if err := backups.NewStore(empty.db, other).CheckKey(ctx); err != nil {
		t.Errorf("empty database: %v", err)
	}
}

func TestPatchDestinationRoute(t *testing.T) {
	h := newHub(t)
	h.setup()
	h.status(201, "POST", "/v1/backups/destinations", "", input("second", "https://y.example.com", "other", ""))

	var d backups.Destination
	res := h.do("PATCH", "/v1/backups/destinations/r2-code", "", map[string]any{"bucket": "code-2", "path": "/new/"}, &d)
	if res.StatusCode != 200 || d.Bucket != "code-2" || d.Path != "new" || d.Endpoint != "https://x.r2.cloudflarestorage.com" || d.Name != "r2-code" {
		t.Fatalf("patch location: %d %+v", res.StatusCode, d)
	}

	// New keys replace the old; the password stays. Nothing but the destination comes back.
	var raw map[string]any
	res = h.do("PATCH", "/v1/backups/destinations/r2-code", "", map[string]any{"access_key_id": "new-id", "secret_access_key": "new-secret"}, &raw)
	if res.StatusCode != 200 || raw["access_key_id"] != nil || raw["secret_access_key"] != nil || raw["password"] != nil {
		t.Fatalf("patch keys: %d %v", res.StatusCode, raw)
	}
	var st backups.Settings
	h.do("GET", "/v1/machines/a/backups/settings", "tok-a", nil, &st)
	if st.Credentials == nil || *st.Credentials != (backups.Credentials{AccessKeyID: "new-id", SecretAccessKey: "new-secret", Password: "repo-password"}) {
		t.Errorf("credentials after the patch = %+v, want the new keys and the same password", st.Credentials)
	}
	if st.Destination.Bucket != "code-2" {
		t.Errorf("settings show bucket %q, want code-2", st.Destination.Bucket)
	}

	for _, c := range []struct {
		name, path string
		body       any
		want       int
		says       string
	}{
		{"password change", "r2-code", map[string]any{"password": "new"}, 422, "encrypted with it"},
		{"password with other fields", "r2-code", map[string]any{"bucket": "x", "password": "new"}, 422, "can't change"},
		{"empty key id", "r2-code", map[string]any{"access_key_id": ""}, 422, "can't be empty"},
		{"empty bucket", "r2-code", map[string]any{"bucket": ""}, 422, "can't be empty"},
		{"bad endpoint", "r2-code", map[string]any{"endpoint": "nope"}, 422, "endpoint"},
		{"another destination's location", "r2-code", map[string]any{"endpoint": "https://y.example.com", "bucket": "other", "path": ""}, 409, "Use that one"},
		{"unknown destination", "nope", map[string]any{"bucket": "x"}, 404, "nope"},
		{"unknown field", "r2-code", map[string]any{"name": "renamed"}, 400, ""},
		{"wrong type", "r2-code", map[string]any{"bucket": 5}, 422, ""},
	} {
		var p problem
		if res := h.do("PATCH", "/v1/backups/destinations/"+c.path, "", c.body, &p); res.StatusCode != c.want || !strings.Contains(p.Detail, c.says) || p.Detail == "" {
			t.Errorf("%s: %d %q, want %d containing %q", c.name, res.StatusCode, p.Detail, c.want, c.says)
		}
	}
	// A refused patch changes nothing, and the password is what it was.
	h.do("GET", "/v1/machines/a/backups/settings", "tok-a", nil, &st)
	if st.Destination.Bucket != "code-2" || st.Credentials.Password != "repo-password" {
		t.Errorf("after refused patches: %+v %+v", st.Destination, st.Credentials)
	}
}

func TestDeleteDestinationRoute(t *testing.T) {
	h := newHub(t)
	h.setup() // machine a uses r2-code
	h.status(201, "POST", "/v1/backups/destinations", "", input("spare", "https://y.example.com", "other", ""))

	var p problem
	if res := h.do("DELETE", "/v1/backups/destinations/r2-code", "", nil, &p); res.StatusCode != 409 || !strings.Contains(p.Detail, "a") || !strings.Contains(p.Detail, "backup settings") {
		t.Errorf("in use: %d %q, want 409 naming the machine and what to do", res.StatusCode, p.Detail)
	}
	h.status(204, "DELETE", "/v1/backups/destinations/spare", "", nil)
	h.status(404, "GET", "/v1/backups/destinations/spare", "", nil)
	h.status(404, "DELETE", "/v1/backups/destinations/spare", "", nil)

	// Once the machine stops using it, it can go.
	h.status(204, "DELETE", "/v1/machines/a/backups/settings", "", nil)
	h.status(204, "DELETE", "/v1/backups/destinations/r2-code", "", nil)
	var list server.List[backups.Destination]
	h.do("GET", "/v1/backups/destinations", "", nil, &list)
	if len(list.Data) != 0 {
		t.Errorf("destinations = %+v, want none", list.Data)
	}
}

func TestSettingsRoutes(t *testing.T) {
	h := newHub(t)
	h.status(201, "POST", "/v1/backups/destinations", "", input("r2", "https://x.example.com", "b", ""))
	body := backups.SettingsInput{Destination: "r2", Schedule: "30 3 * * *", RetentionDays: 30}

	var st backups.Settings
	if res := h.do("PUT", "/v1/machines/a/backups/settings", "", body, &st); res.StatusCode != 200 || st.Destination.Name != "r2" ||
		st.Schedule != "30 3 * * *" || st.RetentionDays != 30 || st.Credentials != nil {
		t.Fatalf("put: %d %+v, want 200 with no credentials", res.StatusCode, st)
	}
	// Putting again replaces.
	body.Schedule, body.RetentionDays = "0 4 * * *", 7
	h.do("PUT", "/v1/machines/a/backups/settings", "", body, &st)
	if st.Schedule != "0 4 * * *" || st.RetentionDays != 7 {
		t.Errorf("replaced settings = %+v", st)
	}

	for _, c := range []struct {
		name, machine string
		body          any
		want          int
	}{
		{"unknown machine", "zzz", body, 404},
		{"unknown destination", "a", backups.SettingsInput{Destination: "nope", Schedule: "30 3 * * *", RetentionDays: 30}, 422},
		{"bad cron", "a", backups.SettingsInput{Destination: "r2", Schedule: "daily", RetentionDays: 30}, 422},
		{"no retention", "a", backups.SettingsInput{Destination: "r2", Schedule: "30 3 * * *"}, 422},
		{"unknown field", "a", map[string]any{"destination": "r2", "schedule": "30 3 * * *", "retention_days": 30, "paths": []string{"/"}}, 400},
		{"wrong type", "a", map[string]any{"destination": "r2", "schedule": "30 3 * * *", "retention_days": "thirty"}, 422},
	} {
		var p problem
		if res := h.do("PUT", "/v1/machines/"+c.machine+"/backups/settings", "", c.body, &p); res.StatusCode != c.want || p.Detail == "" {
			t.Errorf("%s: %d %q, want %d with a sentence", c.name, res.StatusCode, p.Detail, c.want)
		}
	}
	h.do("GET", "/v1/machines/a/backups/settings", "", nil, &st)
	if st.Schedule != "0 4 * * *" {
		t.Errorf("a refused put changed the settings: %+v", st)
	}

	// An agent can't set its own settings: that is for people.
	h.status(204, "DELETE", "/v1/machines/a/backups/settings", "", nil)
	h.status(404, "GET", "/v1/machines/a/backups/settings", "tok-a", nil)
	h.status(404, "DELETE", "/v1/machines/a/backups/settings", "", nil)
	h.status(404, "DELETE", "/v1/machines/zzz/backups/settings", "", nil)
	// The machine's other data stays; the summary shows no settings.
	var sum backups.Summary
	h.do("GET", "/v1/machines/a/backups", "", nil, &sum)
	if sum.Settings != nil {
		t.Errorf("summary after unset = %+v", sum.Settings)
	}
}

func TestDestinationJSONHasNoSecretFields(t *testing.T) {
	// The wire types of every people response carry no secret field, whatever a handler sets.
	for _, v := range []any{backups.Destination{}, backups.Summary{}, backups.DestinationPatch{}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"password", "secret_access_key", "access_key_id\":\"", "credentials"} {
			if strings.Contains(string(b), field) {
				t.Errorf("%T marshals with %s: %s", v, field, b)
			}
		}
	}
}

func mustRequest(t *testing.T, url, token string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}
