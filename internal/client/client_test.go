package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type seen struct {
	method, path, auth, key, body string
}

func fakeHub(t *testing.T, handler func(n int, w http.ResponseWriter)) (*Client, func() []seen) {
	t.Helper()
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), string(b)})
		n := len(got)
		mu.Unlock()
		handler(n, w)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", "tok"), func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen(nil), got...)
	}
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"type":"about:blank","title":"Nope","status":`+strconv.Itoa(status)+`,"detail":"`+detail+`"}`)
}

func TestDoSendsJSONAndBearer(t *testing.T) {
	c, requests := fakeHub(t, func(_ int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":7}`)
	})
	var out struct{ ID int }
	if err := c.Do(context.Background(), http.MethodPost, "/v1/x", "key-1", map[string]int{"a": 1}, &out); err != nil {
		t.Fatal(err)
	}
	r := requests()[0]
	if out.ID != 7 || r.auth != "Bearer tok" || r.key != "key-1" || r.path != "/v1/x" || !strings.Contains(r.body, `"a":1`) {
		t.Errorf("out=%+v request=%+v", out, r)
	}
}

func TestProblemBecomesTypedError(t *testing.T) {
	c, _ := fakeHub(t, func(_ int, w http.ResponseWriter) { writeProblem(w, http.StatusNotFound, "no settings yet") })
	err := c.Do(context.Background(), http.MethodGet, "/v1/x", "", nil, nil)
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not *Error", err)
	}
	if e.Status != 404 || e.Detail != "no settings yet" || e.Title != "Nope" {
		t.Errorf("got %+v", e)
	}
	if !strings.Contains(err.Error(), "no settings yet") {
		t.Errorf("error text %q lacks the detail", err)
	}
	if Retryable(err) {
		t.Error("404 must be permanent")
	}
}

func TestNonProblemErrorBody(t *testing.T) {
	c, _ := fakeHub(t, func(_ int, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html>bad gateway</html>")
	})
	err := c.Do(context.Background(), http.MethodGet, "/v1/x", "", nil, nil)
	var e *Error
	if !errors.As(err, &e) || e.Status != 502 || e.Title != "Bad Gateway" {
		t.Errorf("error = %v", err)
	}
}

func TestRetryable(t *testing.T) {
	for status, want := range map[int]bool{
		400: false, 401: false, 403: false, 404: false, 409: false, 422: false,
		408: true, 429: true, 500: true, 502: true, 503: true,
	} {
		err := &Error{Status: status}
		if got := Retryable(err); got != want {
			t.Errorf("Retryable(%d) = %v, want %v", status, got, want)
		}
	}
	if Retryable(nil) {
		t.Error("nil is not retryable")
	}
	if Retryable(context.Canceled) {
		t.Error("cancel is not retryable")
	}

	// A connection that nobody listens on is a network error, so retryable.
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	err := New(base, "t").Do(context.Background(), http.MethodGet, "/v1/x", "", nil, nil)
	if err == nil || !Retryable(err) {
		t.Errorf("network error = %v, want retryable", err)
	}
}

func TestIdempotencyKeyIsReusedOnRetry(t *testing.T) {
	c, requests := fakeHub(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			writeProblem(w, http.StatusServiceUnavailable, "try later")
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	key := NewKey()
	var err error
	for range 3 {
		if err = c.Do(context.Background(), http.MethodPost, "/v1/x", key, map[string]int{}, nil); err == nil || !Retryable(err) {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	got := requests()
	if len(got) != 2 || got[0].key == "" || got[0].key != got[1].key {
		t.Errorf("keys across retry = %+v", got)
	}
	if first := NewKey(); first == NewKey() {
		t.Error("NewKey repeats")
	}
}
