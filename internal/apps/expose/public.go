package expose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// PublicPorts are the ports Caddy listens on, on every address of the machine, while it has public
// routes. HTTP/3 is off, so 443/udp stays closed.
var PublicPorts = []string{"80/tcp", "443/tcp"}

// AllowedPorts is a machine's allowlist for the security checks: allowed, its own, plus PublicPorts
// while it has public routes, so Caddy's ports don't count as exposed.
func AllowedPorts(allowed []string, hasPublicRoutes bool) []string {
	out := slices.Clone(allowed)
	if hasPublicRoutes {
		for _, p := range PublicPorts {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// ufwComment marks the ufw rules the public adapter adds; it removes only rules that carry it.
const ufwComment = "tender public"

// Public serves routes on the public internet with automatic HTTPS, through one Caddy container
// with host networking: service caddy of project tender. It starts the container on the first
// route and removes it after the last. While routes exist and ufw is active, it allows 80/tcp and
// 443/tcp in ufw. DNS and the provider's firewall are the owner's.
type Public struct {
	// Dir holds the Caddyfile and the compose file, and data/, Caddy's certificates; it is
	// /var/lib/tender/daemon/caddy on a machine.
	Dir    string
	Docker string // the docker program; docker when empty
	UFW    string // the ufw program; ufw when empty
}

// Name is the adapter's name in a route's expose.
func (p *Public) Name() string { return "public" }

func (p *Public) service() service {
	return service{docker: orDefault(p.Docker, "docker"), file: filepath.Join(p.Dir, "compose.yaml"), name: "caddy"}
}

// Apply makes Caddy serve exactly routes, each on https://<host>.
func (p *Public) Apply(ctx context.Context, routes []Route) error {
	routes, err := hostRoutes("public", routes)
	if err != nil {
		return err
	}
	svc := p.service()
	if len(routes) == 0 {
		if err := svc.remove(ctx); err != nil {
			return fmt.Errorf("stop Caddy: %w", err)
		}
		return p.closePorts(ctx)
	}
	if err := os.MkdirAll(filepath.Join(p.Dir, "data"), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(p.Dir, 0o700); err != nil {
		return err
	}
	if err := p.openPorts(ctx); err != nil {
		return err
	}
	if _, err := writeFile(filepath.Join(p.Dir, "Caddyfile"), caddyfile(routes), 0o644); err != nil {
		return err
	}
	composeChanged, err := writeFile(svc.file, p.compose(), 0o600)
	if err != nil {
		return err
	}
	running, err := svc.running(ctx)
	if err != nil {
		return fmt.Errorf("ask Docker about Caddy: %w", err)
	}
	if err := svc.up(ctx, nil); err != nil {
		return fmt.Errorf("start Caddy: %w", err)
	}
	// A container that up started or recreated read the Caddyfile as it started. One that kept
	// running reloads it; Caddy changes nothing when the file is the same, and a reload that failed
	// before is tried again.
	if running && !composeChanged {
		if _, err := svc.compose(ctx, nil, "exec", "-T", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile",
			"--adapter", "caddyfile", "--address", caddyAdmin); err != nil {
			return fmt.Errorf("reload Caddy: %w", err)
		}
	}
	return nil
}

// caddyAdmin is Caddy's admin endpoint: a socket inside the container, so no process on the
// machine can reconfigure Caddy through a port.
const caddyAdmin = "unix//tmp/caddy-admin.sock"

// caddyfile is the Caddyfile for routes, sorted by host.
func caddyfile(routes []Route) []byte {
	var b strings.Builder
	b.WriteString("# Written by Tender for this machine's public routes. Tender replaces it on every deploy.\n")
	b.WriteString("{\n\tadmin " + caddyAdmin + "\n\tservers {\n\t\tprotocols h1 h2\n\t}\n}\n")
	for _, r := range routes {
		fmt.Fprintf(&b, "\n# %s/%s\n%s {\n\treverse_proxy 127.0.0.1:%d\n}\n", r.App, r.Service, r.Host, r.LocalPort)
	}
	return []byte(b.String())
}

// compose is Caddy's compose file. The folder is mounted read-only as /etc/caddy, rather than the
// Caddyfile alone, because a new Caddyfile is a new file that a file mount wouldn't show.
func (p *Public) compose() []byte {
	return fmt.Appendf(nil, `# Written by Tender: the Caddy that serves this machine's public routes.
services:
  caddy:
    image: %s
    network_mode: host
    restart: unless-stopped
    volumes:
      - %s:/etc/caddy:ro
      - %s:/data
`, CaddyImage, yamlQuote(p.Dir), yamlQuote(filepath.Join(p.Dir, "data")))
}

// yamlQuote is s as a YAML double-quoted string.
func yamlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// ufw runs ufw. Without ufw installed, ok is false and nothing ran.
func (p *Public) ufw(ctx context.Context, args ...string) (out string, ok bool, err error) {
	out, err = run(ctx, nil, orDefault(p.UFW, "ufw"), args...)
	if errors.Is(err, exec.ErrNotFound) {
		return "", false, nil
	}
	return out, true, err
}

// added returns the rules "ufw show added" lists for each of PublicPorts: the whole line, such as
// ufw allow 80/tcp comment 'tender public'. It lists them whether ufw is active or not.
func (p *Public) added(ctx context.Context) (map[string]string, bool, error) {
	out, ok, err := p.ufw(ctx, "show", "added")
	if !ok || err != nil {
		return nil, ok, err
	}
	rules := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		for _, port := range PublicPorts {
			if line == "ufw allow "+port || strings.HasPrefix(line, "ufw allow "+port+" ") {
				rules[port] = line
			}
		}
	}
	return rules, true, nil
}

func ours(rule string) bool { return strings.HasSuffix(rule, " comment '"+ufwComment+"'") }

// openPorts allows PublicPorts in ufw while it is active. A port that already has an allow rule
// of the owner's keeps it, and the adapter adds none, so it never takes over the owner's rule.
func (p *Public) openPorts(ctx context.Context) error {
	status, ok, err := p.ufw(ctx, "status")
	if !ok {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ask ufw whether it is active: %w", err)
	}
	if !strings.Contains(status, "Status: active") {
		return nil
	}
	rules, _, err := p.added(ctx)
	if err != nil {
		return fmt.Errorf("read ufw's rules: %w", err)
	}
	for _, port := range PublicPorts {
		if _, has := rules[port]; has {
			continue
		}
		if _, _, err := p.ufw(ctx, "allow", port, "comment", ufwComment); err != nil {
			return fmt.Errorf("allow %s in ufw for Caddy: %w", port, err)
		}
	}
	return nil
}

// closePorts deletes the ufw rules openPorts added, and no other.
func (p *Public) closePorts(ctx context.Context) error {
	rules, ok, err := p.added(ctx)
	if !ok {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read ufw's rules: %w", err)
	}
	for _, port := range PublicPorts {
		if rule, has := rules[port]; has && ours(rule) {
			if _, _, err := p.ufw(ctx, "delete", "allow", port); err != nil {
				return fmt.Errorf("delete Tender's ufw rule for %s: %w", port, err)
			}
		}
	}
	return nil
}
