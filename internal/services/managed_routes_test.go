package services

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/server"
)

// newManagedHub serves the routes of a store with a key and fake actions.
func newManagedHub(t *testing.T) (*hub, *Store, *fakeActions) {
	t.Helper()
	s, fa := managedStore(t)
	srv := server.New(s.db, slog.New(slog.NewTextHandler(io.Discard, nil)), identity{s.db})
	s.Routes(srv)
	h := &hub{Server: httptest.NewServer(srv), srv: srv, db: s.db, logs: &syncBuffer{}}
	t.Cleanup(h.Close)
	return h, s, fa
}

func TestManagedRoutes(t *testing.T) {
	h, s, fa := newManagedHub(t)
	if err := s.Replace(t.Context(), 1, []ContainerInput{running("proxy", "c1")}); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 900<<10) // with the compose file, the body is over 1 MiB
	create := map[string]any{"name": "notes", "compose": notesCompose, "env": map[string]string{"SECRET_KEY": "s3cret"},
		"files": map[string]string{"big.txt": big}, "routes": []map[string]any{{"container": "web", "port": 8080, "expose": "tailscale"}}}
	for _, c := range []struct {
		method, path, token string
		body                any
		status              int
		has                 string
	}{
		{"POST", "/v1/machines/a/services", "", create, 202, `"deploy_status":"deploying"`},
		{"POST", "/v1/machines/a/services", "", create, 409, `Service \"notes\" already exists`},
		{"POST", "/v1/machines/a/services", "", map[string]any{"name": "proxy", "compose": notesCompose}, 409, "docker compose -p proxy down"},
		{"POST", "/v1/machines/a/services", "", map[string]any{"name": "blog", "compose": notesCompose, "extra": 1}, 400, "unknown field"},
		{"GET", "/v1/machines/a/services/notes", "", nil, 200, `"env":{"SECRET_KEY":"***"}`},
		{"PATCH", "/v1/machines/a/services/notes", "", map[string]any{"compose": nil}, 422, "compose can't be null"},
		{"PATCH", "/v1/machines/a/services/notes", "", map[string]any{"env": map[string]any{"MODE": "prod"}}, 202, `"MODE":"***"`},
		{"PATCH", "/v1/machines/a/services/notes", "", map[string]any{"alerts_enabled": false}, 200, `"alerts_enabled":false`},
		{"PATCH", "/v1/machines/a/services/proxy", "", map[string]any{"compose": notesCompose}, 409, `Tender didn't deploy service \"proxy\"`},
		{"PATCH", "/v1/machines/a/services/proxy", "", map[string]any{"alerts_enabled": false}, 200, `"managed":false`},
		{"POST", "/v1/machines/a/services/notes/deploy", "", nil, 202, `"action":`},
		{"POST", "/v1/machines/a/services/proxy/deploy", "", nil, 409, "can't deploy it"},
		{"POST", "/v1/machines/a/services/proxy/restart", "", nil, 202, `"name":"proxy"`},
		{"POST", "/v1/machines/a/services/proxy/stop", "", map[string]any{"container": "proxy"}, 202, `"name":"proxy"`},
		{"POST", "/v1/machines/a/services/proxy/start", "", map[string]any{"container": ""}, 422, "container can't be empty"},
		{"POST", "/v1/machines/a/services/nope/start", "", nil, 404, `Service \"nope\" doesn't exist`},
		{"DELETE", "/v1/machines/a/services/proxy", "", nil, 409, "can't remove it"},
		{"DELETE", "/v1/machines/a/services/notes?delete_data=maybe", "", nil, 400, "delete_data must be true or false"},
		{"PUT", "/v1/machines/a/exposures/status", "token-a", map[string]any{"tailscale_dns_name": "apps.tail1234.ts.net"}, 204, ""},
		{"PUT", "/v1/machines/a/exposures/status", "token-a", map[string]any{"tailscale_dns_name": "not a name"}, 422, "tailscale_dns_name"},
		{"GET", "/v1/machines/a/exposures", "token-a", nil, 200, `{"routes":[{"service":"notes","container":"web","expose":"tailscale","local_port":20000,"host":null,"https_port":10000}],"cloudflare_tunnel_token":null}`},
		{"GET", "/v1/machines/a/exposures", "token-b", nil, 403, "another machine"},
		{"GET", "/v1/machines/a/exposures", "", nil, 401, "Bearer"},
		{"GET", "/v1/machines/a/services/notes", "", nil, 200, `"url":"https://apps.tail1234.ts.net:10000"`},
		{"DELETE", "/v1/machines/a/services/notes?delete_data=true", "", nil, 202, `"deploy_status":"removing"`},
		{"POST", "/v1/registries", "", map[string]any{"name": "ghcr", "host": "ghcr.io", "username": "me", "password": "token"}, 201, `"password":"***","password_set":true`},
		{"GET", "/v1/registries", "", nil, 200, `"name":"ghcr"`},
		{"PATCH", "/v1/registries/ghcr", "", map[string]any{"username": "you"}, 200, `"username":"you"`},
		{"DELETE", "/v1/registries/ghcr", "", nil, 204, ""},
		{"DELETE", "/v1/registries/ghcr", "", nil, 404, `Registry \"ghcr\" doesn't exist`},
	} {
		status, body := h.call(t, c.method, c.path, c.token, c.body)
		if status != c.status || !strings.Contains(body, c.has) {
			t.Errorf("%s %s: %d %s, want %d with %q", c.method, c.path, status, trim(body), c.status, c.has)
		}
		if strings.Contains(body, "s3cret") || strings.Contains(body, `"token"`) {
			t.Errorf("%s %s shows a secret: %s", c.method, c.path, trim(body))
		}
	}
	if a := fa.last(); a.typ != ActionRemove || !a.deleteData {
		t.Errorf("the last action = %+v", a)
	}
	// A deploy's answer says where its action is.
	req, _ := http.NewRequest("POST", h.URL+"/v1/machines/a/services/proxy/restart", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if loc := res.Header.Get("Location"); !strings.HasPrefix(loc, "/v1/machines/a/actions/") {
		t.Errorf("Location = %q", loc)
	}
}

func trim(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}
