package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/logs/sinks"
)

// journalSource follows the systemd journal with journalctl, from a saved cursor.
type journalSource struct {
	bin    string // journalctl, found on PATH
	filter JournalSource
	log    *slog.Logger
}

func (journalSource) kind() string { return sinks.SourceJournal }

// journalRetry is the longest wait before journalctl is started again after it stopped.
const journalRetry = time.Minute

// args are journalctl's arguments to follow the journal after cursor, or from since without one.
func (j journalSource) args(cursor string, since time.Time) []string {
	args := []string{"--follow", "--no-tail", "--output=json", "--no-pager", "--quiet"}
	if cursor != "" {
		args = append(args, "--after-cursor="+cursor)
	} else {
		args = append(args, "--since=@"+strconv.FormatInt(since.Unix(), 10))
	}
	for _, u := range j.filter.Units {
		if unitPattern.MatchString(u) { // the hub checked them; an option must never get through
			args = append(args, "--unit="+u)
		}
	}
	if j.filter.MinPriority != "" && j.filter.MinPriority != "debug" {
		args = append(args, "--priority="+j.filter.MinPriority)
	}
	return args
}

func (j journalSource) run(ctx context.Context, start cursors, out chan<- item) {
	cursor, retry := start.Journal, time.Second
	missing := false
	for ctx.Err() == nil {
		bin, err := exec.LookPath(j.bin)
		if err != nil {
			if !missing {
				j.log.Warn("journalctl is not installed, so the journal can't be sent; trying again every minute")
				missing = true
			}
			if !wait(ctx, journalRetry) {
				return
			}
			continue
		}
		missing = false
		began := time.Now()
		next, err := j.follow(ctx, bin, cursor, start.JournalSince, out)
		if next != "" {
			cursor = next
		}
		if ctx.Err() != nil {
			return
		}
		if time.Since(began) > journalRetry {
			retry = time.Second
		}
		j.log.Warn("journalctl stopped; starting it again", "error", err, "in", retry)
		if !wait(ctx, retry) {
			return
		}
		retry = min(2*retry, journalRetry)
	}
}

// follow runs journalctl until it stops or ctx ends, and returns the cursor of the last entry it sent.
func (j journalSource) follow(ctx context.Context, bin, cursor string, since time.Time, out chan<- item) (string, error) {
	cmd := exec.CommandContext(ctx, bin, j.args(cursor, since)...)
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &capped{buf: &stderr, left: 4 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	last := ""
	scanErr := readLines(stdout, journalLineBytes, func(line []byte, cut bool) bool {
		it, ok := journalEntry(line)
		if cut {
			it, ok = journalCut(line)
		}
		if !ok {
			return true
		}
		if !send(ctx, out, it) {
			return false
		}
		last = it.journal
		return true
	})
	if ctx.Err() != nil {
		_ = cmd.Wait()
		return last, ctx.Err()
	}
	_ = stdout.Close()
	err = cmd.Wait()
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		err = errors.New(msg)
	} else if scanErr != nil {
		err = scanErr
	} else if err == nil {
		err = errors.New("it exited")
	}
	return last, err
}

// journalLineBytes is the most of one line of journalctl's output the daemon reads. journalctl
// prints a field over 4 KiB as null, so only an entry with very many fields comes near it.
const journalLineBytes = 8 << 20

// cutField finds a string field in the start of a journal entry that was cut.
var cutField = regexp.MustCompile(`"(__CURSOR|__REALTIME_TIMESTAMP|_SYSTEMD_UNIT|SYSLOG_IDENTIFIER|_COMM|PRIORITY)":"((?:[^"\\]|\\.)*)"`)

// journalCut makes a record of an entry longer than journalLineBytes, from the fields at its
// start, so the stream goes on past it: its message is cut, and ends in truncatedMark.
func journalCut(b []byte) (item, bool) {
	fields := map[string]json.RawMessage{}
	for _, m := range cutField.FindAllSubmatch(b, -1) {
		if _, seen := fields[string(m[1])]; !seen {
			fields[string(m[1])] = json.RawMessage(`"` + string(m[2]) + `"`)
		}
	}
	msg := ""
	if _, rest, ok := bytes.Cut(b, []byte(`"MESSAGE":"`)); ok {
		raw := rest[:min(len(rest), maxMessageBytes)]
		for len(raw) > 0 && json.Unmarshal([]byte(`"`+string(raw)+`"`), &msg) != nil {
			raw = raw[:len(raw)-1] // drop a split escape at the end
		}
	}
	fields["MESSAGE"], _ = json.Marshal(msg)
	e, _ := json.Marshal(fields)
	it, ok := journalEntry(e)
	if ok {
		it.rec.Message = message(msg, true)
	}
	return it, ok
}

// journalEntry turns one line of journalctl's JSON output into a record. MESSAGE is a string, or
// an array of bytes when it isn't valid UTF-8; an entry without one, or without a cursor, is skipped.
func journalEntry(b []byte) (item, bool) {
	var e map[string]json.RawMessage
	if json.Unmarshal(b, &e) != nil {
		return item{}, false
	}
	msg, ok := journalText(e["MESSAGE"])
	cursor := journalString(e["__CURSOR"])
	if !ok || cursor == "" {
		return item{}, false
	}
	when := time.Now().UTC()
	if us, err := strconv.ParseInt(journalString(e["__REALTIME_TIMESTAMP"]), 10, 64); err == nil {
		when = time.UnixMicro(us).UTC()
	}
	unit := "-"
	for _, k := range []string{"_SYSTEMD_UNIT", "SYSLOG_IDENTIFIER", "_COMM"} {
		if v := journalString(e[k]); v != "" {
			unit = v
			break
		}
	}
	priority := "info"
	if p, err := strconv.Atoi(journalString(e["PRIORITY"])); err == nil && p >= 0 && p < len(sinks.Priorities) {
		priority = sinks.Priorities[p]
	}
	return item{journal: cursor, rec: sinks.Record{Time: when, Source: sinks.SourceJournal, Unit: unit, Priority: priority, Message: message(msg, false)}}, true
}

func journalString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func journalText(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var nums []int
	if json.Unmarshal(raw, &nums) != nil {
		return "", false
	}
	b := make([]byte, len(nums))
	for i, n := range nums {
		b[i] = byte(n)
	}
	return string(b), true
}

// capped keeps the first left bytes written to it.
type capped struct {
	buf  *bytes.Buffer
	left int
}

func (c *capped) Write(p []byte) (int, error) {
	n := min(len(p), c.left)
	c.buf.Write(p[:n])
	c.left -= n
	return len(p), nil
}
