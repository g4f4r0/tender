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

type machineKey struct{}

// MachineID is the id of the machine named by {machine} in the route, set by Agent, Reader
// and AgentOrReader. It is 0 on routes without {machine}.
func MachineID(ctx context.Context) int64 {
	id, _ := ctx.Value(machineKey{}).(int64)
	return id
}

// Agent serves a route that only the machine named by {machine} may call, with its bearer token.
func (s *Server) Agent(h HandlerFunc) http.Handler { return s.wrap(h, s.agent) }

// Reader serves a read-only route for tailnet users, identified by Tailscale Serve's header.
func (s *Server) Reader(h HandlerFunc) http.Handler { return s.wrap(h, s.reader) }

// AgentOrReader serves a route that the machine itself or a reader may call.
// A request with an Authorization header is treated as an agent.
func (s *Server) AgentOrReader(h HandlerFunc) http.Handler {
	return s.wrap(h, func(r *http.Request) (int64, error) {
		if r.Header.Get("Authorization") != "" {
			return s.agent(r)
		}
		return s.reader(r)
	})
}

func (s *Server) wrap(h HandlerFunc, identify func(*http.Request) (int64, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := identify(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), machineKey{}, id))
		// Only agents write, so only their requests are made idempotent; readers' keys are ignored.
		isAgent := r.Header.Get("Authorization") != ""
		if key := r.Header.Get("Idempotency-Key"); isAgent && key != "" && (r.Method == http.MethodPost || r.Method == http.MethodPatch) {
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

func (s *Server) agent(r *http.Request) (int64, error) {
	auth := r.Header.Get("Authorization")
	if len(auth) <= 7 || !strings.EqualFold(auth[:7], "bearer ") {
		return 0, &Error{http.StatusUnauthorized, "Send the machine token as \"Authorization: Bearer <token>\"."}
	}
	id, name, err := s.machines.Authenticate(r.Context(), auth[7:])
	if isNotFound(err) {
		return 0, &Error{http.StatusUnauthorized, "The token is not valid."}
	}
	if err != nil {
		return 0, err
	}
	if want := r.PathValue("machine"); want != name {
		if _, err := s.machines.Lookup(r.Context(), want); err != nil {
			return 0, err
		}
		return 0, &Error{http.StatusForbidden, "This token belongs to another machine."}
	}
	return id, nil
}

func (s *Server) reader(r *http.Request) (int64, error) {
	if r.Header.Get(loginHeader) == "" {
		return 0, &Error{http.StatusUnauthorized, "Readers come through Tailscale Serve, which sets the " + loginHeader + " header."}
	}
	name := r.PathValue("machine")
	if name == "" {
		return 0, nil
	}
	return s.machines.Lookup(r.Context(), name)
}
