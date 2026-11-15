package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/g4f4r0/tender/internal/apps/expose"
	"github.com/g4f4r0/tender/internal/client"
)

// fakeDocker writes a docker program that logs each call, with its folder and the registry logins
// it was given, and prints rendered for docker compose config. A step named in fail exits 1.
func fakeDocker(t *testing.T, rendered, fail string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log, out := filepath.Join(dir, "calls.log"), filepath.Join(dir, "rendered.json")
	if err := os.WriteFile(out, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "docker")
	script := `#!/bin/sh
echo "$(pwd) | $* | $(cat "$DOCKER_CONFIG/config.json" 2>/dev/null) | checked=$(tr -d '\n\t' < "$DOCKER_CONFIG/compose.checked.json" 2>/dev/null)" >> ` + log + `
for a in "$@"; do last="$a"; done
for a in "$@"; do
  case "$a" in
    config) cat ` + out + `; exit 0 ;;
    ` + fail + `) echo "Error response from daemon: it broke" >&2; exit 1 ;;
  esac
done
echo "compose $last done" >&2
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// fakeExposer records the routes it was given. It refuses the routes of the services in refuse, and
// sets the others; broken fails the whole adapter.
type fakeExposer struct {
	mu     sync.Mutex
	name   string
	got    [][]expose.Route
	refuse map[string]bool
	broken error
	set    []string
}

func (f *fakeExposer) Name() string { return f.name }
func (f *fakeExposer) Apply(_ context.Context, routes []expose.Route) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, routes)
	if f.broken != nil {
		return f.broken
	}
	var errs []error
	f.set = nil
	for _, r := range routes {
		if f.refuse[r.Service] {
			errs = append(errs, &expose.RouteError{Service: r.Service, Container: r.Container,
				Err: errors.New("HTTPS port 8443 is taken, so the route to " + r.Container + " in " + r.Service + " can't have it")})
			continue
		}
		f.set = append(f.set, r.Container+" in "+r.Service)
	}
	return errors.Join(errs...)
}

func newRunner(t *testing.T, docker string, hub *client.Client) (*Runner, *fakeExposer) {
	t.Helper()
	r := NewRunner(hub, "apps", filepath.Join(t.TempDir(), "services"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Docker = docker
	ts := &fakeExposer{name: ExposeTailscale}
	r.Exposers = []expose.Exposer{ts}
	return r, ts
}

func deployment(t *testing.T, d Deployment) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const goodRendered = `{"services":{"web":{"image":"ghcr.io/me/notes:1","mem_limit":"536870912"},"db":{"image":"postgres:17","mem_limit":"1073741824",
	"volumes":[{"type":"volume","source":"pg","target":"/var/lib/postgresql/data"}]}},"volumes":{"pg":{}}}`

func TestDeploy(t *testing.T) {
	docker, log := fakeDocker(t, goodRendered, "never")
	r, ts := newRunner(t, docker, nil)
	https := 8443
	dep := Deployment{Name: "notes", Compose: "services: {}\n", Env: map[string]string{"SECRET_KEY": "s3cret"},
		Routes:     []Published{{Container: "web", Port: 8080, LocalPort: 20000}},
		Registries: []Login{{Host: "ghcr.io", Username: "me", Password: "token"}, {Host: "registry.example.com", Username: "x", Password: "y"}},
		Exposures: Exposures{Routes: []Exposed{{Service: "notes", Container: "web", Expose: ExposeTailscale, LocalPort: 20000, HTTPSPort: &https},
			{Service: "blog", Container: "web", Expose: ExposeTailscale, LocalPort: 20001, HTTPSPort: ptr(443)}}}}
	out, err := r.Deploy(context.Background(), deployment(t, dep))
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "compose 300 done") {
		t.Errorf("output = %q; want the end of compose's", out)
	}
	folder := filepath.Join(r.Dir, "notes")
	got := calls(t, log)
	if len(got) != 3 {
		t.Fatalf("calls = %q", got)
	}
	for i, want := range []string{
		folder + " | compose -p notes -f compose.yaml config --format json | ",
		folder + " | compose -p notes --project-directory " + folder + " -f ",
		folder + " | compose -p notes --project-directory " + folder + " -f ",
	} {
		if !strings.HasPrefix(got[i], want) {
			t.Errorf("call %d = %q, want %q...", i, got[i], want)
		}
	}
	// Pull and up run on the config Tender checked, from a file of its own outside the folder, with
	// the override; never on the compose file again, which a container could have changed.
	for i, step := range []string{"pull", "up -d --remove-orphans --wait --wait-timeout 300"} {
		line := got[i+1]
		if !strings.Contains(line, "/compose.checked.json -f "+folder+"/compose.tender.yaml "+step+" | ") || strings.Contains(line, "-f compose.yaml") ||
			strings.Contains(line, folder+"/compose.checked.json") || !strings.HasSuffix(line, "checked="+strings.NewReplacer("\n", "", "\t", "").Replace(goodRendered)) {
			t.Errorf("%s = %q; want it on the checked config", step, line)
		}
	}
	// Only the logins of the images' registries reach compose, and only for pull and up.
	if !strings.Contains(got[1], `"ghcr.io":{"auth":"bWU6dG9rZW4="}`) || strings.Contains(got[1], "registry.example.com") || strings.Contains(got[0], "ghcr.io") {
		t.Errorf("logins = %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "tender-docker-*")); len(left) > 0 {
		for _, l := range left {
			if b, _ := os.ReadFile(filepath.Join(l, "config.json")); strings.Contains(string(b), "bWU6dG9rZW4=") {
				t.Errorf("a login is left in %s", l)
			}
		}
	}
	var ov struct {
		Services map[string]struct {
			Labels map[string]string `json:"labels"`
			Ports  []string          `json:"ports"`
		} `json:"services"`
		Volumes map[string]struct {
			DriverOpts map[string]string `json:"driver_opts"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(folder, "compose.tender.yaml"))), &ov); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ov.Services["web"].Ports, ",") != "127.0.0.1:20000:8080" || ov.Services["db"].Ports != nil ||
		ov.Services["db"].Labels[Label] != "notes" || ov.Volumes["pg"].DriverOpts["device"] != filepath.Join(folder, "volumes", "pg") {
		t.Errorf("override = %+v", ov)
	}
	if fi, err := os.Stat(filepath.Join(folder, "volumes", "pg")); err != nil || !fi.IsDir() {
		t.Errorf("the volume's folder: %v", err)
	}
	if read(t, filepath.Join(folder, ".env")) != "SECRET_KEY='s3cret'\n" {
		t.Error(".env is wrong")
	}
	// The adapter gets the machine's whole set of its routes, the other services' too.
	if len(ts.got) != 1 || len(ts.got[0]) != 2 || ts.got[0][0] != (expose.Route{Service: "notes", Container: "web", LocalPort: 20000, HTTPSPort: 8443}) {
		t.Errorf("tailscale got %+v", ts.got)
	}
	if !r.Managed("notes") || r.Managed("blog") || r.Managed("../x") {
		t.Error("Managed is wrong")
	}
}

// A compose file that breaks a rule fails before anything is pulled, with the rule's sentence, and
// a failing step says which one with the end of compose's output.
func TestDeployRefused(t *testing.T) {
	docker, log := fakeDocker(t, `{"services":{"web":{"image":"nginx","mem_limit":"1","ports":[{"target":80}]}}}`, "never")
	r, ts := newRunner(t, docker, nil)
	_, err := r.Deploy(context.Background(), deployment(t, Deployment{Name: "notes", Compose: "x"}))
	if err == nil || err.Error() != "service web in the compose file publishes ports, which Tender's routes do instead: remove ports from web and add a route to its port, such as --route web:80:tailscale" {
		t.Errorf("refused deploy: %v", err)
	}
	if got := calls(t, log); len(got) != 1 || ts.got != nil {
		t.Errorf("after a refused deploy: calls %q, exposed %v", got, ts.got)
	}

	// A bind through a symbolic link in the folder, which a container could have made, is refused
	// before up.
	folder := filepath.Join(r.Dir, "notes")
	if err := os.MkdirAll(filepath.Join(folder, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(folder, "data", "x")); err != nil {
		t.Fatal(err)
	}
	docker, log = fakeDocker(t, `{"services":{"web":{"image":"nginx","mem_limit":"1","volumes":[{"type":"bind","source":"`+folder+`/data/x/etc","target":"/etc"}]}}}`, "never")
	r.Docker = docker
	_, err = r.Deploy(context.Background(), deployment(t, Deployment{Name: "notes", Compose: "x"}))
	if err == nil || !strings.Contains(err.Error(), "service web in the compose file mounts "+folder+"/data/x/etc, and "+folder+"/data/x in its folder is a symbolic link") {
		t.Errorf("a bind through a link: %v", err)
	}
	if got := calls(t, log); len(got) != 2 || !strings.Contains(got[1], " pull | ") {
		t.Errorf("a bind through a link reached up: %q", got)
	}
	if err := os.Remove(filepath.Join(folder, "data", "x")); err != nil {
		t.Fatal(err)
	}

	docker, _ = fakeDocker(t, goodRendered, "up")
	r.Docker = docker
	out, err := r.Deploy(context.Background(), deployment(t, Deployment{Name: "notes", Compose: "x"}))
	if err == nil || !strings.HasPrefix(err.Error(), "docker compose up exited with status 1: the containers did not all start and turn healthy. The end of its output:\n") ||
		!strings.Contains(err.Error(), "it broke") || !strings.Contains(out, "it broke") {
		t.Errorf("failed up: %v, %q", err, out)
	}
	if _, err := r.Deploy(context.Background(), json.RawMessage(`null`)); err == nil {
		t.Error("a deploy without a service ran")
	}
	if _, err := r.Deploy(context.Background(), deployment(t, Deployment{Name: "Bad-Name", Compose: "x"})); err == nil {
		t.Error("a bad name was deployed")
	}
	if _, err := r.Deploy(context.Background(), deployment(t, Deployment{Name: "notes", Compose: "x", Files: map[string]string{"../x": "y"}})); err == nil {
		t.Error("a file outside the folder was taken")
	}
}

// Remove takes the containers down and keeps the folder aside, or deletes it with delete_data.
func TestRemove(t *testing.T) {
	docker, log := fakeDocker(t, goodRendered, "never")
	r, ts := newRunner(t, docker, nil)
	ctx := context.Background()
	for _, deleteData := range []bool{false, true} {
		if _, err := r.Deploy(ctx, deployment(t, Deployment{Name: "notes", Compose: "x"})); err != nil {
			t.Fatal(err)
		}
		out, err := r.Remove(ctx, deployment(t, Deployment{Name: "notes"}), deleteData)
		if err != nil {
			t.Fatal(err)
		}
		got := calls(t, log)
		if last := got[len(got)-1]; !strings.HasPrefix(last, filepath.Join(r.Dir, "notes")+" | compose -p notes down --remove-orphans | ") {
			t.Errorf("down = %q", last)
		}
		if _, err := os.Stat(filepath.Join(r.Dir, "notes")); !os.IsNotExist(err) {
			t.Errorf("the folder is still there: %v", err)
		}
		kept, _ := filepath.Glob(filepath.Join(r.Dir, ".removed", "notes-*"))
		switch {
		case !deleteData && (len(kept) != 1 || !strings.Contains(out, "Kept the service's folder, data and all, in "+kept[0])):
			t.Errorf("kept %v, output %q", kept, out)
		case deleteData && (len(kept) != 1 || !strings.Contains(out, "Deleted the service's folder and its data.")): // the first one's
			t.Errorf("with delete_data: kept %v, output %q", kept, out)
		}
	}
	if len(ts.got) != 4 {
		t.Errorf("the exposures were applied %d times, want after each deploy and remove", len(ts.got))
	}
	// A service whose folder is gone still goes down, by its project name.
	if _, err := r.Remove(ctx, deployment(t, Deployment{Name: "gone"}), false); err != nil {
		t.Error(err)
	}
	docker, _ = fakeDocker(t, goodRendered, "down")
	r.Docker = docker
	if _, err := r.Remove(ctx, deployment(t, Deployment{Name: "gone"}), false); err == nil || !strings.Contains(err.Error(), "the service's containers may still run") {
		t.Errorf("failed down: %v", err)
	}
}

func TestAct(t *testing.T) {
	docker, log := fakeDocker(t, goodRendered, "never")
	r, _ := newRunner(t, docker, nil)
	ctx := context.Background()
	if err := r.Act(ctx, "restart", "notes"); err == nil {
		t.Error("an action on a service that isn't deployed ran")
	}
	if _, err := r.Deploy(ctx, deployment(t, Deployment{Name: "notes", Compose: "x"})); err != nil {
		t.Fatal(err)
	}
	if err := r.Act(ctx, "stop", "notes"); err != nil {
		t.Fatal(err)
	}
	got := calls(t, log)
	if last := got[len(got)-1]; !strings.HasPrefix(last, filepath.Join(r.Dir, "notes")+" | compose -p notes -f compose.yaml -f compose.tender.yaml stop | ") {
		t.Errorf("stop = %q", last)
	}
}

// Run applies the machine's exposures as the hub has them, once, and tells the hub the tailnet name.
func TestRunAppliesTheExposures(t *testing.T) {
	var mu sync.Mutex
	var status []string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "GET /v1/machines/apps/exposures":
			_, _ = io.WriteString(w, `{"routes":[{"service":"notes","container":"web","expose":"tailscale","local_port":20000,"host":null,"https_port":443},
				{"service":"blog","container":"web","expose":"cloudflare","local_port":20001,"host":"blog.example.com","https_port":null}],
				"cloudflare_tunnel_token":null}`)
		case "PUT /v1/machines/apps/exposures/status":
			b, _ := io.ReadAll(req.Body)
			mu.Lock()
			status = append(status, string(b))
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(hub.Close)
	r, ts := newRunner(t, "docker", client.New(hub.URL, "token-a"))
	r.TailnetName = func(context.Context) (string, error) { return "apps.tail1234.ts.net", nil }
	r.Run(context.Background())
	if len(ts.got) != 1 || len(ts.got[0]) != 1 || ts.got[0][0].HTTPSPort != 443 {
		t.Errorf("tailscale got %+v", ts.got)
	}
	if len(status) != 1 || strings.TrimSpace(status[0]) != `{"tailscale_dns_name":"apps.tail1234.ts.net"}` {
		t.Errorf("status = %q", status)
	}
	// The cloudflare route has no adapter on this daemon: that is an error to say, not a silent skip.
	err := r.expose(context.Background(), Exposures{Routes: []Exposed{{Service: "blog", Container: "web", Expose: ExposeCloudflare, LocalPort: 20001}}}, "blog", nil)
	if err == nil || !strings.Contains(err.Error(), "this daemon can't expose cloudflare routes, such as the one to web in blog") {
		t.Errorf("a route without its adapter: %v", err)
	}
	if len(status) != 1 {
		t.Error("an unchanged tailnet name was sent again")
	}
}

// One service's route that can't be set fails only that service's deploy: the other routes are
// still set, and the other services deploy.
func TestOneRouteFailsOnlyItsService(t *testing.T) {
	docker, _ := fakeDocker(t, goodRendered, "never")
	r, ts := newRunner(t, docker, nil)
	ts.refuse = map[string]bool{"bad": true}
	ex := Exposures{Routes: []Exposed{
		{Service: "bad", Container: "web", Expose: ExposeTailscale, LocalPort: 20002, HTTPSPort: ptr(8443)},
		{Service: "blog", Container: "web", Expose: ExposeTailscale, LocalPort: 20001, HTTPSPort: ptr(444)},
		{Service: "notes", Container: "web", Expose: ExposeTailscale, LocalPort: 20000, HTTPSPort: ptr(443)},
	}}
	notes := Deployment{Name: "notes", Compose: "services: {}\n", Exposures: ex,
		Routes: []Published{{Container: "web", Port: 8080, LocalPort: 20000, Expose: ExposeTailscale}}}
	if out, err := r.Deploy(context.Background(), deployment(t, notes)); err != nil {
		t.Fatalf("notes failed for bad's route: %v\n%s", err, out)
	}
	if strings.Join(ts.set, ", ") != "web in blog, web in notes" {
		t.Errorf("set = %q; want every route but bad's", ts.set)
	}
	bad := notes
	bad.Name = "bad"
	bad.Routes = []Published{{Container: "web", Port: 80, LocalPort: 20002, Expose: ExposeTailscale}}
	_, err := r.Deploy(context.Background(), deployment(t, bad))
	if err == nil || err.Error() != "the service runs, but its routes could not all be exposed: the tailscale routes: "+
		"HTTPS port 8443 is taken, so the route to web in bad can't have it" {
		t.Errorf("bad's deploy: %v", err)
	}
	if strings.Join(ts.set, ", ") != "web in blog, web in notes" {
		t.Errorf("set = %q after bad's deploy", ts.set)
	}

	// An adapter that fails as a whole fails the deploys of the services that use it, not the others.
	ts.broken = errors.New("tailscale is not running")
	if _, err := r.Deploy(context.Background(), deployment(t, notes)); err == nil || !strings.Contains(err.Error(), "the tailscale routes: tailscale is not running") {
		t.Errorf("notes with tailscale down: %v", err)
	}
	plain := notes
	plain.Name, plain.Routes = "plain", nil
	if out, err := r.Deploy(context.Background(), deployment(t, plain)); err != nil {
		t.Errorf("a service without tailscale routes failed for tailscale: %v\n%s", err, out)
	}
}

func ptr[T any](v T) *T { return &v }
