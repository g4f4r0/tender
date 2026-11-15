package security

import (
	"net/http"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the security REST routes on srv.
func (s *Store) Routes(srv *server.Server) {
	srv.Handle("GET /v1/machines/{machine}/security", srv.People(s.get))
	srv.Handle("PUT /v1/machines/{machine}/security", srv.Daemon(s.put))
}

func (s *Store) get(w http.ResponseWriter, r *http.Request) error {
	st, err := s.Get(r.Context(), server.MachineID(r.Context()))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, st)
	return nil
}

// put takes the daemon's report. It answers 204, since the daemon has nothing to read back.
func (s *Store) put(w http.ResponseWriter, r *http.Request) error {
	var in Report
	if err := server.DecodeFromDaemon(w, r, &in); err != nil {
		return err
	}
	if err := s.Put(r.Context(), server.MachineID(r.Context()), in); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
