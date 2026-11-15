// Package channels holds the notification adapters: the types of channel an alert can go to.
// Each type is one Adapter: the fields of its config, their check, and how it sends an event.
// Adding a type is one file here and its tests; the hub stores every config as JSON, so the
// schema doesn't change.
package channels

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Version is the version of the event contract, in every event's "version".
const Version = 1

// EventTest is the event of a test notification.
const EventTest = "notification.test"

// The phases of an event name: a problem starts as <area>.<condition>, gets worse as
// <area>.<condition>.escalated and clears as <area>.<condition>.resolved.
const (
	PhaseOpened    = "opened"
	PhaseEscalated = "escalated"
	PhaseResolved  = "resolved"
	PhaseTest      = "test"
)

// Phase is the phase of the event name event.
func Phase(event string) string {
	switch {
	case event == EventTest:
		return PhaseTest
	case strings.HasSuffix(event, "."+PhaseEscalated):
		return PhaseEscalated
	case strings.HasSuffix(event, "."+PhaseResolved):
		return PhaseResolved
	}
	return PhaseOpened
}

// Base is the event name without .escalated or .resolved: the problem it is about.
func Base(event string) string {
	for _, p := range []string{PhaseEscalated, PhaseResolved} {
		if b, ok := strings.CutSuffix(event, "."+p); ok {
			return b
		}
	}
	return event
}

// Event is the event contract v1, the JSON body a webhook gets and an exec adapter reads on its
// standard input. Log text is never in an event.
type Event struct {
	Version      int          `json:"version"`
	Event        string       `json:"event"` // such as service.down, service.down.resolved or notification.test
	Notification Notification `json:"notification"`
	Hub          string       `json:"hub"` // the hub's URL
}

// Notification is the notification an event carries.
type Notification struct {
	ID        int64     `json:"id"`
	Event     string    `json:"event"`
	Severity  string    `json:"severity"`
	Machine   *string   `json:"machine"` // the machine it is about, or null
	Service   *string   `json:"service"`   // the service it is about, or null
	Container *string   `json:"container"` // the container in the service it is about, or null
	Channel   *string   `json:"channel"` // the channel a channel.failing notification is about, or null
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// Field is one key of a channel type's config. Every value is a string.
type Field struct {
	Name     string
	Secret   bool // stored encrypted; the API shows "***" and <name>_set instead
	Required bool
}

// Config is a channel's config, with its secrets in the clear. Only the hub's memory holds it so.
type Config map[string]string

// Adapter is one type of channel.
type Adapter interface {
	// Fields lists the keys of the config.
	Fields() []Field
	// Check is an error, a sentence that says how to fix it, unless cfg can be used to send.
	Check(cfg Config) error
	// Send delivers ev. A nil error means delivered. A Permanent error is not tried again.
	// No error holds a secret of cfg.
	Send(ctx context.Context, cfg Config, ev Event) error
}

// Permanent marks a failure that trying again can't fix, such as a 4xx answer other than 429.
type Permanent struct{ Err error }

func (p *Permanent) Error() string { return p.Err.Error() }
func (p *Permanent) Unwrap() error { return p.Err }

// IsPermanent reports whether err is not worth trying again.
func IsPermanent(err error) bool {
	var p *Permanent
	return errors.As(err, &p)
}

// maxErrorBytes caps the text of an error the hub stores from an answer or an adapter's output.
const maxErrorBytes = 1 << 10

// cut is the first maxErrorBytes of s, trimmed, never splitting a character.
func cut(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxErrorBytes {
		return s
	}
	s = s[:maxErrorBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Maker is an Adapter that makes part of a new channel's config itself, such as a secret. Make
// fills those fields in cfg and names them, so the hub shows them once, in the answer to the
// create.
type Maker interface {
	Make(cfg Config) (made []string, err error)
}
