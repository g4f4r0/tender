package server

import (
	"errors"
	"fmt"
	"net/http"
)

// Error is an error that a handler wants sent as a problem response with this status.
// Any other error becomes a 500 and is logged, never shown.
type Error struct {
	Status int
	Detail string
	Type   string // the problem's type URI, for a client that must tell this problem apart; empty means about:blank
}

func (e *Error) Error() string { return e.Detail }

// BadRequest is a 400: the request is malformed.
func BadRequest(format string, a ...any) error {
	return &Error{Status: http.StatusBadRequest, Detail: fmt.Sprintf(format, a...)}
}

// NotFound is a 404.
func NotFound(format string, a ...any) error {
	return &Error{Status: http.StatusNotFound, Detail: fmt.Sprintf(format, a...)}
}

// Conflict is a 409: the request clashes with the current state.
func Conflict(format string, a ...any) error {
	return &Error{Status: http.StatusConflict, Detail: fmt.Sprintf(format, a...)}
}

// Invalid is a 422: the request is well formed but its values are not acceptable.
func Invalid(format string, a ...any) error {
	return &Error{Status: http.StatusUnprocessableEntity, Detail: fmt.Sprintf(format, a...)}
}

func isNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// fail answers a handler's error.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if errors.As(err, &e) {
		typ := e.Type
		if typ == "" {
			typ = "about:blank"
		}
		write(w, e.Status, "application/problem+json", map[string]any{
			"type": typ, "title": http.StatusText(e.Status), "status": e.Status, "detail": e.Detail,
		})
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	WriteProblem(w, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError),
		"The hub failed to handle this request.")
}
