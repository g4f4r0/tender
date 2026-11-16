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

// Tokens checks people's API tokens. cmd/tender fills it from the tokens domain. Authenticate
// returns the token's name, or a NotFound error for a token that isn't one.
type Tokens interface {
	Authenticate(ctx context.Context, token string) (name string, err error)
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

// People serves a route for people and their tools, with an API token as the bearer token once
// RequireTokens is set. A machine token is refused. A route with {machine} answers 404 for an
// unknown machine. Tailscale Serve's Tailscale-User-Login header is logged when present.
func (s *Server) People(h HandlerFunc) http.Handler {
	return caller{s.wrap(h, func(r *http.Request) (int64, bool, error) {
		if err := s.person(r); err != nil {
			return 0, false, err
		}
		return s.people(r)
	}), CallerPeople}
}

// DaemonOrPeople serves a route that the machine itself or people may call. It looks the bearer
// token up as a machine token, then as an API token, so neither ever passes as the other;
// IsDaemon tells the handler which it got.
func (s *Server) DaemonOrPeople(h HandlerFunc) http.Handler {
	return caller{s.wrap(h, func(r *http.Request) (int64, bool, error) {
		if token, ok := bearer(r); ok {
			id, name, err := s.machines.Authenticate(r.Context(), token)
			switch {
			case err == nil:
				return s.ownMachine(r, id, name)
			case !isNotFound(err):
				return 0, false, err
			case s.tokens == nil:
				return 0, false, &Error{Status: http.StatusUnauthorized, Detail: "The token is not valid."}
			}
		}
		if err := s.person(r); err != nil {
			return 0, false, err
		}
		return s.people(r)
	}), CallerBoth}
}

// RequireTokens makes every people route need an API token that t knows. The hub always sets it;
// a server without it, in one domain's tests, serves people routes to anyone.
func (s *Server) RequireTokens(t Tokens) { s.tokens = t }

// noToken is the 401 for a people request without a valid API token. It says how to get one.
const noToken = "Send an API token as \"Authorization: Bearer <token>\"; the tender command sends TENDER_TOKEN. " +
	"Make the first one on the hub machine with: sudo tender tokens add NAME"

// person is an error unless the request carries a valid API token, once RequireTokens is set.
func (s *Server) person(r *http.Request) error {
	if s.tokens == nil {
		return nil
	}
	token, ok := bearer(r)
	if !ok {
		return &Error{Status: http.StatusUnauthorized, Detail: noToken}
	}
	_, err := s.tokens.Authenticate(r.Context(), token)
	if isNotFound(err) {
		return &Error{Status: http.StatusUnauthorized, Detail: "This is not an API token of this hub: it was deleted or mistyped, or it is a machine token. " + noToken}
	}
	return err
}

// bearer returns the request's bearer token, if it has one.
func bearer(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if len(auth) <= 7 || !strings.EqualFold(auth[:7], "bearer ") {
		return "", false
	}
	return auth[7:], true
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
	token, ok := bearer(r)
	if !ok {
		return 0, false, &Error{Status: http.StatusUnauthorized, Detail: "Send the machine token as \"Authorization: Bearer <token>\"."}
	}
	id, name, err := s.machines.Authenticate(r.Context(), token)
	if isNotFound(err) {
		return 0, false, &Error{Status: http.StatusUnauthorized, Detail: "The token is not valid."}
	}
	if err != nil {
		return 0, false, err
	}
	return s.ownMachine(r, id, name)
}

// ownMachine lets the daemon of machine id, called name, through when the route names it.
func (s *Server) ownMachine(r *http.Request, id int64, name string) (int64, bool, error) {
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
