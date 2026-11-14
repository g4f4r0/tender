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
	agentKey   struct{}
)

// MachineID is the id of the machine named by {machine} in the route, set by Agent, People
// and AgentOrPeople. It is 0 on routes without {machine}.
func MachineID(ctx context.Context) int64 {
	id, _ := ctx.Value(machineKey{}).(int64)
	return id
}

// IsAgent reports whether the request came from the machine's own agent, with its token.
// Only then may a handler show that machine's secrets.
func IsAgent(ctx context.Context) bool {
	agent, _ := ctx.Value(agentKey{}).(bool)
	return agent
}

// Agent serves a route that only the machine named by {machine} may call, with its bearer token.
func (s *Server) Agent(h HandlerFunc) http.Handler { return s.wrap(h, s.agent) }

// People serves a route for people and their tools: no login, the hub is only reachable
// through the owner's own network access control. A route with {machine} answers 404 for an
// unknown machine. Tailscale Serve's Tailscale-User-Login header is logged when present.
func (s *Server) People(h HandlerFunc) http.Handler { return s.wrap(h, s.people) }

// AgentOrPeople serves a route that the machine itself or people may call. A request with an
// Authorization header is treated as an agent; IsAgent tells the handler which it got.
func (s *Server) AgentOrPeople(h HandlerFunc) http.Handler {
	return s.wrap(h, func(r *http.Request) (int64, bool, error) {
		if r.Header.Get("Authorization") != "" {
			return s.agent(r)
		}
		return s.people(r)
	})
}

func (s *Server) wrap(h HandlerFunc, identify func(*http.Request) (id int64, agent bool, err error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, agent, err := identify(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), machineKey{}, id)
		r = r.WithContext(context.WithValue(ctx, agentKey{}, agent))
		// Only agents retry, so only their requests are made idempotent; people's keys are ignored.
		if key := r.Header.Get("Idempotency-Key"); agent && key != "" && (r.Method == http.MethodPost || r.Method == http.MethodPatch) {
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

func (s *Server) agent(r *http.Request) (int64, bool, error) {
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
