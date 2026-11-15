// Package sinks holds the log sink types: how each one checks its config and sends a batch of
// records. A new type is one file here and its tests.
package sinks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/server"
)

// Config is a sink's config, as JSON decodes it.
type Config = map[string]any

// Record is one log line on its way to a sink.
type Record struct {
	Time     time.Time `json:"time"`
	Source   string    `json:"source"`            // journal or service
	Unit     string    `json:"unit,omitempty"`    // the systemd unit, or the program, for the journal
	Service  string    `json:"service,omitempty"` // the container's name, for a service
	Priority string    `json:"priority"`          // emerg, alert, crit, err, warning, notice, info or debug
	Message  string    `json:"message"`
}

// The sources a record comes from.
const (
	SourceJournal = "journal"
	SourceService = "service"
)

// Priorities are the journal's priority names, most severe first; a record's priority is one.
var Priorities = []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

// Name is the unit or the service a record is about.
func (r Record) Name() string {
	if r.Source == SourceService {
		return r.Service
	}
	return r.Unit
}

// Type is one kind of sink.
type Type interface {
	// Fields are the config fields the type takes; Secrets are the ones that are secret.
	Fields() []string
	Secrets() []string
	// Generated is the secret field the hub makes when it is left out, or "".
	Generated() string
	// Check returns the config cleaned up, or a 422 that says what is wrong in a sentence.
	Check(c Config) (Config, error)
	// New returns the sender for a checked config, for the machine called machine.
	New(c Config, machine string) (Sender, error)
}

// Sender sends one batch of records to a sink.
type Sender interface {
	Send(ctx context.Context, records []Record) error
}

var types = map[string]Type{"otlp": otlp{}, "webhook": webhook{}}

// Get returns the type called name.
func Get(name string) (Type, bool) {
	t, ok := types[name]
	return t, ok
}

// Names lists the types, sorted.
func Names() []string {
	out := make([]string, 0, len(types))
	for n := range types {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// IsSecret reports whether field is one of t's secrets.
func IsSecret(t Type, field string) bool { return slices.Contains(t.Secrets(), field) }

// Error is a failed send. Permanent means the sink refused this batch for good, so sending it
// again can't help: it answered 400, 413 or 422. The message never holds the sink's answer,
// which may echo the logs, nor the URL, which may hold a key.
type Error struct {
	Status    int // the sink's HTTP status, or 0 when it didn't answer
	Permanent bool
	msg       string
}

func (e *Error) Error() string { return e.msg }

// sendTimeout is how long one send may take.
const sendTimeout = 30 * time.Second

var httpClient = &http.Client{Timeout: sendTimeout}

// post sends body to target with headers, and turns any answer but a 2xx into an *Error.
func post(ctx context.Context, target string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return &Error{msg: "the sink's URL is not valid"}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tender")
	resp, err := httpClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &Error{msg: "the sink didn't answer: " + err.Error()}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	permanent := resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge ||
		resp.StatusCode == http.StatusUnprocessableEntity
	return &Error{Status: resp.StatusCode, Permanent: permanent,
		msg: fmt.Sprintf("the sink answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))}
}

// decode reads c into v, a struct with json tags, and refuses fields the type doesn't take.
func decode(typ string, t Type, c Config, v any) error {
	b, err := json.Marshal(c)
	if err != nil {
		return server.Invalid("config must be a JSON object.")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var wrongType *json.UnmarshalTypeError
		if errors.As(err, &wrongType) {
			return server.Invalid("config.%s must be %s.", wrongType.Field, wrongType.Type)
		}
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return server.Invalid("config has %s, which a %s sink doesn't take. It takes %s.", field, typ, strings.Join(t.Fields(), ", "))
	}
	return nil
}

// encode turns a config struct back into a Config.
func encode(v any) Config {
	b, _ := json.Marshal(v)
	var c Config
	_ = json.Unmarshal(b, &c)
	return c
}

// checkURL is an error unless s is an http or https URL without a login or a fragment.
func checkURL(field, s, example string) (string, error) {
	if s == "" {
		return "", server.Invalid("config.%s is required: an http or https URL, such as %s.", field, example)
	}
	u, err := url.Parse(s)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return "", server.Invalid("config.%s must be an http or https URL, such as %s.", field, example)
	case u.User != nil:
		return "", server.Invalid("config.%s can't hold a login. Put it in a header or the secret, which are kept encrypted.", field)
	case u.Fragment != "" || strings.Contains(s, "#"):
		return "", server.Invalid("config.%s can't have a fragment.", field)
	}
	return s, nil
}
