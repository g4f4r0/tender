// Package server is the hub's HTTP server: routing, JSON and problem responses, and health.
// Domains add their routes with Handle; this package never imports a domain.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/g4f4r0/tender/internal/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations creates the server's own tables. It needs the machines domain's tables first.
var Migrations = db.Migrations{Domain: "server", Files: migrationFiles, Dir: "migrations"}

// List is the shape of every collection response: {"data": [...], "next_cursor": "..."}.
type List[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Server routes REST requests under /v1.
type Server struct {
	mux      *http.ServeMux
	db       *db.DB
	log      *slog.Logger
	machines Machines
	keys     sync.Mutex // serializes requests that carry an Idempotency-Key
}

// New returns a server with GET /v1/health registered. Machines resolves tokens and names.
func New(d *db.DB, log *slog.Logger, machines Machines) *Server {
	s := &Server{mux: http.NewServeMux(), db: d, log: log, machines: machines}
	s.mux.HandleFunc("GET /v1/health", s.health)
	return s
}

// Handle registers a route, such as "GET /v1/machines/{machine}".
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	if h, pattern := s.mux.Handler(r); pattern == "" {
		notRouted(rec, r, h)
	} else {
		s.mux.ServeHTTP(rec, r)
	}
	attrs := []any{"method", r.Method, "path", r.URL.Path, "status", rec.status,
		"duration_ms", time.Since(start).Milliseconds()}
	if user := r.Header.Get(loginHeader); user != "" {
		attrs = append(attrs, "user", user)
	}
	s.log.Info("request", attrs...)
}

// notRouted answers requests that match no route (404) or no method (405) with problem
// details instead of the mux's plain text, keeping the mux's status and Allow header.
func notRouted(w http.ResponseWriter, r *http.Request, h http.Handler) {
	c := &discard{header: http.Header{}, status: http.StatusOK}
	h.ServeHTTP(c, r)
	if allow := c.header.Get("Allow"); allow != "" {
		w.Header().Set("Allow", allow)
	}
	detail := r.Method + " " + r.URL.Path + " is not a route on this hub."
	if c.status == http.StatusMethodNotAllowed {
		detail = r.URL.Path + " doesn't accept " + r.Method + ". Allowed: " + c.header.Get("Allow") + "."
	}
	WriteProblem(w, c.status, http.StatusText(c.status), detail)
}

// discard records a response's status and headers and drops its body.
type discard struct {
	header http.Header
	status int
}

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(b []byte) (int, error) { return len(b), nil }
func (d *discard) WriteHeader(code int)        { d.status = code }

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		s.log.Error("health: database unreachable", "error", err)
		WriteProblem(w, http.StatusServiceUnavailable, "Database unreachable", "The hub can't reach its database.")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	write(w, status, "application/json", v)
}

// WriteProblem writes an RFC 9457 problem details response.
func WriteProblem(w http.ResponseWriter, status int, title, detail string) {
	write(w, status, "application/problem+json", map[string]any{
		"type": "about:blank", "title": title, "status": status, "detail": detail,
	})
}

func write(w http.ResponseWriter, status int, contentType string, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error: response encoding failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	// A failed write means the client went away; there is no one left to tell.
	_, _ = w.Write(append(body, '\n'))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
