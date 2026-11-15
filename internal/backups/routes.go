package backups

import (
	"net/http"
	"strconv"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the backups REST routes on srv.
func (s *Store) Routes(srv *server.Server) {
	srv.Handle("GET /v1/backups", srv.People(s.summaries))
	srv.Handle("GET /v1/backups/destinations", srv.People(s.listDestinations))
	srv.Handle("POST /v1/backups/destinations", srv.People(s.addDestination))
	srv.Handle("GET /v1/backups/destinations/{destination}", srv.People(s.getDestination))
	srv.Handle("PATCH /v1/backups/destinations/{destination}", srv.People(s.patchDestination))
	srv.Handle("DELETE /v1/backups/destinations/{destination}", srv.People(s.removeDestination))
	srv.Handle("GET /v1/machines/{machine}/backups", srv.People(s.summary))
	srv.Handle("GET /v1/machines/{machine}/backups/settings", srv.DaemonOrPeople(s.settings))
	srv.Handle("PUT /v1/machines/{machine}/backups/settings", srv.People(s.setSettings))
	srv.Handle("DELETE /v1/machines/{machine}/backups/settings", srv.People(s.unsetSettings))
	srv.Handle("GET /v1/machines/{machine}/backups/snapshots", srv.People(s.listSnapshots))
	srv.Handle("PUT /v1/machines/{machine}/backups/snapshots", srv.Daemon(s.replaceSnapshots))
	srv.Handle("GET /v1/machines/{machine}/backups/snapshots/{id}", srv.People(s.getSnapshot))
	srv.Handle("GET /v1/machines/{machine}/backups/history", srv.People(s.listHistory))
	srv.Handle("POST /v1/machines/{machine}/backups/history", srv.Daemon(s.createHistory))
	srv.Handle("GET /v1/machines/{machine}/backups/history/{id}", srv.People(s.getHistory))
	srv.Handle("PATCH /v1/machines/{machine}/backups/history/{id}", srv.Daemon(s.patchHistory))
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

func (s *Store) addDestination(w http.ResponseWriter, r *http.Request) error {
	var in DestinationInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	d, err := s.AddDestination(r.Context(), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/backups/destinations/"+d.Name)
	server.WriteJSON(w, http.StatusCreated, d)
	return nil
}

func (s *Store) patchDestination(w http.ResponseWriter, r *http.Request) error {
	// The password and the type aren't in DestinationPatch; they are read here only to refuse them with a sentence.
	var in struct {
		DestinationPatch
		Password *string `json:"password"`
		Type     *string `json:"type"`
	}
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	switch {
	case in.Password != nil:
		return errPasswordFixed
	case in.Type != nil:
		return errTypeFixed
	}
	d, err := s.PatchDestination(r.Context(), r.PathValue("destination"), in.DestinationPatch)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, d)
	return nil
}

func (s *Store) removeDestination(w http.ResponseWriter, r *http.Request) error {
	if err := s.DeleteDestination(r.Context(), r.PathValue("destination")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// settings serves a machine's settings. Its own daemon, with its token, also gets the credentials
// of the destination; nobody else ever does.
func (s *Store) settings(w http.ResponseWriter, r *http.Request) error {
	id := server.MachineID(r.Context())
	if server.IsDaemon(r.Context()) {
		st, err := s.DaemonSettings(r.Context(), id)
		if err != nil {
			return err
		}
		server.WriteJSON(w, http.StatusOK, st)
		return nil
	}
	st, err := s.Settings(r.Context(), id)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, st)
	return nil
}

func (s *Store) setSettings(w http.ResponseWriter, r *http.Request) error {
	var in SettingsInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	st, err := s.SetSettings(r.Context(), server.MachineID(r.Context()), in.Destination, in.Schedule, in.RetentionDays)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, st)
	return nil
}

func (s *Store) unsetSettings(w http.ResponseWriter, r *http.Request) error {
	if err := s.DeleteSettings(r.Context(), server.MachineID(r.Context())); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
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
	server.WriteJSON(w, http.StatusOK, server.NewList(snaps, page, snapshotCursor))
	return nil
}

func (s *Store) replaceSnapshots(w http.ResponseWriter, r *http.Request) error {
	var in []SnapshotInput
	if err := server.DecodeFromDaemon(w, r, &in); err != nil {
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
	if err := validateFilters(typ, status); err != nil {
		return err
	}
	runs, err := s.ListHistory(r.Context(), server.MachineID(r.Context()), typ, status, page)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.NewList(runs, page, historyCursor))
	return nil
}

func (s *Store) createHistory(w http.ResponseWriter, r *http.Request) error {
	var in HistoryInput
	if err := server.DecodeFromDaemon(w, r, &in); err != nil {
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
	if err := server.DecodeFromDaemon(w, r, &p); err != nil {
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
