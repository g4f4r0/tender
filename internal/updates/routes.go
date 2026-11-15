package updates

import (
	"net/http"
	"strconv"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the updates REST routes on srv.
func (s *Store) Routes(srv *server.Server) {
	srv.Handle("GET /v1/updates", srv.People(s.overview))
	srv.Handle("GET /v1/updates/settings", srv.People(s.getSettings))
	srv.Handle("PUT /v1/updates/settings", srv.People(s.putSettings))
	srv.Handle("GET /v1/updates/rollouts", srv.People(s.listRollouts))
	srv.Handle("POST /v1/updates/rollouts", srv.People(s.startRollout))
	srv.Handle("GET /v1/updates/rollouts/{id}", srv.People(s.getRollout))
}

func (s *Store) overview(w http.ResponseWriter, r *http.Request) error {
	o, err := s.Overview(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, o)
	return nil
}

func (s *Store) getSettings(w http.ResponseWriter, r *http.Request) error {
	st, err := s.Settings(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, st)
	return nil
}

func (s *Store) putSettings(w http.ResponseWriter, r *http.Request) error {
	var in SettingsInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	st, err := s.SetSettings(r.Context(), in)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, st)
	return nil
}

func (s *Store) listRollouts(w http.ResponseWriter, r *http.Request) error {
	list, err := s.Rollouts(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Rollout]{Data: list})
	return nil
}

func (s *Store) startRollout(w http.ResponseWriter, r *http.Request) error {
	var in RolloutInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	ro, err := s.Start(r.Context(), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/updates/rollouts/"+strconv.FormatInt(ro.ID, 10))
	server.WriteJSON(w, http.StatusAccepted, ro)
	return nil
}

func (s *Store) getRollout(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return server.NotFound("%q is not an id.", r.PathValue("id"))
	}
	ro, err := s.Rollout(r.Context(), id)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, ro)
	return nil
}
