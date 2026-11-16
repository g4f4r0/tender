package services

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services/deploy"
	"github.com/g4f4r0/tender/internal/services/expose"
)

// fakeTunnels records each Apply. It refuses the routes of the services in refuse, and fails as a
// whole with broken. Each Apply takes delay, or until its context ends with block; running counts
// the Applys at once, and most the most there ever were.
type fakeTunnels struct {
	mu            sync.Mutex
	applied       [][]expose.Route
	refuse        map[string]bool
	broken        error
	ready         error
	delay         time.Duration
	block         bool
	running, most int
}

func (f *fakeTunnels) Ready(context.Context) error { return f.ready }

func (f *fakeTunnels) Apply(ctx context.Context, machine string, routes []expose.Route) (expose.Tunnel, error) {
	f.mu.Lock()
	f.running++
	f.most = max(f.most, f.running)
	delay, block := f.delay, f.block
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
	}()
	if block {
		<-ctx.Done()
		return expose.Tunnel{}, ctx.Err()
	}
	time.Sleep(delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, routes)
	if f.broken != nil {
		return expose.Tunnel{}, f.broken
	}
	var errs []error
	for _, r := range routes {
		if f.refuse[r.Service] {
			errs = append(errs, &expose.RouteError{Service: r.Service, Container: r.Container,
				Err: errors.New(r.Host + " already has a DNS record Tender didn't make, so the route to " + r.Container + " in " + r.Service + " can't have it")})
		}
	}
	tun := expose.Tunnel{ID: "tunnel-of-" + machine}
	if len(routes) > 0 {
		tun.Token = "tunnel-token-of-" + machine
	}
	return tun, errors.Join(errs...)
}

func (f *fakeTunnels) applies() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

func (f *fakeTunnels) lastApplied() []expose.Route {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applied[len(f.applied)-1]
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

// Every deploy and remove brings the machine's tunnel up to date with its cloudflare routes when
// it is queued, and the exposures carry the stored token. A route the tunnel can't take fails only
// its own service; Cloudflare failing as a whole fails the services that have cloudflare routes.
func TestDeploymentSyncsTheTunnel(t *testing.T) {
	s, _ := managedStore(t)
	ft := &fakeTunnels{refuse: map[string]bool{"bad": true}}
	logs := &syncBuffer{}
	s.Tunnels, s.Log = ft, slog.New(slog.NewTextHandler(logs, nil))
	ctx := context.Background()
	for _, in := range []Input{
		{Name: "blog", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("blog.example.com")}}},
		{Name: "bad", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("taken.example.com")}}},
		{Name: "notes", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeTailscale}}},
	} {
		if _, err := s.Create(ctx, 1, in); err != nil {
			t.Fatal(err)
		}
	}

	notes := deployment(t, s, "notes")
	if notes.CloudflareError != "" {
		t.Errorf("bad's route failed notes: %q", notes.CloudflareError)
	}
	if got := ft.lastApplied(); len(got) != 2 || got[0].Service != "bad" || got[1].Service != "blog" || got[1].Host != "blog.example.com" {
		t.Errorf("the tunnel got %+v; want the machine's two cloudflare routes", got)
	}
	if notes.Exposures.CloudflareTunnelToken == nil || *notes.Exposures.CloudflareTunnelToken != "tunnel-token-of-a" {
		t.Errorf("exposures token = %v", notes.Exposures.CloudflareTunnelToken)
	}
	if blog := deployment(t, s, "blog"); blog.CloudflareError != "" {
		t.Errorf("blog's own route failed: %q", blog.CloudflareError)
	}
	if bad := deployment(t, s, "bad"); bad.CloudflareError != "taken.example.com already has a DNS record Tender didn't make, so the route to web in bad can't have it" {
		t.Errorf("bad's own error = %q", bad.CloudflareError)
	}
	if !strings.Contains(logs.String(), "could not route all of the machine's cloudflare routes") || !strings.Contains(logs.String(), "the route to web in bad") {
		t.Errorf("bad's route wasn't logged for the others' deploys: %s", logs.String())
	}

	ft.broken = errors.New("Cloudflare answered 503 three times")
	for _, name := range []string{"blog", "notes"} {
		if _, err := s.Redeploy(ctx, 1, name); err != nil {
			t.Fatal(err)
		}
	}
	ft.broken = nil
	if blog := deployment(t, s, "blog"); !strings.Contains(blog.CloudflareError, "Cloudflare answered 503") {
		t.Errorf("Cloudflare down, blog = %q", blog.CloudflareError)
	}
	if notes := deployment(t, s, "notes"); notes.CloudflareError != "" {
		t.Errorf("Cloudflare down failed notes, which has no cloudflare route: %q", notes.CloudflareError)
	}
	// Cloudflare failing as a whole keeps the token of the last sync.
	ex, err := s.Exposures(ctx, 1)
	if err != nil || ex.CloudflareTunnelToken == nil || *ex.CloudflareTunnelToken != "tunnel-token-of-a" || len(ex.Routes) != 3 {
		t.Errorf("exposures = %+v, %v", ex, err)
	}
	if n, err := s.CloudflareRoutes(ctx); n != 2 || err != nil {
		t.Errorf("cloudflare routes = %d, %v", n, err)
	}
}

// The daemon's poll and GET of the exposures only read what the sync stored: they never call
// Cloudflare, and the token is kept sealed in the database, so a restarted hub has it.
func TestPollReadsTheStoredSync(t *testing.T) {
	s, _ := managedStore(t)
	ft := &fakeTunnels{}
	s.Tunnels = ft
	ctx := context.Background()
	if _, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose,
		Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("blog.example.com")}}}); err != nil {
		t.Fatal(err)
	}
	if n := ft.applies(); n != 1 {
		t.Fatalf("queueing the deploy synced %d times", n)
	}
	restarted := NewStore(s.db)
	restarted.Key, restarted.Actions, restarted.Tunnels = s.Key, s.Actions, &fakeTunnels{broken: errors.New("no Cloudflare call is expected")}
	d := deployment(t, restarted, "blog")
	ex, err := restarted.Exposures(ctx, 1)
	if err != nil || d.Exposures.CloudflareTunnelToken == nil || ex.CloudflareTunnelToken == nil || *ex.CloudflareTunnelToken != "tunnel-token-of-a" {
		t.Errorf("after a restart, deployment %+v and exposures %+v, %v", d.Exposures, ex, err)
	}
	if n := ft.applies() + restarted.Tunnels.(*fakeTunnels).applies(); n != 1 {
		t.Errorf("the poll and the exposures called Cloudflare: %d syncs", n)
	}
	var sealed string
	if err := s.db.Read.QueryRow(`SELECT cloudflare_tunnel_token FROM service_exposures WHERE machine_id = 1`).Scan(&sealed); err != nil || strings.Contains(sealed, "tunnel-token") {
		t.Errorf("stored token = %q, %v; want it sealed", sealed, err)
	}
}

// A sync that Cloudflare keeps waiting stops at its deadline, and fails the service's deploy with
// why, rather than holding the request.
func TestTunnelSyncHasADeadline(t *testing.T) {
	s, fa := managedStore(t)
	s.Tunnels = &fakeTunnels{block: true}
	defer func(d time.Duration) { tunnelSyncTimeout = d }(tunnelSyncTimeout)
	tunnelSyncTimeout = 50 * time.Millisecond
	start := time.Now()
	if _, err := s.Create(context.Background(), 1, Input{Name: "blog", Compose: notesCompose,
		Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("blog.example.com")}}}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the sync took %s", took)
	}
	if d := deployment(t, s, "blog"); !strings.Contains(d.CloudflareError, "took longer than 50ms, so it stopped") {
		t.Errorf("cloudflare_error = %q", d.CloudflareError)
	}
	if len(fa.list) != 1 {
		t.Errorf("actions = %d", len(fa.list))
	}
}

// Two syncs of one machine never run at once.
func TestOneSyncPerMachineAtATime(t *testing.T) {
	s, _ := managedStore(t)
	ft := &fakeTunnels{delay: 30 * time.Millisecond}
	s.Tunnels = ft
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for _, name := range []string{"one", "two", "three", "four"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Create(ctx, 1, Input{Name: name, Compose: notesCompose,
				Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr(name + ".example.com")}}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ft.most != 1 || ft.applies() != 4 {
		t.Errorf("%d syncs ran at once, of %d", ft.most, ft.applies())
	}
	if got := ft.lastApplied(); len(got) != 4 {
		t.Errorf("the last sync had %d routes; want all four", len(got))
	}
}

// Dropping a service's last cloudflare route while Cloudflare fails as a whole fails its deploy:
// the routes it had count, not only those it keeps.
func TestDroppingTheLastCloudflareRouteStillFails(t *testing.T) {
	s, _ := managedStore(t)
	ft := &fakeTunnels{}
	s.Tunnels = ft
	ctx := context.Background()
	if _, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose,
		Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("blog.example.com")}}}); err != nil {
		t.Fatal(err)
	}
	ft.broken = errors.New("Cloudflare answered 503 three times")
	if _, err := s.Update(ctx, 1, "blog", Change{Routes: &[]RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeTailscale}}}); err != nil {
		t.Fatal(err)
	}
	if d := deployment(t, s, "blog"); !strings.Contains(d.CloudflareError, "Cloudflare answered 503") {
		t.Errorf("blog dropped its cloudflare route with Cloudflare down, and cloudflare_error = %q", d.CloudflareError)
	}
}

// A remove takes the service's cloudflare routes out of the tunnel when it is queued; when it
// fails, the service is still there, so they come back.
func TestFailedRemoveKeepsTheCloudflareRoutes(t *testing.T) {
	s, fa := managedStore(t)
	ft := &fakeTunnels{}
	s.Tunnels = ft
	ctx := context.Background()
	if _, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose,
		Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("blog.example.com")}}}); err != nil {
		t.Fatal(err)
	}
	fa.finish("completed", "")
	if _, err := s.Remove(ctx, 1, "blog", false); err != nil {
		t.Fatal(err)
	}
	if got := ft.lastApplied(); len(got) != 0 {
		t.Fatalf("the remove's sync kept %+v", got)
	}
	fa.finish("failed", "docker compose down exited with status 1")
	if _, err := s.List(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := ft.lastApplied(); len(got) != 1 || got[0].Host != "blog.example.com" {
		t.Errorf("after the failed remove the tunnel has %+v; want blog's route back", got)
	}
	if ex, _ := s.Exposures(ctx, 1); len(ex.Routes) != 1 {
		t.Errorf("exposures = %+v", ex)
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
	if _, err := s.Tunnels.Apply(ctx, "apps", []expose.Route{{Service: "blog", Container: "web", Host: "blog.example.com"}}); err == nil ||
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

// A cloudflare route's access is stored, shown and passed to the tunnel. A change of access alone
// is a route change: it keeps the route's local port and syncs the tunnel again. A change that
// leaves the routes out keeps access.
func TestRouteAccess(t *testing.T) {
	s, _ := managedStore(t)
	ft := &fakeTunnels{}
	s.Tunnels = ft
	ctx := context.Background()
	route := RouteInput{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("blog.example.com"), Access: true}
	q, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose, Routes: []RouteInput{route}})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Service.Routes) != 1 || !q.Service.Routes[0].Access {
		t.Fatalf("created routes = %+v", q.Service.Routes)
	}
	local := q.Service.Routes[0].LocalPort
	if got := ft.lastApplied(); len(got) != 1 || !got[0].Access || got[0].Host != "blog.example.com" {
		t.Errorf("the tunnel got %+v; want the route with access", got)
	}
	var stored int
	if err := s.db.Read.QueryRow(`SELECT access FROM service_routes`).Scan(&stored); err != nil || stored != 1 {
		t.Errorf("stored access = %d, %v", stored, err)
	}
	sv, err := s.Get(ctx, 1, "blog")
	if b, _ := json.Marshal(sv.Routes); err != nil || !strings.Contains(string(b), `"access":true`) {
		t.Errorf("the API's routes = %s, %v", b, err)
	}
	// The daemon's exposures never carry it.
	if ex, err := s.Exposures(ctx, 1); err != nil || len(ex.Routes) != 1 || !ex.Routes[0].Access {
		t.Errorf("exposures = %+v, %v", ex, err)
	} else if b, _ := json.Marshal(ex); strings.Contains(string(b), "access") {
		t.Errorf("the daemon's exposures show access: %s", b)
	}

	applies := ft.applies()
	if _, err := s.Update(ctx, 1, "blog", Change{Compose: ptr(notesCompose)}); err != nil {
		t.Fatal(err)
	}
	if got := ft.lastApplied(); ft.applies() != applies+1 || len(got) != 1 || !got[0].Access {
		t.Errorf("a compose change dropped access: %+v", got)
	}
	route.Access = false
	q, err = s.Update(ctx, 1, "blog", Change{Routes: &[]RouteInput{route}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ft.lastApplied(); ft.applies() != applies+2 || len(got) != 1 || got[0].Access {
		t.Errorf("turning access off synced %d times, with %+v", ft.applies()-applies-1, got)
	}
	if r := q.Service.Routes; len(r) != 1 || r[0].Access || r[0].LocalPort != local {
		t.Errorf("routes after access went off = %+v; want local port %d kept", r, local)
	}

	// The schema refuses access on any other route, as the input does.
	if _, err := s.db.Write.Exec(`UPDATE service_routes SET expose = 'public', access = 1`); err == nil {
		t.Error("the schema took access on a public route")
	}
}

// Only a cloudflare route can require Cloudflare Access.
func TestAccessIsOnlyForCloudflare(t *testing.T) {
	s, fa := managedStore(t)
	s.Tunnels = &fakeTunnels{}
	ctx := context.Background()
	for _, r := range []RouteInput{
		{Container: "web", Port: 80, Expose: deploy.ExposeTailscale, Access: true},
		{Container: "web", Port: 80, Expose: deploy.ExposePublic, Host: ptr("blog.example.com"), Access: true},
	} {
		_, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose, Routes: []RouteInput{r}})
		want := "routes[0]: access is only for a cloudflare route, whose tunnel checks Cloudflare Access, so the " + r.Expose +
			" route to web can't have it. Expose it through cloudflare, or leave access out."
		var e *server.Error
		if !errors.As(err, &e) || e.Status != 422 || e.Detail != want {
			t.Errorf("%s with access: %v, want %q", r.Expose, err, want)
		}
	}
	if len(fa.list) != 0 {
		t.Error("a refused deploy was queued")
	}
}
