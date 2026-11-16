package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/services"
	"github.com/g4f4r0/tender/internal/services/deploy"
	"github.com/g4f4r0/tender/internal/services/expose"
)

const cfCompose = "services:\n  web:\n    image: ghcr.io/me/blog:2\n    mem_limit: 512m\n"

// The daemon applies all three adapters, and the cloudflare one takes the hub's tunnel token.
func TestDaemonExposers(t *testing.T) {
	state := t.TempDir()
	list, dns := daemonExposers(state, "/run/docker.sock")
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "tailscale,public,cloudflare" || dns == nil {
		t.Fatalf("exposers = %v", names)
	}
	cf, ok := list[2].(*expose.Cloudflared)
	if !ok {
		t.Fatalf("the cloudflare adapter is %T", list[2])
	}
	cf.SetTunnelToken("tunnel-token")
	pub := list[1].(*expose.Public)
	if cf.Token != "tunnel-token" || cf.Dir != filepath.Join(state, "cloudflared") || pub.Dir != filepath.Join(state, "caddy") ||
		cf.Socket != "/run/docker.sock" || pub.Socket != "/run/docker.sock" {
		t.Errorf("cloudflared %+v, public %+v", cf, pub)
	}
}

// The Cloudflare settings can't be removed while a cloudflare route needs them, and a cloudflare
// route can't be made before they are set.
func TestCloudflareSettingsInUse(t *testing.T) {
	const account, token = "0123456789abcdef0123456789abcdef", "cf-api-token-marker-0123456789abcdefghij"
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success": true, "errors": [], "result": [], "result_info": {"page": 1, "total_pages": 1}}`))
	}))
	t.Cleanup(cf.Close)
	t.Setenv(expose.EnvTestCloudflare, cf.URL)
	d := seeded(t)
	hub := httptest.NewServer(newHub(d, testKey, nil, quiet))
	t.Cleanup(hub.Close)
	ctx := context.Background()
	ss := newServices(d, testKey, newActions(d, testKey))
	blog := services.Input{Name: "blog", Compose: cfCompose,
		Routes: []services.RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("blog.example.com")}}}
	if _, err := ss.Create(ctx, 1, blog); err == nil || !strings.Contains(err.Error(), "tender exposures cloudflare set") {
		t.Fatalf("a cloudflare route before the settings: %v", err)
	}
	if _, err := newCloudflareStore(d, testKey).SetCloudflare(ctx, services.CloudflareInput{AccountID: account, APIToken: ptr(token)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.Create(ctx, 1, blog); err != nil {
		t.Fatal(err)
	}
	del := func() (int, string) {
		req, _ := http.NewRequest(http.MethodDelete, hub.URL+"/v1/exposures/cloudflare", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := del(); code != http.StatusConflict || !strings.Contains(body, "1 cloudflare routes need these settings") {
		t.Errorf("DELETE with a cloudflare route = %d %s", code, body)
	}
}

// A machine's public routes put 80/tcp and 443/tcp on its allowlist for the security checks.
func TestPublicRoutesAllowCaddysPorts(t *testing.T) {
	d := seeded(t)
	ctx := context.Background()
	ms := machines.NewStore(d)
	ss := newServices(d, testKey, newActions(d, testKey))
	allowed := newSecurity(d, ms).Allowed
	if got, err := allowed(ctx, 1); err != nil || slices.Contains(got, "443/tcp") {
		t.Fatalf("before = %v, %v", got, err)
	}
	if _, err := ss.Create(ctx, 1, services.Input{Name: "blog", Compose: cfCompose,
		Routes: []services.RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposePublic, Host: ptr("blog.example.com")}}}); err != nil {
		t.Fatal(err)
	}
	if got, err := allowed(ctx, 1); err != nil || !slices.Contains(got, "80/tcp") || !slices.Contains(got, "443/tcp") {
		t.Errorf("with a public route = %v, %v", got, err)
	}
	if got, _ := allowed(ctx, 2); slices.Contains(got, "443/tcp") {
		t.Errorf("the other machine = %v", got)
	}
	list, err := ms.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	with, err := withPublicPorts(ctx, ss, list)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range with {
		if has := slices.Contains(o.SecurityAllowedPorts, "443/tcp"); has != (o.ID == 1) {
			t.Errorf("%s allows 443/tcp: %v", o.Name, has)
		}
	}
}
