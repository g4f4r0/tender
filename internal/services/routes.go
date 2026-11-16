package services

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services/deploy"
)

// Routes registers the services REST routes on srv. Their actions go through the actions domain,
// which also answers POST /v1/machines/{machine}/actions for them.
func (s *Store) Routes(srv *server.Server) {
	srv.Handle("GET /v1/services", srv.People(s.listAll))
	srv.Handle("GET /v1/machines/{machine}/services", srv.People(s.listMachine))
	srv.Handle("PUT /v1/machines/{machine}/services", srv.Daemon(s.report))
	srv.Handle("POST /v1/machines/{machine}/services", srv.People(s.create))
	srv.Handle("GET /v1/machines/{machine}/services/{service}", srv.People(s.get))
	srv.Handle("PATCH /v1/machines/{machine}/services/{service}", srv.People(s.patch))
	srv.Handle("DELETE /v1/machines/{machine}/services/{service}", srv.People(s.remove))
	srv.Handle("POST /v1/machines/{machine}/services/{service}/deploy", srv.People(s.redeploy))
	srv.Handle("POST /v1/machines/{machine}/services/{service}/start", srv.People(s.act(actStart)))
	srv.Handle("POST /v1/machines/{machine}/services/{service}/stop", srv.People(s.act(actStop)))
	srv.Handle("POST /v1/machines/{machine}/services/{service}/restart", srv.People(s.act(actRestart)))
	srv.Handle("GET /v1/machines/{machine}/exposures", srv.Daemon(s.exposures))
	srv.Handle("PUT /v1/machines/{machine}/exposures/status", srv.Daemon(s.exposureStatus))
	srv.Handle("GET /v1/registries", srv.People(s.listRegistries))
	srv.Handle("POST /v1/registries", srv.People(s.addRegistry))
	srv.Handle("PATCH /v1/registries/{registry}", srv.People(s.patchRegistry))
	srv.Handle("DELETE /v1/registries/{registry}", srv.People(s.removeRegistry))
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

func (s *Store) report(w http.ResponseWriter, r *http.Request) error {
	var in Report
	if err := server.DecodeFromDaemon(w, r, &in); err != nil {
		return err
	}
	if err := s.Apply(r.Context(), server.MachineID(r.Context()), in); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// accepted answers 202 with the service and its action, and the action's Location.
func accepted(w http.ResponseWriter, r *http.Request, q Queued) {
	if q.actionID != 0 {
		w.Header().Set("Location", "/v1/machines/"+url.PathEscape(r.PathValue("machine"))+"/actions/"+strconv.FormatInt(q.actionID, 10))
	}
	server.WriteJSON(w, http.StatusAccepted, q)
}

func (s *Store) create(w http.ResponseWriter, r *http.Request) error {
	var in Input
	if err := server.DecodeUpTo(w, r, &in, deploy.MaxBodyBytes); err != nil {
		return err
	}
	q, err := s.Create(r.Context(), server.MachineID(r.Context()), in)
	if err != nil {
		return err
	}
	accepted(w, r, q)
	return nil
}

// parsePatch turns a merge-patch body of a service into a Change: compose replaces the file, files and
// env merge into the stored ones with null removing a key (and files or env null removing all),
// and routes replaces every route.
func parsePatch(raw rawPatch) (Change, error) {
	var c Change
	isNull := func(m json.RawMessage) bool { return string(m) == "null" }
	switch {
	case raw.Compose == nil:
	case isNull(raw.Compose):
		return c, server.Invalid("compose can't be null: a managed service needs its compose file.")
	default:
		if err := json.Unmarshal(raw.Compose, &c.Compose); err != nil {
			return c, server.Invalid("compose must be the text of the compose file.")
		}
	}
	for _, f := range []struct {
		raw   json.RawMessage
		into  *map[string]*string
		clear *bool
		name  string
	}{{raw.Files, &c.Files, &c.ClearFiles, "files"}, {raw.Env, &c.Env, &c.ClearEnv, "env"}} {
		switch {
		case f.raw == nil:
		case isNull(f.raw):
			*f.clear = true
		default:
			if err := json.Unmarshal(f.raw, f.into); err != nil {
				return c, server.Invalid("%s must be an object of strings, with null to remove a key.", f.name)
			}
		}
	}
	switch {
	case raw.Routes == nil:
	case isNull(raw.Routes):
		c.Routes = &[]RouteInput{}
	default:
		var routes []RouteInput
		dec := json.NewDecoder(bytes.NewReader(raw.Routes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&routes); err != nil {
			return c, server.Invalid("routes must be a list of routes, each with container, port, expose and maybe host, https_port or access: %v.", err)
		}
		if routes == nil {
			routes = []RouteInput{}
		}
		c.Routes = &routes
	}
	return c, nil
}

func (s *Store) patch(w http.ResponseWriter, r *http.Request) error {
	var raw rawPatch
	if err := server.DecodeUpTo(w, r, &raw, deploy.MaxBodyBytes); err != nil {
		return err
	}
	c, err := parsePatch(raw)
	if err != nil {
		return err
	}
	id, name := server.MachineID(r.Context()), r.PathValue("service")
	sv, err := s.Get(r.Context(), id, name)
	if err != nil {
		return err
	}
	if !c.empty() {
		// The name's rules come first, so tender, the project of Tender's own containers, says why
		// it is refused rather than that Tender didn't deploy it.
		if err := deploy.CheckName(name); err != nil {
			return err
		}
		if !sv.Managed {
			return unmanaged(name, "change")
		}
	}
	if raw.AlertsEnabled != nil {
		if sv, err = s.SetAlerts(r.Context(), id, name, *raw.AlertsEnabled); err != nil {
			return err
		}
	}
	if c.empty() {
		server.WriteJSON(w, http.StatusOK, sv)
		return nil
	}
	q, err := s.Update(r.Context(), id, name, c)
	if err != nil {
		return err
	}
	accepted(w, r, q)
	return nil
}

func (s *Store) redeploy(w http.ResponseWriter, r *http.Request) error {
	q, err := s.Redeploy(r.Context(), server.MachineID(r.Context()), r.PathValue("service"))
	if err != nil {
		return err
	}
	accepted(w, r, q)
	return nil
}

// act answers POST .../start, stop and restart, whose body may name one container.
func (s *Store) act(typ string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		var in actInput
		if r.ContentLength != 0 {
			if err := server.Decode(w, r, &in); err != nil {
				return err
			}
		}
		container := ""
		if in.Container != nil {
			if *in.Container == "" {
				return server.Invalid("container can't be empty. Leave it out to %s every container of the service.", typ)
			}
			container = *in.Container
		}
		q, err := s.Act(r.Context(), server.MachineID(r.Context()), r.PathValue("service"), typ, container)
		if err != nil {
			return err
		}
		accepted(w, r, q)
		return nil
	}
}

func (s *Store) remove(w http.ResponseWriter, r *http.Request) error {
	deleteData := false
	switch v := r.URL.Query().Get("delete_data"); v {
	case "", "false":
	case "true":
		deleteData = true
	default:
		return server.BadRequest("delete_data must be true or false, got %q.", v)
	}
	q, err := s.Remove(r.Context(), server.MachineID(r.Context()), r.PathValue("service"), deleteData)
	if err != nil {
		return err
	}
	accepted(w, r, q)
	return nil
}

// exposures serves the machine's daemon its whole set of routes, and its Cloudflare tunnel token.
// Only that daemon, with its token, ever gets the tunnel token.
func (s *Store) exposures(w http.ResponseWriter, r *http.Request) error {
	ex, err := s.Exposures(r.Context(), server.MachineID(r.Context()))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, ex)
	return nil
}

func (s *Store) exposureStatus(w http.ResponseWriter, r *http.Request) error {
	var in deploy.ExposureStatus
	if err := server.DecodeFromDaemon(w, r, &in); err != nil {
		return err
	}
	if err := s.SetExposureStatus(r.Context(), server.MachineID(r.Context()), in); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Store) listRegistries(w http.ResponseWriter, r *http.Request) error {
	list, err := s.Registries(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Registry]{Data: list})
	return nil
}

func (s *Store) addRegistry(w http.ResponseWriter, r *http.Request) error {
	var in RegistryInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	g, err := s.AddRegistry(r.Context(), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/registries/"+url.PathEscape(g.Name))
	server.WriteJSON(w, http.StatusCreated, g)
	return nil
}

func (s *Store) patchRegistry(w http.ResponseWriter, r *http.Request) error {
	var p RegistryPatch
	if err := server.Decode(w, r, &p); err != nil {
		return err
	}
	g, err := s.UpdateRegistry(r.Context(), r.PathValue("registry"), p)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, g)
	return nil
}

func (s *Store) removeRegistry(w http.ResponseWriter, r *http.Request) error {
	if err := s.DeleteRegistry(r.Context(), r.PathValue("registry")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
