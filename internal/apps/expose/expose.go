// Package expose makes a machine's app routes reachable: through Tailscale Serve, a Cloudflare
// tunnel, or Caddy on the public internet. The daemon applies the whole desired set for its
// machine after every deploy and remove, and on start. Each adapter is idempotent and owns only
// what it created.
package expose

import "context"

// Route is one route of an app, as an adapter needs it.
type Route struct {
	App, Service string
	LocalPort    int    // 127.0.0.1 port the app's service is published on
	Host         string // cloudflare and public
	HTTPSPort    int    // tailscale
}

// Exposer is one way of exposing routes.
type Exposer interface {
	Name() string                                    // tailscale, cloudflare, public
	Apply(ctx context.Context, routes []Route) error // the whole set for this adapter
}
