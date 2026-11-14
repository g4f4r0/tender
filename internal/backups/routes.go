package backups

import (
	"net/http"
	"strconv"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the backups REST routes on srv.
func (s *Store) Routes(srv *server.Server) {
	srv.Handle("GET /v1/backups", srv.Reader(s.summaries))
	srv.Handle("GET /v1/backups/destinations", srv.Reader(s.listDestinations))
	srv.Handle("GET /v1/backups/destinations/{destination}", srv.Reader(s.getDestination))
	srv.Handle("GET /v1/machines/{machine}/backups", srv.Reader(s.summary))
	srv.Handle("GET /v1/machines/{machine}/backups/settings", srv.AgentOrReader(s.settings))
	srv.Handle("GET /v1/machines/{machine}/backups/snapshots", srv.Reader(s.listSnapshots))
	srv.Handle("PUT /v1/machines/{machine}/backups/snapshots", srv.Agent(s.replaceSnapshots))
	srv.Handle("GET /v1/machines/{machine}/backups/snapshots/{id}", srv.Reader(s.getSnapshot))
	srv.Handle("GET /v1/machines/{machine}/backups/history", srv.Reader(s.listHistory))
	srv.Handle("POST /v1/machines/{machine}/backups/history", srv.Agent(s.createHistory))
	srv.Handle("GET /v1/machines/{machine}/backups/history/{id}", srv.Reader(s.getHistory))
	srv.Handle("PATCH /v1/machines/{machine}/backups/history/{id}", srv.Agent(s.patchHistory))
}

func (s *Store) summaries(w http.ResponseWriter, r *http.Request) error {
	sums, err := s.Summaries(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Summary]{Data: sums})
	return nil
}

func (s *Store) summary(w http.ResponseWriter, r *http.Request) error {
	sum, err := s.Summary(r.Context(), server.MachineID(r.Context()), r.PathValue("machine"))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, sum)
	return nil
}

func (s *Store) listDestinations(w http.ResponseWriter, r *http.Request) error {
	ds, err := s.Destinations(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Destination]{Data: ds})
	return nil
}

func (s *Store) getDestination(w http.ResponseWriter, r *http.Request) error {
	d, err := s.Destination(r.Context(), r.PathValue("destination"))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, d)
	return nil
}

func (s *Store) settings(w http.ResponseWriter, r *http.Request) error {
	st, err := s.Settings(r.Context(), server.MachineID(r.Context()))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, st)
	return nil
}

func (s *Store) listSnapshots(w http.ResponseWriter, r *http.Request) error {
	page, err := server.ParsePage(r)
	if err != nil {
		return err
	}
	snaps, err := s.ListSnapshots(r.Context(), server.MachineID(r.Context()), page)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.NewList(snaps, page, func(sn Snapshot) server.Cursor {
		return server.Cursor{CreatedAt: sn.CreatedAt.Unix(), ID: sn.ID}
	}))
	return nil
}

func (s *Store) replaceSnapshots(w http.ResponseWriter, r *http.Request) error {
	var in []SnapshotInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	if in == nil {
		return server.Invalid("The body must be a JSON array of snapshots.")
	}
	snaps, err := s.ReplaceSnapshots(r.Context(), server.MachineID(r.Context()), in)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Snapshot]{Data: snaps})
	return nil
}

func (s *Store) getSnapshot(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	sn, err := s.Snapshot(r.Context(), server.MachineID(r.Context()), id)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, sn)
	return nil
}

func (s *Store) listHistory(w http.ResponseWriter, r *http.Request) error {
	page, err := server.ParsePage(r)
	if err != nil {
		return err
	}
	typ, status := r.URL.Query().Get("type"), r.URL.Query().Get("status")
	if typ != "" && !validType(typ) {
		return server.Invalid("type must be backup, cleanup or check.")
	}
	if status != "" && !validStatus(status) {
		return server.Invalid("status must be pending, in_progress, completed or failed.")
	}
	runs, err := s.ListHistory(r.Context(), server.MachineID(r.Context()), typ, status, page)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.NewList(runs, page, func(h History) server.Cursor {
		return server.Cursor{CreatedAt: h.CreatedAt.Unix(), ID: h.ID}
	}))
	return nil
}

func (s *Store) createHistory(w http.ResponseWriter, r *http.Request) error {
	var in HistoryInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	h, err := s.CreateHistory(r.Context(), server.MachineID(r.Context()), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", r.URL.Path+"/"+strconv.FormatInt(h.ID, 10))
	server.WriteJSON(w, http.StatusCreated, h)
	return nil
}

func (s *Store) getHistory(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	h, err := s.History(r.Context(), server.MachineID(r.Context()), id)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, h)
	return nil
}

func (s *Store) patchHistory(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var p HistoryPatch
	if err := server.Decode(w, r, &p); err != nil {
		return err
	}
	h, err := s.PatchHistory(r.Context(), server.MachineID(r.Context()), id, p)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, h)
	return nil
}

// pathID reads {id}. A value that isn't a number can't name a row, so it is a 404.
func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, server.NotFound("%q is not an id.", r.PathValue("id"))
	}
	return id, nil
}
