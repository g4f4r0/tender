package server

import (
	"context"
	"net/http"
	"strings"
)

const loginHeader = "Tailscale-User-Login"

// Machines resolves machine tokens and names. cmd/tender fills it from the machines domain.
// Both methods return a NotFound error for an unknown token or name.
type Machines interface {
	Authenticate(ctx context.Context, token string) (id int64, name string, err error)
	Lookup(ctx context.Context, name string) (id int64, err error)
}

// HandlerFunc is a handler that returns its error; the server turns it into a problem response.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

type (
	machineKey struct{}
	daemonKey  struct{}
)

// MachineID is the id of the machine named by {machine} in the route, set by Daemon, People
// and DaemonOrPeople. It is 0 on routes without {machine}.
func MachineID(ctx context.Context) int64 {
	id, _ := ctx.Value(machineKey{}).(int64)
	return id
}

// IsDaemon reports whether the request came from the machine's own daemon, with its token.
// Only then may a handler show that machine's secrets.
func IsDaemon(ctx context.Context) bool {
	daemon, _ := ctx.Value(daemonKey{}).(bool)
	return daemon
}

// Daemon serves a route that only the machine named by {machine} may call, with its bearer token.
func (s *Server) Daemon(h HandlerFunc) http.Handler { return caller{s.wrap(h, s.daemon), CallerDaemon} }

// People serves a route for people and their tools: no login, the hub is only reachable
// through the owner's own network access control. A route with {machine} answers 404 for an
// unknown machine. Tailscale Serve's Tailscale-User-Login header is logged when present.
func (s *Server) People(h HandlerFunc) http.Handler { return caller{s.wrap(h, s.people), CallerPeople} }

// DaemonOrPeople serves a route that the machine itself or people may call. A request with an
// Authorization header is treated as a daemon; IsDaemon tells the handler which it got.
func (s *Server) DaemonOrPeople(h HandlerFunc) http.Handler {
	return caller{s.wrap(h, func(r *http.Request) (int64, bool, error) {
		if r.Header.Get("Authorization") != "" {
			return s.daemon(r)
		}
		return s.people(r)
	}), CallerBoth}
}

// Who may call a route.
const (
	CallerPeople = "people" // people and their tools
	CallerDaemon = "daemon" // only the machine's daemon, with its token
	CallerBoth   = "both"   // either; the handler tells them apart
)

// caller is a route's handler with who may call it, so Routes can list them.
type caller struct {
	http.Handler
	who string
}

func (s *Server) wrap(h HandlerFunc, identify func(*http.Request) (id int64, daemon bool, err error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, daemon, err := identify(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), machineKey{}, id)
		r = r.WithContext(context.WithValue(ctx, daemonKey{}, daemon))
		// Only daemons retry, so only their requests are made idempotent; people's keys are ignored.
		if key := r.Header.Get("Idempotency-Key"); daemon && key != "" && (r.Method == http.MethodPost || r.Method == http.MethodPatch) {
			s.once(w, r, id, key, h)
			return
		}
		s.run(w, r, h)
	})
}

func (s *Server) run(w http.ResponseWriter, r *http.Request, h HandlerFunc) {
	if err := h(w, r); err != nil {
		s.fail(w, r, err)
	}
}

func (s *Server) daemon(r *http.Request) (int64, bool, error) {
	auth := r.Header.Get("Authorization")
	if len(auth) <= 7 || !strings.EqualFold(auth[:7], "bearer ") {
		return 0, false, &Error{Status: http.StatusUnauthorized, Detail: "Send the machine token as \"Authorization: Bearer <token>\"."}
	}
	id, name, err := s.machines.Authenticate(r.Context(), auth[7:])
	if isNotFound(err) {
		return 0, false, &Error{Status: http.StatusUnauthorized, Detail: "The token is not valid."}
	}
	if err != nil {
		return 0, false, err
	}
	if want := r.PathValue("machine"); want != name {
		if _, err := s.machines.Lookup(r.Context(), want); err != nil {
			return 0, false, err
		}
		return 0, false, &Error{Status: http.StatusForbidden, Detail: "This token belongs to another machine."}
	}
	return id, true, nil
}

func (s *Server) people(r *http.Request) (int64, bool, error) {
	name := r.PathValue("machine")
	if name == "" {
		return 0, false, nil
	}
	id, err := s.machines.Lookup(r.Context(), name)
	return id, false, err
}
