package expose

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeDocker is a docker on PATH that logs each call, with TUNNEL_TOKEN from its environment, and
// keeps which of Tender's containers run in files named after them. A file fail-<word> makes any
// call with that word fail.
const fakeDocker = `#!/usr/bin/env bash
dir=$(dirname "$0")
printf '%s|TUNNEL_TOKEN=%s\n' "$*" "${TUNNEL_TOKEN-unset}" >>"$dir/docker.log"
for a in "$@"; do [ -e "$dir/fail-$a" ] && { echo "Error response from daemon: $a is broken" >&2; exit 1; }; done
svc=${@: -1}
case " $* " in
  *" ps "*) [ -e "$dir/running-$svc" ] && echo "c0ffee-$svc"; exit 0 ;;
  *" up "*) touch "$dir/running-$svc" ;;
  *" rm "*) rm -f "$dir/running-$svc" ;;
esac
exit 0
`

// fakeUFW is a ufw on PATH whose rules are the lines of ufw-rules, as "ufw show added" prints
// them, and which is active while ufw-active exists.
const fakeUFW = `#!/usr/bin/env bash
dir=$(dirname "$0")
printf '%s\n' "$*" >>"$dir/ufw.log"
rules="$dir/ufw-rules"; touch "$rules"
case "$1" in
  status) [ -e "$dir/ufw-active" ] && echo "Status: active" || echo "Status: inactive" ;;
  show) echo "Added user rules (see 'ufw status' for running firewall):"; cat "$rules" ;;
  allow) echo "ufw allow $2 comment '$4'" >>"$rules"; echo "Rule added"; echo "Rule added (v6)" ;;
  delete) grep -vE "^ufw allow $3( |$)" "$rules" >"$rules.new"; mv "$rules.new" "$rules"; echo "Rule deleted" ;;
esac
`

// fakes puts the fake docker and ufw first on PATH, and returns their folder.
func fakes(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	for name, body := range map[string]string{"docker": fakeDocker, "ufw": fakeUFW} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) || (err == nil && len(b) == 0) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func dockerCalls(t *testing.T, bin string) []string {
	t.Helper()
	var out []string
	for _, l := range lines(t, filepath.Join(bin, "docker.log")) {
		args, _, _ := strings.Cut(l, "|")
		out = append(out, args)
	}
	return out
}

func reset(t *testing.T, bin string) {
	t.Helper()
	for _, f := range []string{"docker.log", "ufw.log"} {
		_ = os.Remove(filepath.Join(bin, f))
	}
}

var (
	web = Route{App: "blog", Service: "web", LocalPort: 20001, Host: "blog.example.com"}
	api = Route{App: "shop", Service: "api", LocalPort: 20002, Host: "api.shop.example.com"}
)

// The first public route starts Caddy in project tender with host networking and the pinned
// image, writes one site per host, and opens 80 and 443 in an active ufw with Tender's comment.
func TestPublicStartsCaddyOnTheFirstRoute(t *testing.T) {
	bin := fakes(t)
	touch(t, filepath.Join(bin, "ufw-active"))
	dir := filepath.Join(t.TempDir(), "caddy")
	p := &Public{Dir: dir}
	if err := p.Apply(context.Background(), []Route{web, api}); err != nil {
		t.Fatal(err)
	}
	caddy, err := os.ReadFile(filepath.Join(dir, "Caddyfile"))
	if err != nil {
		t.Fatal(err)
	}
	want := "\n# shop/api\napi.shop.example.com {\n\treverse_proxy 127.0.0.1:20002\n}\n\n# blog/web\nblog.example.com {\n\treverse_proxy 127.0.0.1:20001\n}\n"
	if !strings.HasSuffix(string(caddy), want) || !strings.Contains(string(caddy), "admin "+caddyAdmin) || !strings.Contains(string(caddy), "protocols h1 h2") {
		t.Errorf("Caddyfile:\n%s", caddy)
	}
	compose, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	for _, w := range []string{"image: " + CaddyImage, "network_mode: host", "restart: unless-stopped", `- "` + dir + `":/etc/caddy:ro`, `- "` + dir + `/data":/data`} {
		if !strings.Contains(string(compose), w) {
			t.Errorf("compose.yaml lacks %q:\n%s", w, compose)
		}
	}
	for path, mode := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "data"): 0o700, filepath.Join(dir, "compose.yaml"): 0o600} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v, want %v", path, fi.Mode(), err, mode)
		}
	}
	file := filepath.Join(dir, "compose.yaml")
	wantCalls := []string{"compose -p tender -f " + file + " ps --status running --quiet caddy", "compose -p tender -f " + file + " up --detach caddy"}
	if got := dockerCalls(t, bin); !slices.Equal(got, wantCalls) {
		t.Errorf("docker calls:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantCalls, "\n"))
	}
	if got := lines(t, filepath.Join(bin, "ufw-rules")); !slices.Equal(got, []string{"ufw allow 80/tcp comment 'tender public'", "ufw allow 443/tcp comment 'tender public'"}) {
		t.Errorf("ufw rules %q", got)
	}
}

// A change while Caddy runs rewrites the Caddyfile and reloads it inside the container; Apply
// again with the same routes adds no ufw rule twice.
func TestPublicReloadsCaddyWhileItRuns(t *testing.T) {
	bin := fakes(t)
	touch(t, filepath.Join(bin, "ufw-active"))
	dir := filepath.Join(t.TempDir(), "caddy")
	p := &Public{Dir: dir}
	ctx := context.Background()
	if err := p.Apply(ctx, []Route{web}); err != nil {
		t.Fatal(err)
	}
	reset(t, bin)
	if err := p.Apply(ctx, []Route{web, api}); err != nil {
		t.Fatal(err)
	}
	calls := dockerCalls(t, bin)
	file := filepath.Join(dir, "compose.yaml")
	reload := "compose -p tender -f " + file + " exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile --address " + caddyAdmin
	if len(calls) != 3 || calls[2] != reload {
		t.Errorf("docker calls:\n%s", strings.Join(calls, "\n"))
	}
	if caddy, _ := os.ReadFile(filepath.Join(dir, "Caddyfile")); !strings.Contains(string(caddy), "api.shop.example.com {") {
		t.Errorf("Caddyfile:\n%s", caddy)
	}
	if got := lines(t, filepath.Join(bin, "ufw-rules")); len(got) != 2 {
		t.Errorf("ufw rules %q", got)
	}
	if slices.ContainsFunc(lines(t, filepath.Join(bin, "ufw.log")), func(l string) bool { return strings.HasPrefix(l, "allow") }) {
		t.Error("the second apply allowed the ports again")
	}

	// A failed reload is an error that names it, and the next apply tries it again.
	touch(t, filepath.Join(bin, "fail-reload"))
	if err := p.Apply(ctx, []Route{web}); err == nil || !strings.Contains(err.Error(), "reload Caddy") || !strings.Contains(err.Error(), "reload is broken") {
		t.Fatalf("failed reload: %v", err)
	}
	_ = os.Remove(filepath.Join(bin, "fail-reload"))
	reset(t, bin)
	if err := p.Apply(ctx, []Route{web}); err != nil || !slices.ContainsFunc(dockerCalls(t, bin), func(c string) bool { return strings.Contains(c, " reload ") }) {
		t.Errorf("retry: %v %q", err, dockerCalls(t, bin))
	}
}

// A container that was stopped is started, and reads the Caddyfile then; it isn't reloaded.
func TestPublicStartsAStoppedCaddy(t *testing.T) {
	bin := fakes(t)
	dir := filepath.Join(t.TempDir(), "caddy")
	p := &Public{Dir: dir}
	if err := p.Apply(context.Background(), []Route{web}); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(bin, "running-caddy"))
	reset(t, bin)
	if err := p.Apply(context.Background(), []Route{web, api}); err != nil {
		t.Fatal(err)
	}
	if calls := dockerCalls(t, bin); len(calls) != 2 || !strings.HasSuffix(calls[1], "up --detach caddy") {
		t.Errorf("docker calls %q", calls)
	}
}

// After the last route Caddy is removed with its compose file, the certificates stay, and only
// Tender's ufw rules go: a rule of the owner's for the same port stays, and Tender never added one.
func TestPublicStopsAfterTheLastRoute(t *testing.T) {
	bin := fakes(t)
	touch(t, filepath.Join(bin, "ufw-active"))
	if err := os.WriteFile(filepath.Join(bin, "ufw-rules"), []byte("ufw allow 22/tcp\nufw allow 443/tcp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "caddy")
	p := &Public{Dir: dir}
	ctx := context.Background()
	if err := p.Apply(ctx, []Route{web}); err != nil {
		t.Fatal(err)
	}
	if got := lines(t, filepath.Join(bin, "ufw-rules")); !slices.Equal(got, []string{"ufw allow 22/tcp", "ufw allow 443/tcp", "ufw allow 80/tcp comment 'tender public'"}) {
		t.Errorf("ufw rules after the first route %q", got)
	}
	touch(t, filepath.Join(dir, "data", "cert.pem"))
	reset(t, bin)
	if err := p.Apply(ctx, nil); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "compose.yaml")
	if got := dockerCalls(t, bin); !slices.Equal(got, []string{"compose -p tender -f " + file + " rm --stop --force caddy"}) {
		t.Errorf("docker calls %q", got)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("compose.yaml stays: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "cert.pem")); err != nil {
		t.Errorf("the certificates went: %v", err)
	}
	if got := lines(t, filepath.Join(bin, "ufw-rules")); !slices.Equal(got, []string{"ufw allow 22/tcp", "ufw allow 443/tcp"}) {
		t.Errorf("ufw rules after the last route %q", got)
	}

	// With nothing of Tender's left, another empty apply runs no docker at all.
	reset(t, bin)
	if err := p.Apply(ctx, nil); err != nil || len(dockerCalls(t, bin)) != 0 {
		t.Errorf("empty apply: %v %q", err, dockerCalls(t, bin))
	}
}

// An inactive ufw gets no rules, and a machine without ufw works.
func TestPublicLeavesAnInactiveOrMissingUFW(t *testing.T) {
	bin := fakes(t)
	p := &Public{Dir: filepath.Join(t.TempDir(), "caddy")}
	if err := p.Apply(context.Background(), []Route{web}); err != nil {
		t.Fatal(err)
	}
	if got := lines(t, filepath.Join(bin, "ufw-rules")); len(got) != 0 {
		t.Errorf("inactive ufw got rules %q", got)
	}
	p = &Public{Dir: filepath.Join(t.TempDir(), "caddy"), UFW: "tender-no-such-ufw"}
	if err := p.Apply(context.Background(), []Route{web}); err != nil {
		t.Errorf("without ufw: %v", err)
	}
	if err := p.Apply(context.Background(), nil); err != nil {
		t.Errorf("without ufw, last route: %v", err)
	}
}

// Routes with a bad host, a bad port or a host twice fail before anything changes, and a failing
// docker is an error that says what failed.
func TestPublicRefusesBadRoutes(t *testing.T) {
	bin := fakes(t)
	dir := filepath.Join(t.TempDir(), "caddy")
	p := &Public{Dir: dir}
	for _, routes := range [][]Route{
		{{App: "a", Service: "s", LocalPort: 20001, Host: "localhost"}},
		{{App: "a", Service: "s", LocalPort: 20001, Host: "Blog.Example.com"}},
		{{App: "a", Service: "s", LocalPort: 20001, Host: "10.0.0.1"}},
		{{App: "a", Service: "s", LocalPort: 20001, Host: "a.example.com {\n}"}},
		{{App: "a", Service: "s", LocalPort: 0, Host: "a.example.com"}},
		{web, {App: "other", Service: "s", LocalPort: 20009, Host: web.Host}},
	} {
		if err := p.Apply(context.Background(), routes); err == nil {
			t.Errorf("%+v passed", routes)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) || len(dockerCalls(t, bin)) != 0 {
		t.Errorf("a refused apply changed something: %v %q", err, dockerCalls(t, bin))
	}
	touch(t, filepath.Join(bin, "fail-up"))
	if err := p.Apply(context.Background(), []Route{web}); err == nil || !strings.Contains(err.Error(), "start Caddy") || !strings.Contains(err.Error(), "up is broken") {
		t.Errorf("failing up: %v", err)
	}
}

// The security checks count 80/tcp and 443/tcp as allowed only while the machine has public routes.
func TestAllowedPorts(t *testing.T) {
	own := []string{"41641/udp", "443/tcp"}
	if got := AllowedPorts(own, false); !slices.Equal(got, own) {
		t.Errorf("without public routes %q", got)
	}
	if got := AllowedPorts(own, true); !slices.Equal(got, []string{"41641/udp", "443/tcp", "80/tcp"}) {
		t.Errorf("with public routes %q", got)
	}
	if got := AllowedPorts(nil, true); !slices.Equal(got, PublicPorts) {
		t.Errorf("empty list %q", got)
	}
	if own[1] != "443/tcp" || len(own) != 2 {
		t.Error("AllowedPorts changed its argument")
	}
}
