package machines

import (
	"net/http"
	"time"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the machines REST routes on srv.
func (s *Store) Routes(srv *server.Server) {
	srv.Handle("GET /v1/machines", srv.Reader(s.list))
	srv.Handle("GET /v1/machines/{machine}", srv.Reader(s.get))
	srv.Handle("GET /v1/machines/{machine}/usage", srv.Reader(s.listUsage))
	srv.Handle("POST /v1/machines/{machine}/usage", srv.Agent(s.addUsage))
}

func (s *Store) list(w http.ResponseWriter, r *http.Request) error {
	machines, err := s.List(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Overview]{Data: machines})
	return nil
}

func (s *Store) get(w http.ResponseWriter, r *http.Request) error {
	o, err := s.Get(r.Context(), r.PathValue("machine"))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, o)
	return nil
}

func (s *Store) listUsage(w http.ResponseWriter, r *http.Request) error {
	page, err := server.ParsePage(r)
	if err != nil {
		return err
	}
	since, err := parseTime("since", r.URL.Query().Get("since"))
	if err != nil {
		return err
	}
	until, err := parseTime("until", r.URL.Query().Get("until"))
	if err != nil {
		return err
	}
	usage, err := s.ListUsage(r.Context(), server.MachineID(r.Context()), since, until, page)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.NewList(usage, page, usageCursor))
	return nil
}

func (s *Store) addUsage(w http.ResponseWriter, r *http.Request) error {
	var in UsageInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	u, err := s.AddUsage(r.Context(), server.MachineID(r.Context()), in)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusCreated, u)
	return nil
}

// parseTime reads an optional RFC 3339 time; empty is the zero time.
func parseTime(name, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return t, server.BadRequest("%s must be an RFC 3339 time, such as 2026-11-01T00:00:00Z.", name)
	}
	return t, nil
}
