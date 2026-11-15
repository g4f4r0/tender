package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
)

const (
	maxBodyBytes = 1 << 20
	defaultLimit = 50
	maxLimit     = 500
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidName reports whether s is fit to be a machine or destination name: it appears in URLs.
func ValidName(s string) bool { return namePattern.MatchString(s) }

// Decode reads the request body, one JSON value of at most 1 MiB with no unknown fields, into v.
// Malformed JSON is a 400. JSON of the wrong type for a field is a 422.
func Decode(w http.ResponseWriter, r *http.Request, v any) error { return decode(w, r, v, true) }

// DecodeFromDaemon is Decode for the routes only a daemon calls, which ignore unknown fields: a
// daemon updated before its hub, during a rollout, may send fields this hub doesn't know yet.
func DecodeFromDaemon(w http.ResponseWriter, r *http.Request, v any) error {
	return decode(w, r, v, false)
}

func decode(w http.ResponseWriter, r *http.Request, v any, strict bool) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		var wrongType *json.UnmarshalTypeError
		switch {
		case errors.As(err, &tooBig):
			return &Error{Status: http.StatusRequestEntityTooLarge, Detail: "The body is larger than 1 MiB."}
		case errors.As(err, &wrongType) && wrongType.Field == "":
			return Invalid("The request body must be a JSON object.")
		case errors.As(err, &wrongType):
			return Invalid("Field %q must be %s.", wrongType.Field, wrongType.Type)
		}
		return BadRequest("The body is not valid JSON for this route: %v.", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return BadRequest("The body must hold one JSON value.")
	}
	return nil
}

// Cursor marks a row in a newest-first list.
type Cursor struct {
	CreatedAt int64
	ID        int64
}

// Page is a parsed ?limit= and ?cursor=. Query rows older than Before, newest first, fetching Size().
type Page struct {
	Limit  int
	Before Cursor // everything when there is no cursor
}

// Size is how many rows to fetch: one more than the limit, to know whether another page exists.
func (p Page) Size() int { return p.Limit + 1 }

// ParsePage reads ?limit= (default 50, at most 500) and ?cursor=.
func ParsePage(r *http.Request) (Page, error) {
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Page{}, errLimit()
		}
		limit = n
	}
	return NewPage(limit, q.Get("cursor"))
}

// NewPage returns the page that limit and cursor name. A limit of 0 means the default, 50;
// the most is 500. An empty cursor means the first page.
func NewPage(limit int, cursor string) (Page, error) {
	p := Page{Limit: defaultLimit, Before: Cursor{math.MaxInt64, math.MaxInt64}}
	if limit != 0 {
		if limit < 1 || limit > maxLimit {
			return p, errLimit()
		}
		p.Limit = limit
	}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err == nil {
			_, err = fmt.Sscanf(string(raw), "%d:%d", &p.Before.CreatedAt, &p.Before.ID)
		}
		if err != nil {
			return p, BadRequest("cursor is not valid.")
		}
	}
	return p, nil
}

func errLimit() error { return BadRequest("limit must be a whole number from 1 to %d.", maxLimit) }

// NewList turns the rows fetched with p.Size() into a response, cutting the extra row and
// setting next_cursor from the last row kept.
func NewList[T any](rows []T, p Page, cursor func(T) Cursor) List[T] {
	if rows == nil {
		rows = []T{}
	}
	if len(rows) <= p.Limit {
		return List[T]{Data: rows}
	}
	rows = rows[:p.Limit]
	c := cursor(rows[p.Limit-1])
	raw := fmt.Sprintf("%d:%d", c.CreatedAt, c.ID)
	return List[T]{Data: rows, NextCursor: base64.RawURLEncoding.EncodeToString([]byte(raw))}
}
