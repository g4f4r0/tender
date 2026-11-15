package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services"
)

// sentRequest is one request the fake hub got.
type sentRequest struct {
	Method, Path, Query, Auth string
	Body                      string
}

// fakeHub implements the routes the tender command uses, as the v0.2 design lists them, with
// the same problem+json errors. The people routes are open: it records any Authorization header.
type fakeHub struct {
	t   *testing.T
	srv *httptest.Server

	mu           sync.Mutex
	requests     []sentRequest
	now          time.Time
	nextID       int64
	machines     map[string]*fakeMachine
	destinations []backups.Destination
	secrets      map[string]backups.DestinationInput // by destination name; never returned
	settings     map[string]backups.Settings
	alerts       []notifications.Notification
	history      map[string]*backups.History   // last backup by machine
	pageSize     int                           // notifications per page, to exercise next_cursor
	services     map[string][]services.Service // by machine
	docker       map[string]string             // what each machine's daemon said about Docker
	actions      map[int64]*actions.Action
	daemon       func(*actions.Action) // finishes an action when it is first read; nil leaves it pending
	olderHub     bool                  // a v0.2 hub, which has no /v1/services
}

type fakeMachine struct {
	machines.Machine
	token string
	usage *machines.Usage
	seen  *time.Time
}

func newFakeHub(t *testing.T) *fakeHub {
	h := &fakeHub{
		t: t, now: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC), nextID: 1, pageSize: 500,
		machines: map[string]*fakeMachine{}, secrets: map[string]backups.DestinationInput{},
		settings: map[string]backups.Settings{}, history: map[string]*backups.History{},
		services: map[string][]services.Service{}, docker: map[string]string{}, actions: map[int64]*actions.Action{}, daemon: completeAction,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/machines", h.addMachine)
	mux.HandleFunc("GET /v1/machines", h.listMachines)
	mux.HandleFunc("POST /v1/machines/{machine}/token", h.rotateToken)
	mux.HandleFunc("DELETE /v1/machines/{machine}", h.removeMachine)
	mux.HandleFunc("GET /v1/machines/{machine}/backups", h.machineBackups)
	mux.HandleFunc("PUT /v1/machines/{machine}/backups/settings", h.putSettings)
	mux.HandleFunc("DELETE /v1/machines/{machine}/backups/settings", h.deleteSettings)
	mux.HandleFunc("GET /v1/backups", h.listBackups)
	mux.HandleFunc("POST /v1/backups/destinations", h.addDestination)
	mux.HandleFunc("GET /v1/backups/destinations", h.listDestinations)
	mux.HandleFunc("DELETE /v1/backups/destinations/{destination}", h.removeDestination)
	mux.HandleFunc("GET /v1/notifications", h.listAlerts)
	mux.HandleFunc("GET /v1/services", h.listServices)
	mux.HandleFunc("GET /v1/machines/{machine}/services", h.listServices)
	mux.HandleFunc("PATCH /v1/machines/{machine}/services/{service}", h.patchService)
	mux.HandleFunc("POST /v1/machines/{machine}/actions", h.createAction)
	mux.HandleFunc("GET /v1/machines/{machine}/actions", h.listActions)
	mux.HandleFunc("GET /v1/machines/{machine}/actions/{id}", h.getAction)
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.mu.Lock()
		defer h.mu.Unlock()
		h.requests = append(h.requests, sentRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(body)})
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) url() string { return h.srv.URL }

// sent returns the requests so far and forgets them.
func (h *fakeHub) sent() []sentRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.requests
	h.requests = nil
	return r
}

func (h *fakeHub) id() int64 { h.nextID++; return h.nextID - 1 }

func problem(w http.ResponseWriter, status int, format string, a ...any) {
	server.WriteProblem(w, status, http.StatusText(status), fmt.Sprintf(format, a...))
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		problem(w, http.StatusBadRequest, "The body is not valid JSON.")
		return false
	}
	return true
}

func (h *fakeHub) machine(w http.ResponseWriter, r *http.Request) *fakeMachine {
	m := h.machines[r.PathValue("machine")]
	if m == nil {
		problem(w, http.StatusNotFound, "Machine %q doesn't exist. List the machines with \"tender machines list\".", r.PathValue("machine"))
	}
	return m
}

func (h *fakeHub) addMachine(w http.ResponseWriter, r *http.Request) {
	var in machines.MachineInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == "" || strings.ContainsAny(in.Name, "/ ") {
		problem(w, http.StatusUnprocessableEntity, "The machine name must be one word without slashes. Use something like web-1.")
		return
	}
	if h.machines[in.Name] != nil {
		problem(w, http.StatusConflict, "Machine %q already exists. Choose another name.", in.Name)
		return
	}
	m := &fakeMachine{Machine: machines.Machine{ID: h.id(), Name: in.Name, CreatedAt: h.now, UpdatedAt: h.now}, token: "token-" + in.Name + "-1"}
	h.machines[in.Name] = m
	server.WriteJSON(w, http.StatusCreated, machines.MachineCreated{Machine: m.Machine, Token: m.token})
}

func (h *fakeHub) listMachines(w http.ResponseWriter, _ *http.Request) {
	list := []machines.Overview{}
	for _, m := range h.sortedMachines() {
		list = append(list, machines.Overview{Machine: m.Machine, LastSeen: m.seen, Usage: m.usage})
	}
	server.WriteJSON(w, http.StatusOK, server.List[machines.Overview]{Data: list})
}

func (h *fakeHub) sortedMachines() []*fakeMachine {
	var list []*fakeMachine
	for _, m := range h.machines {
		list = append(list, m)
	}
	for i := range list { // insertion sort by id keeps the output stable
		for j := i; j > 0 && list[j].ID < list[j-1].ID; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	return list
}

func (h *fakeHub) rotateToken(w http.ResponseWriter, r *http.Request) {
	m := h.machine(w, r)
	if m == nil {
		return
	}
	m.token += "-rotated"
	server.WriteJSON(w, http.StatusOK, machines.Token{Token: m.token})
}

func (h *fakeHub) removeMachine(w http.ResponseWriter, r *http.Request) {
	m := h.machine(w, r)
	if m == nil {
		return
	}
	delete(h.machines, m.Name)
	delete(h.settings, m.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *fakeHub) summary(name string) backups.Summary {
	sum := backups.Summary{Machine: name, LastBackup: h.history[name]}
	if st, ok := h.settings[name]; ok {
		sum.Settings = &st
	}
	if sum.LastBackup != nil {
		sum.SnapshotCount = 3
	}
	return sum
}

func (h *fakeHub) listBackups(w http.ResponseWriter, _ *http.Request) {
	list := []backups.Summary{}
	for _, m := range h.sortedMachines() {
		list = append(list, h.summary(m.Name))
	}
	server.WriteJSON(w, http.StatusOK, server.List[backups.Summary]{Data: list})
}

func (h *fakeHub) machineBackups(w http.ResponseWriter, r *http.Request) {
	if m := h.machine(w, r); m != nil {
		server.WriteJSON(w, http.StatusOK, h.summary(m.Name))
	}
}

func (h *fakeHub) putSettings(w http.ResponseWriter, r *http.Request) {
	m := h.machine(w, r)
	if m == nil {
		return
	}
	var in backups.SettingsInput
	if !decode(w, r, &in) {
		return
	}
	var dest *backups.Destination
	for i := range h.destinations {
		if h.destinations[i].Name == in.Destination {
			dest = &h.destinations[i]
		}
	}
	switch {
	case dest == nil:
		problem(w, http.StatusUnprocessableEntity, "Destination %q doesn't exist. Add it with \"tender destinations add\".", in.Destination)
		return
	case len(strings.Fields(in.Schedule)) != 5:
		problem(w, http.StatusUnprocessableEntity, "The schedule must have five cron fields, like \"30 3 * * *\".")
		return
	case in.RetentionDays < 1:
		problem(w, http.StatusUnprocessableEntity, "retention_days must be 1 or more.")
		return
	}
	st := backups.Settings{Destination: *dest, Schedule: in.Schedule, RetentionDays: in.RetentionDays, CreatedAt: h.now, UpdatedAt: h.now}
	h.settings[m.Name] = st
	server.WriteJSON(w, http.StatusOK, st) // no credentials on a people route
}

func (h *fakeHub) deleteSettings(w http.ResponseWriter, r *http.Request) {
	m := h.machine(w, r)
	if m == nil {
		return
	}
	delete(h.settings, m.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *fakeHub) addDestination(w http.ResponseWriter, r *http.Request) {
	var in backups.DestinationInput
	if !decode(w, r, &in) {
		return
	}
	for _, d := range h.destinations {
		if d.Name == in.Name {
			problem(w, http.StatusConflict, "Destination %q already exists. Choose another name.", in.Name)
			return
		}
	}
	if in.AccessKeyID == "" || in.SecretAccessKey == "" || in.Endpoint == "" || in.Bucket == "" {
		problem(w, http.StatusUnprocessableEntity, "A destination needs an endpoint, a bucket, an access key ID and a secret access key.")
		return
	}
	created := backups.DestinationCreated{}
	if in.Password == "" {
		in.Password = "generated-repository-password"
		created.Password = in.Password
	}
	d := backups.Destination{ID: h.id(), Name: in.Name, Type: in.Type, Endpoint: in.Endpoint, Bucket: in.Bucket, Path: in.Path, CreatedAt: h.now, UpdatedAt: h.now}
	h.destinations = append(h.destinations, d)
	h.secrets[in.Name] = in
	created.Destination = d
	server.WriteJSON(w, http.StatusCreated, created)
}

func (h *fakeHub) listDestinations(w http.ResponseWriter, _ *http.Request) {
	list := h.destinations
	if list == nil {
		list = []backups.Destination{}
	}
	server.WriteJSON(w, http.StatusOK, server.List[backups.Destination]{Data: list})
}

func (h *fakeHub) removeDestination(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("destination")
	for machine, st := range h.settings {
		if st.Destination.Name == name {
			problem(w, http.StatusConflict, "Destination %q is used by %s. Run \"tender backups unset %s\" first.", name, machine, machine)
			return
		}
	}
	for i, d := range h.destinations {
		if d.Name == name {
			h.destinations = append(h.destinations[:i], h.destinations[i+1:]...)
			delete(h.secrets, name)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	problem(w, http.StatusNotFound, "Destination %q doesn't exist.", name)
}

func (h *fakeHub) listAlerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var match []notifications.Notification
	for _, a := range h.alerts {
		if q.Get("status") == "" || a.Status == q.Get("status") {
			match = append(match, a)
		}
	}
	offset, _ := strconv.Atoi(q.Get("cursor"))
	end := min(offset+h.pageSize, len(match))
	page := server.List[notifications.Notification]{Data: []notifications.Notification{}}
	if offset < len(match) {
		page.Data = match[offset:end]
	}
	if end < len(match) {
		page.NextCursor = strconv.Itoa(end)
	}
	server.WriteJSON(w, http.StatusOK, page)
}

// answers is a session's prompt: it hands out the answers in order and records the labels asked.
// With no answers left it behaves like a machine with no terminal.
type answers struct {
	left   []string
	labels []string
}

func (a *answers) ask(label string) (string, error) {
	a.labels = append(a.labels, label)
	if len(a.left) == 0 {
		return "", errNoTerminal
	}
	v := a.left[0]
	a.left = a.left[1:]
	return v, nil
}

// runCommand runs "tender ARGS..." for a hub command against the fake hub, with the hub address
// in TENDER_HUB. It returns what the command printed and its error.
func runCommand(t *testing.T, h *fakeHub, a *answers, args ...string) (string, error) {
	t.Helper()
	t.Setenv("TENDER_HUB", h.url())
	run, ok := hubCommands[args[0]]
	if !ok {
		t.Fatalf("%s is not a hub command", args[0])
	}
	var out bytes.Buffer
	s := session{out: &out, prompt: a.ask, now: func() time.Time { return h.now }}
	err := run(args[1:], s)
	return out.String(), err
}
