// Package expose makes the routes of the services Tender deploys reachable: each adapter takes the
// whole set of routes of its type on this machine and makes the machine match it. An adapter is
// idempotent and changes only what it created.
//
// One service's route never fails another service's deploy. Apply sets every route it can and
// returns a *RouteError, joined with errors.Join, for each route it couldn't set; an error that
// isn't a RouteError is one the adapter as a whole hit, such as its program missing. For splits
// such an error into the part that belongs to one service and the rest.
package expose

import (
	"context"
	"errors"
)

// Route is one route of a service, as the daemon gets it from the hub.
type Route struct {
	Service, Container string // the compose project, and the compose service in it
	LocalPort          int    // 127.0.0.1 port the container is published on
	Host               string // cloudflare and public
	HTTPSPort          int    // tailscale
}

// Exposer is one exposure adapter.
type Exposer interface {
	Name() string // tailscale, cloudflare, public
	// Apply makes the machine expose exactly routes, the whole set for this adapter. It sets every
	// route it can, and returns a *RouteError for each one it couldn't.
	Apply(ctx context.Context, routes []Route) error
}

// RouteError is an adapter's failure to set one route. Err's sentence names the route, such as
// "... so the route to web in notes can't have it".
type RouteError struct {
	Service, Container string
	Err                error
}

func (e *RouteError) Error() string { return e.Err.Error() }

func (e *RouteError) Unwrap() error { return e.Err }

// For splits err, what an adapter's Apply returned, into mine, the part that belongs to service,
// and others. Mine is the errors of service's routes and, when uses says service has routes on
// this adapter, the errors that belong to no route. Each is nil when empty.
func For(err error, service string, uses bool) (mine, others error) {
	var m, o []error
	var walk func(error)
	walk = func(err error) {
		var re *RouteError
		switch j, ok := err.(interface{ Unwrap() []error }); {
		case ok:
			for _, e := range j.Unwrap() {
				walk(e)
			}
		case errors.As(err, &re):
			if re.Service == service {
				m = append(m, err)
			} else {
				o = append(o, err)
			}
		case uses:
			m = append(m, err)
		default:
			o = append(o, err)
		}
	}
	if err != nil {
		walk(err)
	}
	return errors.Join(m...), errors.Join(o...)
}
