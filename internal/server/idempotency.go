package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

const keyLifetime = 24 * time.Hour

// replayed are the headers a replay repeats.
var replayed = []string{"Content-Type", "Location"}

// once runs h unless this machine already used the key: then it replays the stored status,
// headers and body. A key repeated on another method or path is a 422. The first response is stored unless
// it is a 5xx. The lock makes two identical requests at once run h only once.
func (s *Server) once(w http.ResponseWriter, r *http.Request, machineID int64, key string, h HandlerFunc) {
	s.keys.Lock()
	defer s.keys.Unlock()

	var method, path string
	var status int
	var body []byte
	var headers string
	err := s.db.Read.QueryRowContext(r.Context(),
		`SELECT method, path, status_code, response_body, response_headers FROM idempotency_keys WHERE machine_id = ? AND key = ?`,
		machineID, key).Scan(&method, &path, &status, &body, &headers)
	switch {
	case err == nil:
		if method != r.Method || path != r.URL.Path {
			s.fail(w, r, Invalid("Idempotency-Key was first used for %s %s.", method, path))
			return
		}
		stored := map[string]string{}
		_ = json.Unmarshal([]byte(headers), &stored)
		if _, ok := stored["Content-Type"]; !ok && len(body) > 0 { // stored before the headers were
			stored["Content-Type"] = "application/json"
			if status >= 400 {
				stored["Content-Type"] = "application/problem+json"
			}
		}
		for _, k := range replayed {
			if v, ok := stored[k]; ok {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	case !errors.Is(err, sql.ErrNoRows):
		s.fail(w, r, err)
		return
	}

	rec := &recorder{header: http.Header{}}
	s.run(rec, r, h)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	if rec.status < 500 {
		now := time.Now().Unix()
		stored := map[string]string{}
		for _, k := range replayed {
			if v := rec.header.Get(k); v != "" {
				stored[k] = v
			}
		}
		headers, _ := json.Marshal(stored)
		_, err := s.db.Write.ExecContext(r.Context(),
			`INSERT INTO idempotency_keys (machine_id, key, method, path, status_code, response_body, response_headers, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			machineID, key, r.Method, r.URL.Path, rec.status, append([]byte{}, rec.body.Bytes()...), string(headers), now, now)
		if err != nil {
			s.log.Error("store idempotency key", "error", err)
		}
	}
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body.Bytes())
}

// recorder keeps a handler's response so it can be stored before it is sent.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *recorder) Header() http.Header { return c.header }

func (c *recorder) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}

func (c *recorder) Write(b []byte) (int, error) {
	c.WriteHeader(http.StatusOK)
	return c.body.Write(b)
}

// PruneKeys deletes idempotency keys older than 24 hours, now and then every hour until ctx ends.
func (s *Server) PruneKeys(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if err := s.pruneKeys(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.log.Error("prune idempotency keys", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Server) pruneKeys(ctx context.Context, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE created_at < ?`, now.Add(-keyLifetime).Unix())
	return err
}
