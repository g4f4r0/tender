package logs

import (
	"bufio"
	"context"
	"errors"
	"io"
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

// truncatedMark ends a message that was cut.
const truncatedMark = " [truncated]"

// message cleans a log line for a record: no trailing line break, valid UTF-8, and at most
// maxMessageBytes, ending in truncatedMark when it was longer or cut is set.
func message(s string, cut bool) string {
	s = strings.TrimRight(s, "\r\n")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if len(s) > maxMessageBytes || (cut && len(s) > maxMessageBytes-len(truncatedMark)) {
		s = s[:maxMessageBytes-len(truncatedMark)]
		for !utf8.ValidString(s) { // don't split a character
			s = s[:len(s)-1]
		}
		cut = true
	}
	if cut {
		s += truncatedMark
	}
	return s
}

// readLines calls fn with each line of r, without its line break, until fn returns false or r
// ends. A line longer than limit comes cut to limit, with cut set, and the rest of it is skipped,
// so one long line never stops the stream. line is only valid during the call.
func readLines(r io.Reader, limit int, fn func(line []byte, cut bool) bool) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	cut := false
	for {
		chunk, err := br.ReadSlice('\n')
		ended := err == nil // chunk ends with the line break
		if ended {
			chunk = chunk[:len(chunk)-1]
		}
		take := min(len(chunk), limit-len(line))
		line = append(line, chunk[:take]...)
		cut = cut || take < len(chunk)
		switch {
		case ended:
			if !fn(line, cut) {
				return nil
			}
			line, cut = line[:0], false
		case errors.Is(err, bufio.ErrBufferFull): // the line goes on
		case errors.Is(err, io.EOF):
			if len(line) > 0 || cut {
				fn(line, cut)
			}
			return nil
		default:
			return err
		}
	}
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
