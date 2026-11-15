// Package expose makes the routes of the services Tender deploys reachable: each adapter takes the
// whole set of routes of its type on this machine and makes the machine match it. An adapter is
// idempotent and changes only what it created.
package expose

import "context"

// Route is one route of a service, as the daemon gets it from the hub.
type Route struct {
	Service, Container string // the compose project, and the compose service in it
	LocalPort          int    // 127.0.0.1 port the container is published on
	Host               string // cloudflare and public
	HTTPSPort          int    // tailscale
}

// Exposer is one exposure adapter.
type Exposer interface {
	Name() string                                    // tailscale, cloudflare, public
	Apply(ctx context.Context, routes []Route) error // the whole set for this adapter
}
