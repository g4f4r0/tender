package services

import (
	"net/http"
	"strconv"
	"time"

	"github.com/g4f4r0/tender/internal/server"
)

// maxWait is the longest long poll a daemon may ask for, in seconds. The hub's HTTP server has no
// write timeout, and the daemon's client allows more than this.
const maxWait = 30

// Routes registers the services REST routes on srv. The actions live under /service-actions, so
// every name is free for a service, "actions" too. The daemon's long poll ends early when the hub
// shuts down.
func (s *Store) Routes(srv *server.Server) {
	stop := srv.Stopping()
	srv.Handle("GET /v1/services", srv.People(s.listAll))
	srv.Handle("GET /v1/machines/{machine}/services", srv.People(s.listMachine))
	srv.Handle("PUT /v1/machines/{machine}/services", srv.Daemon(s.report))
	srv.Handle("GET /v1/machines/{machine}/service-actions", srv.Daemon(func(w http.ResponseWriter, r *http.Request) error {
		return s.take(w, r, stop)
	}))
	srv.Handle("GET /v1/machines/{machine}/service-actions/{id}", srv.People(s.getAction))
	srv.Handle("PATCH /v1/machines/{machine}/service-actions/{id}", srv.Daemon(s.finish))
	srv.Handle("GET /v1/machines/{machine}/services/{service}", srv.People(s.get))
	srv.Handle("PATCH /v1/machines/{machine}/services/{service}", srv.People(s.patch))
	srv.Handle("POST /v1/machines/{machine}/services/{service}/actions", srv.People(s.createAction))
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

func (s *Store) createAction(w http.ResponseWriter, r *http.Request) error {
	var in ActionInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	a, err := s.CreateAction(r.Context(), server.MachineID(r.Context()), r.PathValue("service"), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/machines/"+r.PathValue("machine")+"/service-actions/"+strconv.FormatInt(a.ID, 10))
	server.WriteJSON(w, http.StatusAccepted, a)
	return nil
}

func (s *Store) getAction(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	a, err := s.Action(r.Context(), server.MachineID(r.Context()), id)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, a)
	return nil
}

// take answers the daemon with its pending actions. With ?wait=N it waits up to N seconds for one.
func (s *Store) take(w http.ResponseWriter, r *http.Request, stop <-chan struct{}) error {
	wait := 0
	if v := r.URL.Query().Get("wait"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxWait {
			return server.BadRequest("wait must be a whole number of seconds from 0 to %d.", maxWait)
		}
		wait = n
	}
	list, err := s.Wait(r.Context(), server.MachineID(r.Context()), time.Duration(wait)*time.Second, stop)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Action]{Data: list})
	return nil
}

// finish takes the daemon's report of how an action ended. It answers 204: the body would repeat
// the logs, and the hub keeps a daemon's answers for a day to replay them on a retry.
func (s *Store) finish(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var p ActionPatch
	if err := server.Decode(w, r, &p); err != nil {
		return err
	}
	if err := s.Finish(r.Context(), server.MachineID(r.Context()), id, p); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
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
