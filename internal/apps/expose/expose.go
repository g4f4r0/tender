// Package expose makes apps' routes reachable: each adapter takes the whole set of routes of its
// type on this machine and makes the machine match it. An adapter is idempotent and changes only
// what it created.
package expose

import "context"

// Route is one route of an app, as the daemon gets it from the hub.
type Route struct {
	App, Service string
	LocalPort    int    // 127.0.0.1 port the app's service is published on
	Host         string // cloudflare and public
	HTTPSPort    int    // tailscale
}

// Exposer is one exposure adapter.
type Exposer interface {
	Name() string                                    // tailscale, cloudflare, public
	Apply(ctx context.Context, routes []Route) error // the whole set for this adapter
}
