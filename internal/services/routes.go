package services

import (
	"net/http"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the services REST routes on srv. Actions on services are the actions domain's.
func (s *Store) Routes(srv *server.Server) {
	srv.Handle("GET /v1/services", srv.People(s.listAll))
	srv.Handle("GET /v1/machines/{machine}/services", srv.People(s.listMachine))
	srv.Handle("PUT /v1/machines/{machine}/services", srv.Daemon(s.report))
	srv.Handle("GET /v1/machines/{machine}/services/{service}", srv.People(s.get))
	srv.Handle("PATCH /v1/machines/{machine}/services/{service}", srv.People(s.patch))
}

func (s *Store) listAll(w http.ResponseWriter, r *http.Request) error {
	list, err := s.Machines(r.Context(), 0)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[MachineServices]{Data: list})
	return nil
}

func (s *Store) listMachine(w http.ResponseWriter, r *http.Request) error {
	list, err := s.Machines(r.Context(), server.MachineID(r.Context()))
	if err != nil {
		return err
	}
	if len(list) == 0 { // removed since the route looked it up
		return server.NotFound("Machine %q doesn't exist.", r.PathValue("machine"))
	}
	server.WriteJSON(w, http.StatusOK, list[0])
	return nil
}

func (s *Store) get(w http.ResponseWriter, r *http.Request) error {
	sv, err := s.Get(r.Context(), server.MachineID(r.Context()), r.PathValue("service"))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, sv)
	return nil
}

func (s *Store) patch(w http.ResponseWriter, r *http.Request) error {
	var p ServicePatch
	if err := server.Decode(w, r, &p); err != nil {
		return err
	}
	id, name := server.MachineID(r.Context()), r.PathValue("service")
	var sv Service
	var err error
	if p.AlertsEnabled == nil {
		sv, err = s.Get(r.Context(), id, name)
	} else {
		sv, err = s.SetAlerts(r.Context(), id, name, *p.AlertsEnabled)
	}
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, sv)
	return nil
}

func (s *Store) report(w http.ResponseWriter, r *http.Request) error {
	var in Report
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	if err := s.Apply(r.Context(), server.MachineID(r.Context()), in); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
