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
}

func (e *Error) Error() string { return e.Detail }

// BadRequest is a 400: the request is malformed.
func BadRequest(format string, a ...any) error {
	return &Error{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

// NotFound is a 404.
func NotFound(format string, a ...any) error {
	return &Error{http.StatusNotFound, fmt.Sprintf(format, a...)}
}

// Conflict is a 409: the request clashes with the current state.
func Conflict(format string, a ...any) error {
	return &Error{http.StatusConflict, fmt.Sprintf(format, a...)}
}

// Invalid is a 422: the request is well formed but its values are not acceptable.
func Invalid(format string, a ...any) error {
	return &Error{http.StatusUnprocessableEntity, fmt.Sprintf(format, a...)}
}

func isNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// fail answers a handler's error.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if errors.As(err, &e) {
		WriteProblem(w, e.Status, http.StatusText(e.Status), e.Detail)
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	WriteProblem(w, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError),
		"The hub failed to handle this request.")
}
