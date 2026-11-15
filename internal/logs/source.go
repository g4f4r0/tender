package logs

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/g4f4r0/tender/internal/logs/sinks"
)

// item is one record from a source, with where the source got to, so a restart resumes after it.
type item struct {
	rec     sinks.Record
	journal string // the journal cursor of a journal record
}

// cursors is where each of a sink's sources got to in the logs already in its buffer. A restart
// resumes after them, so it neither loses nor repeats what the buffer holds.
type cursors struct {
	// Since is when the daemon first sent to the sink: logs from before it are never sent.
	Since    time.Time            `json:"since"`
	Journal  string               `json:"journal,omitempty"`
	Services map[string]time.Time `json:"services,omitempty"` // by container name, the time of its last record
}

func (c cursors) clone() cursors {
	out := c
	out.Services = make(map[string]time.Time, len(c.Services))
	for k, v := range c.Services {
		out.Services[k] = v
	}
	return out
}

// apply moves c past it.
func (c *cursors) apply(it item) {
	switch it.rec.Source {
	case sinks.SourceJournal:
		if it.journal != "" {
			c.Journal = it.journal
		}
	case sinks.SourceService:
		if c.Services == nil {
			c.Services = map[string]time.Time{}
		}
		if it.rec.Time.After(c.Services[it.rec.Service]) {
			c.Services[it.rec.Service] = it.rec.Time
		}
	}
}

// source reads one kind of log and sends what it finds to out, from start on, until ctx ends.
type source interface {
	run(ctx context.Context, start cursors, out chan<- item)
}

// maxMessageBytes is the most of one log line a record carries.
const maxMessageBytes = 64 << 10

// message cleans a log line for a record: no trailing line break, at most maxMessageBytes, and
// valid UTF-8.
func message(s string) string {
	s = strings.TrimRight(s, "\r\n")
	if len(s) > maxMessageBytes {
		s = s[:maxMessageBytes]
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return s
}

// send hands it to out, unless ctx ends first.
func send(ctx context.Context, out chan<- item, it item) bool {
	select {
	case out <- it:
		return true
	case <-ctx.Done():
		return false
	}
}

// wait sleeps for d, unless ctx ends first.
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
