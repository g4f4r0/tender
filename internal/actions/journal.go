package actions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Query is what to read from the machine's journal.
type Query struct {
	Unit     string        // a systemd unit, or "" for every unit
	Priority string        // this priority and the more severe ones, or "" for all
	Since    time.Duration // how far back, or 0 for no limit
	Lines    int           // at most this many of the newest entries
}

// errNoJournal means journalctl isn't installed, so the machine's logs can't be read.
var errNoJournal = errors.New("journalctl is not installed on this machine, so Tender can't read its logs")

// journalctl is the program that reads the journal. Tests point it elsewhere.
var journalctl = "journalctl"

// ReadJournal runs journalctl and returns the entries as text, one per line: the time in UTC,
// the unit (or the program, for an entry without one) and the message. It keeps at most
// MaxOutputBytes, the newest, as valid UTF-8.
func ReadJournal(ctx context.Context, q Query, now time.Time) (string, error) {
	bin, err := exec.LookPath(journalctl)
	if err != nil {
		return "", errNoJournal
	}
	args := []string{"--output=json", "--no-pager", "--quiet", "--lines=" + strconv.Itoa(max(1, min(q.Lines, MaxLines)))}
	if q.Unit != "" {
		if !unitPattern.MatchString(q.Unit) { // the hub checked it; a flag must never get through
			return "", fmt.Errorf("%q is not a systemd unit name", q.Unit)
		}
		args = append(args, "--unit="+q.Unit)
	}
	if q.Priority != "" {
		args = append(args, "--priority="+q.Priority)
	}
	if q.Since > 0 {
		args = append(args, "--since=@"+strconv.FormatInt(now.Add(-q.Since).Unix(), 10))
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &limited{w: &stderr, left: 4 << 10}
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("journalctl failed: %s", msg)
		}
		return "", fmt.Errorf("journalctl failed: %w", err)
	}
	var lines []string
	size := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line, ok := entry(sc.Bytes())
		if !ok {
			continue
		}
		lines = append(lines, line)
		size += len(line) + 1
		for size > MaxOutputBytes && len(lines) > 0 {
			size -= len(lines[0]) + 1
			lines = lines[1:]
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read journalctl's output: %w", err)
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// entry formats one line of journalctl's JSON output. MESSAGE is a string, or an array of bytes
// when it isn't valid UTF-8; a line without one is skipped.
func entry(b []byte) (string, bool) {
	var e map[string]json.RawMessage
	if json.Unmarshal(b, &e) != nil {
		return "", false
	}
	msg, ok := text(e["MESSAGE"])
	if !ok {
		return "", false
	}
	when := "-"
	if us, err := strconv.ParseInt(str(e["__REALTIME_TIMESTAMP"]), 10, 64); err == nil {
		when = time.UnixMicro(us).UTC().Format("2006-01-02T15:04:05.000000000Z") // as Docker writes its timestamps
	}
	who := "-"
	for _, k := range []string{"_SYSTEMD_UNIT", "SYSLOG_IDENTIFIER", "_COMM"} {
		if v := str(e[k]); v != "" {
			who = v
			break
		}
	}
	msg = strings.ReplaceAll(strings.TrimRight(msg, "\n"), "\n", " ")
	return strings.ToValidUTF8(when+" "+who+" "+msg, "�"), true
}

func str(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func text(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var bs []byte
	var nums []int
	if json.Unmarshal(raw, &nums) != nil {
		return "", false
	}
	for _, n := range nums {
		bs = append(bs, byte(n))
	}
	if !utf8.Valid(bs) {
		return strings.ToValidUTF8(string(bs), "�"), true
	}
	return string(bs), true
}

// limited keeps the first left bytes written to it, so a chatty journalctl can't fill memory.
type limited struct {
	w    *bytes.Buffer
	left int
}

func (l *limited) Write(p []byte) (int, error) {
	n := min(len(p), l.left)
	l.w.Write(p[:n])
	l.left -= n
	return len(p), nil
}
