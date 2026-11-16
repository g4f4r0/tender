package services

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/g4f4r0/tender/internal/apps/expose"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services/deploy"
)

// fakeTunnels records each Apply. It refuses the routes of the services in refuse, and fails as a
// whole with broken.
type fakeTunnels struct {
	mu      sync.Mutex
	applied [][]expose.Route
	refuse  map[string]bool
	broken  error
	ready   error
}

func (f *fakeTunnels) Ready(context.Context) error { return f.ready }

func (f *fakeTunnels) Apply(_ context.Context, machine string, routes []expose.Route) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, routes)
	if f.broken != nil {
		return f.broken
	}
	var errs []error
	for _, r := range routes {
		if f.refuse[r.Service] {
			errs = append(errs, &expose.RouteError{Service: r.Service, Container: r.Container,
				Err: errors.New(r.Host + " already has a DNS record Tender didn't make, so the route to " + r.Container + " in " + r.Service + " can't have it")})
		}
	}
	return errors.Join(errs...)
}

func (f *fakeTunnels) Token(_ context.Context, machine string, routes []expose.Route) (string, error) {
	if len(routes) == 0 {
		return "", nil
	}
	return "tunnel-token-of-" + machine, nil
}

func deployment(t *testing.T, s *Store, name string) deploy.Deployment {
	t.Helper()
	raw, err := s.Deployment(context.Background(), 1, name)
	if err != nil {
		t.Fatalf("deployment of %s: %v", name, err)
	}
	var d deploy.Deployment
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// Every deploy and remove brings the machine's tunnel up to date with its cloudflare routes, and
// the exposures carry the tunnel's token. A route the tunnel can't take fails only its own
// service; Cloudflare failing as a whole fails the services that have cloudflare routes.
func TestDeploymentSyncsTheTunnel(t *testing.T) {
	s, _ := managedStore(t)
	ft := &fakeTunnels{refuse: map[string]bool{"bad": true}}
	logs := &syncBuffer{}
	s.Tunnels, s.Log = ft, slog.New(slog.NewTextHandler(logs, nil))
	ctx := context.Background()
	for _, in := range []Input{
		{Name: "notes", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeTailscale}}},
		{Name: "blog", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("blog.example.com")}}},
		{Name: "bad", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("taken.example.com")}}},
	} {
		if _, err := s.Create(ctx, 1, in); err != nil {
			t.Fatal(err)
		}
	}

	notes := deployment(t, s, "notes")
	if notes.CloudflareError != "" {
		t.Errorf("bad's route failed notes: %q", notes.CloudflareError)
	}
	if got := ft.applied[len(ft.applied)-1]; len(got) != 2 || got[0].Service != "bad" || got[1].Service != "blog" || got[1].Host != "blog.example.com" {
		t.Errorf("the tunnel got %+v; want the machine's two cloudflare routes", got)
	}
	if notes.Exposures.CloudflareTunnelToken == nil || !strings.HasPrefix(*notes.Exposures.CloudflareTunnelToken, "tunnel-token-of-") {
		t.Errorf("exposures token = %v", notes.Exposures.CloudflareTunnelToken)
	}
	if blog := deployment(t, s, "blog"); blog.CloudflareError != "" {
		t.Errorf("bad's route failed blog: %q", blog.CloudflareError)
	}
	if bad := deployment(t, s, "bad"); bad.CloudflareError != "taken.example.com already has a DNS record Tender didn't make, so the route to web in bad can't have it" {
		t.Errorf("bad's own error = %q", bad.CloudflareError)
	}

	ft.broken = errors.New("Cloudflare answered 503 three times")
	if blog := deployment(t, s, "blog"); !strings.Contains(blog.CloudflareError, "Cloudflare answered 503") {
		t.Errorf("Cloudflare down, blog = %q", blog.CloudflareError)
	}
	if notes := deployment(t, s, "notes"); notes.CloudflareError != "" {
		t.Errorf("Cloudflare down failed notes, which has no cloudflare route: %q", notes.CloudflareError)
	}
	ft.broken = nil
	if !strings.Contains(logs.String(), "could not route all of the machine's cloudflare routes") || !strings.Contains(logs.String(), "the route to web in bad") {
		t.Errorf("bad's route wasn't logged for the others' deploys: %s", logs.String())
	}

	// The GET of the daemon's exposures carries the token too.
	ex, err := s.Exposures(ctx, 1)
	if err != nil || ex.CloudflareTunnelToken == nil || len(ex.Routes) != 3 {
		t.Errorf("exposures = %+v, %v", ex, err)
	}
	if n, err := s.CloudflareRoutes(ctx); n != 2 || err != nil {
		t.Errorf("cloudflare routes = %d, %v", n, err)
	}
}

// A cloudflare route on a hub without the Cloudflare settings is refused, with how to set them.
func TestCloudflareRouteNeedsTheSettings(t *testing.T) {
	s, fa := managedStore(t)
	s.Tunnels = NewCloudflareTunnels(NewCloudflareStore(s.db, testKey))
	ctx := context.Background()
	_, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose,
		Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("blog.example.com")}}})
	var e *server.Error
	if !errors.As(err, &e) || e.Status != 422 || !strings.Contains(e.Detail, "A cloudflare route needs the hub's Cloudflare settings, and it has none yet. "+
		"Set them with: tender exposures cloudflare set --account-id ID --api-token-file PATH") {
		t.Fatalf("a cloudflare route without settings: %v", err)
	}
	if len(fa.list) != 0 {
		t.Error("a refused deploy was queued")
	}
	// A tailscale service on the same hub deploys, and the tunnels have nothing to do.
	if _, err := s.Create(ctx, 1, Input{Name: "notes", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeTailscale}}}); err != nil {
		t.Fatal(err)
	}
	if d := deployment(t, s, "notes"); d.CloudflareError != "" || d.Exposures.CloudflareTunnelToken != nil {
		t.Errorf("notes = %+v", d)
	}
	if err := s.Tunnels.Apply(ctx, "apps", []expose.Route{{Service: "blog", Container: "web", Host: "blog.example.com"}}); err == nil ||
		!strings.Contains(err.Error(), "tender exposures cloudflare set") {
		t.Errorf("a route through tunnels without settings: %v", err)
	}
}

// The machines with public routes are those whose Caddy listens on 80 and 443.
func TestPublicMachines(t *testing.T) {
	s, _ := managedStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose,
		Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposePublic, Host: ptr("blog.example.com")}}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PublicMachines(ctx)
	if err != nil || !got[1] || len(got) != 1 {
		t.Errorf("public machines = %v, %v", got, err)
	}
}
