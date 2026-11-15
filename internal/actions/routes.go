package actions

import (
	"net/http"
	"strconv"
	"time"

	"github.com/g4f4r0/tender/internal/server"
)

// maxWait is the longest long poll a daemon may ask for, in seconds. The hub's HTTP server has no
// write timeout, and the daemon's client allows more than this.
const maxWait = 30

// Routes registers the actions REST routes on srv. They aren't under /services, so every name is
// free for a service, "actions" too. The daemon's long poll ends early when the hub shuts down.
func (s *Store) Routes(srv *server.Server) {
	stop := srv.Stopping()
	srv.Handle("POST /v1/machines/{machine}/actions", srv.People(s.create))
	srv.Handle("GET /v1/machines/{machine}/actions", srv.DaemonOrPeople(func(w http.ResponseWriter, r *http.Request) error {
		if server.IsDaemon(r.Context()) {
			return s.take(w, r, stop)
		}
		return s.list(w, r)
	}))
	srv.Handle("GET /v1/machines/{machine}/actions/{id}", srv.People(s.getOne))
	srv.Handle("PATCH /v1/machines/{machine}/actions/{id}", srv.Daemon(s.finish))
	srv.Handle("POST /v1/backups/destinations/{destination}/test", srv.People(s.test))
}

// TestInput is the body of POST /v1/backups/destinations/{destination}/test.
type TestInput struct {
	Machine string `json:"machine"` // the machine whose daemon tries the destination
}

// test queues a test of a destination on the machine the body names: its daemon opens that
// machine's repository there, or makes it.
func (s *Store) test(w http.ResponseWriter, r *http.Request) error {
	var in TestInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	if in.Machine == "" {
		return server.Invalid("machine is required: the machine whose daemon tries the destination.")
	}
	id, err := s.machineID(r.Context(), in.Machine)
	if err != nil {
		return err
	}
	destination := r.PathValue("destination")
	a, err := s.Create(r.Context(), id, Input{Type: TypeTest, Destination: &destination})
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/machines/"+in.Machine+"/actions/"+strconv.FormatInt(a.ID, 10))
	server.WriteJSON(w, http.StatusAccepted, a)
	return nil
}

func (s *Store) create(w http.ResponseWriter, r *http.Request) error {
	var in Input
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	a, err := s.Create(r.Context(), server.MachineID(r.Context()), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/machines/"+r.PathValue("machine")+"/actions/"+strconv.FormatInt(a.ID, 10))
	server.WriteJSON(w, http.StatusAccepted, a)
	return nil
}

func (s *Store) list(w http.ResponseWriter, r *http.Request) error {
	if r.URL.Query().Has("wait") {
		return server.BadRequest("wait is only for the machine's daemon, with its token.")
	}
	list, err := s.List(r.Context(), server.MachineID(r.Context()))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Summary]{Data: list})
	return nil
}

func (s *Store) getOne(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	a, err := s.Get(r.Context(), server.MachineID(r.Context()), id)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, a)
	return nil
}

// take answers the daemon with its oldest pending action, or none. With ?wait=N it waits up to N
// seconds for one.
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
	server.WriteJSON(w, http.StatusOK, server.List[Handout]{Data: list})
	return nil
}

// finish takes the daemon's claim of an action, status in_progress, or its report of how the action
// ended. It answers 204: the body would repeat the logs, and the hub keeps a daemon's answers for a
// day to replay them on a retry.
func (s *Store) finish(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var p Patch
	if err := server.DecodeFromDaemon(w, r, &p); err != nil {
		return err
	}
	if p.Status == StatusInProgress {
		if p.Output != nil {
			return server.Invalid("output is only for completed or failed.")
		}
		err = s.Claim(r.Context(), server.MachineID(r.Context()), id)
	} else {
		err = s.Finish(r.Context(), server.MachineID(r.Context()), id, p)
	}
	if err != nil {
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
