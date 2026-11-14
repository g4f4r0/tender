package notifications

import (
	"net/http"
	"strconv"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the notifications REST routes on srv.
func (s *Store) Routes(srv *server.Server) {
	srv.Handle("GET /v1/notifications", srv.Reader(s.list))
	srv.Handle("GET /v1/machines/{machine}/notifications", srv.Reader(s.list))
	srv.Handle("GET /v1/machines/{machine}/notifications/{id}", srv.Reader(s.get))
}

// list serves both list routes: server.MachineID is 0 on /v1/notifications, which means every machine.
func (s *Store) list(w http.ResponseWriter, r *http.Request) error {
	page, err := server.ParsePage(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	items, err := s.List(r.Context(), server.MachineID(r.Context()), q.Get("status"), q.Get("severity"), page)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.NewList(items, page, cursor))
	return nil
}

func (s *Store) get(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return server.NotFound("%q is not an id.", r.PathValue("id"))
	}
	n, err := s.Get(r.Context(), server.MachineID(r.Context()), id)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, n)
	return nil
}
