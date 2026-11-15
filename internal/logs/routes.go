package logs

import (
	"net/http"
	"net/url"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the logs REST routes on srv.
func (s *Store) Routes(srv *server.Server) {
	srv.Handle("GET /v1/logs/sinks", srv.People(s.listSinks))
	srv.Handle("POST /v1/logs/sinks", srv.People(s.addSink))
	srv.Handle("GET /v1/logs/sinks/{sink}", srv.People(s.getSink))
	srv.Handle("PATCH /v1/logs/sinks/{sink}", srv.People(s.patchSink))
	srv.Handle("DELETE /v1/logs/sinks/{sink}", srv.People(s.removeSink))
	srv.Handle("GET /v1/machines/{machine}/logs/sinks", srv.Daemon(s.daemonSinks))
	srv.Handle("PUT /v1/machines/{machine}/logs/status", srv.Daemon(s.setStatus))
}

func (s *Store) listSinks(w http.ResponseWriter, r *http.Request) error {
	list, err := s.Sinks(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Sink]{Data: list})
	return nil
}

func (s *Store) getSink(w http.ResponseWriter, r *http.Request) error {
	d, err := s.Sink(r.Context(), r.PathValue("sink"))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, d)
	return nil
}

func (s *Store) addSink(w http.ResponseWriter, r *http.Request) error {
	var in SinkInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	d, err := s.AddSink(r.Context(), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/logs/sinks/"+url.PathEscape(d.Name))
	server.WriteJSON(w, http.StatusCreated, d)
	return nil
}

func (s *Store) patchSink(w http.ResponseWriter, r *http.Request) error {
	var patch map[string]any
	if err := server.Decode(w, r, &patch); err != nil {
		return err
	}
	if patch == nil {
		return server.Invalid("The request body must be a JSON object.")
	}
	d, err := s.PatchSink(r.Context(), r.PathValue("sink"), patch)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, d)
	return nil
}

func (s *Store) removeSink(w http.ResponseWriter, r *http.Request) error {
	if err := s.DeleteSink(r.Context(), r.PathValue("sink")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// daemonSinks serves a machine's daemon the sinks it sends to, with their secrets. Only that
// daemon, with its token, ever gets them.
func (s *Store) daemonSinks(w http.ResponseWriter, r *http.Request) error {
	list, err := s.DaemonSinks(r.Context(), r.PathValue("machine"))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[DaemonSink]{Data: list})
	return nil
}

func (s *Store) setStatus(w http.ResponseWriter, r *http.Request) error {
	var in StatusReport
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	if err := s.SetStatus(r.Context(), server.MachineID(r.Context()), in); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
