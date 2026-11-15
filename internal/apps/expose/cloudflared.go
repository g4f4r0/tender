package expose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Cloudflared runs the machine's Cloudflare tunnel: cloudflared as service cloudflared of project
// tender, with host networking, so the tunnel reaches each route's 127.0.0.1 port. The hub keeps
// the tunnel's ingress and DNS in Cloudflare, so the routes only say whether it runs: it starts on
// the first route and is removed after the last.
type Cloudflared struct {
	// Dir holds the compose file; it is /var/lib/tender/daemon/cloudflared on a machine.
	Dir    string
	Docker string // the docker program; docker when empty
	// Token is the tunnel token the hub sent with the routes. It reaches cloudflared through the
	// environment of docker compose, never an argument or a file.
	Token string
}

// Name is the adapter's name in a route's expose.
func (c *Cloudflared) Name() string { return "cloudflare" }

func (c *Cloudflared) service() service {
	return service{docker: orDefault(c.Docker, "docker"), file: filepath.Join(c.Dir, "compose.yaml"), name: "cloudflared"}
}

// cloudflaredCompose is cloudflared's compose file. TUNNEL_TOKEN, with no value, comes from the
// environment docker compose runs in. The metrics listen on a loopback port the kernel picks, so
// they neither reach the public address nor take a port of a service's.
var cloudflaredCompose = fmt.Appendf(nil, `# Written by Tender: the cloudflared that runs this machine's Cloudflare tunnel.
services:
  cloudflared:
    image: %s
    network_mode: host
    restart: unless-stopped
    command: ["tunnel", "--metrics", "127.0.0.1:0", "run"]
    environment:
      - TUNNEL_TOKEN
`, CloudflaredImage)

// Apply runs cloudflared while routes has a good route, and removes it otherwise. Compose recreates
// the container when the token changes. A bad route gets a *RouteError; the hub's ingress says
// which hosts the tunnel serves.
func (c *Cloudflared) Apply(ctx context.Context, routes []Route) error {
	good, errs := hostRoutes("cloudflare", routes)
	if err := c.apply(ctx, good); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// apply runs cloudflared while routes, which hostRoutes passed, has a route.
func (c *Cloudflared) apply(ctx context.Context, routes []Route) error {
	svc := c.service()
	if len(routes) == 0 {
		if err := svc.remove(ctx); err != nil {
			return fmt.Errorf("stop cloudflared: %w", err)
		}
		return nil
	}
	if c.Token == "" {
		return errors.New("the hub sent no Cloudflare tunnel token with the cloudflare routes. Check the hub's Cloudflare settings with: tender exposures cloudflare show")
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	if _, err := writeFile(svc.file, cloudflaredCompose, 0o600); err != nil {
		return err
	}
	if err := svc.up(ctx, []string{"TUNNEL_TOKEN=" + c.Token}); err != nil {
		return errors.New("start cloudflared: " + strings.ReplaceAll(err.Error(), c.Token, "***"))
	}
	return nil
}
