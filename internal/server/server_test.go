package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/g4f4r0/tender/internal/db"
)

func hub(t *testing.T) (*db.DB, *httptest.Server) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	t.Cleanup(srv.Close)
	return d, srv
}

// call sends a request and decodes a JSON body into out, if out is not nil.
func call(t *testing.T, method, url string, out any) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode body: %v", method, url, err)
		}
	}
	return res
}

func TestHealth(t *testing.T) {
	d, srv := hub(t)

	var body map[string]string
	res := call(t, "GET", srv.URL+"/v1/health", &body)
	if res.StatusCode != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthy hub: got %d %v, want 200 ok", res.StatusCode, body)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	var problem map[string]any
	res = call(t, "GET", srv.URL+"/v1/health", &problem)
	if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("closed database: got %d %s, want 503 problem+json", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if problem["status"] != float64(http.StatusServiceUnavailable) || problem["title"] == "" {
		t.Fatalf("problem body = %v", problem)
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	d, srv := hub(t)
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})

	for _, c := range []struct {
		method, path, allow string
		want                int
	}{
		{"GET", "/v1/nope", "", http.StatusNotFound},
		{"POST", "/v1/health", "GET, HEAD", http.StatusMethodNotAllowed},
	} {
		var problem map[string]any
		res := call(t, c.method, srv.URL+c.path, &problem)
		if res.StatusCode != c.want || res.Header.Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s %s = %d %s, want %d problem+json", c.method, c.path, res.StatusCode, res.Header.Get("Content-Type"), c.want)
		}
		if problem["status"] != float64(c.want) || problem["detail"] == "" {
			t.Errorf("%s %s problem body = %v", c.method, c.path, problem)
		}
		if got := res.Header.Get("Allow"); got != c.allow {
			t.Errorf("%s %s Allow = %q, want %q", c.method, c.path, got, c.allow)
		}
	}
}
