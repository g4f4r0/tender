package logs

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A line over the limit comes cut, marked, and the lines after it still come.
func TestReadLinesCutsLongLines(t *testing.T) {
	in := strings.Repeat("a", 200<<10) + "\nshort\n" + strings.Repeat("b", 50) + "\nlast"
	var got []string
	var cuts []bool
	err := readLines(strings.NewReader(in), 1000, func(l []byte, cut bool) bool {
		got, cuts = append(got, string(l)), append(cuts, cut)
		return true
	})
	if err != nil || len(got) != 4 || len(got[0]) != 1000 || !cuts[0] || got[1] != "short" || cuts[1] || got[3] != "last" {
		t.Errorf("lines %d, cuts %v, err %v, second %q", len(got), cuts, err, got[min(1, len(got)-1)])
	}
	if m := message(strings.Repeat("é", maxMessageBytes), false); len(m) > maxMessageBytes || !strings.HasSuffix(m, truncatedMark) {
		t.Errorf("message of %d bytes, ends %q", len(m), m[len(m)-20:])
	}
}

// A journal entry longer than the daemon reads comes cut, with its cursor, and the journal goes
// on: journalctl isn't restarted over it.
func TestJournalSourceLongEntry(t *testing.T) {
	dir := t.TempDir()
	long := filepath.Join(dir, "long.json")
	entry := `{"__CURSOR":"7","__REALTIME_TIMESTAMP":"1794744000000007","_SYSTEMD_UNIT":"big.service","PRIORITY":"3","MESSAGE":"` +
		strings.Repeat("x", journalLineBytes+10) + `"}` + "\n" +
		`{"__CURSOR":"8","__REALTIME_TIMESTAMP":"1794744000000008","_SYSTEMD_UNIT":"nginx.service","PRIORITY":"6","MESSAGE":"after"}` + "\n"
	if err := os.WriteFile(long, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "journalctl")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho x >>"+args+"\ncat "+long+"\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan item, 10)
	go journalSource{bin: bin, filter: JournalSource{MinPriority: "debug"}, log: quiet}.run(ctx, cursors{Since: time.Unix(1, 0)}, out)
	got := collect(out, 2, 10*time.Second)
	if len(got) != 2 {
		t.Fatalf("got %d entries", len(got))
	}
	first := got[0]
	if first.journal != "7" || first.rec.Unit != "big.service" || first.rec.Priority != "err" || len(first.rec.Message) > maxMessageBytes ||
		!strings.HasSuffix(first.rec.Message, truncatedMark) || !strings.HasPrefix(first.rec.Message, "xxxx") {
		t.Errorf("long entry: cursor %q unit %q priority %q, %d bytes", first.journal, first.rec.Unit, first.rec.Priority, len(first.rec.Message))
	}
	if got[1].rec.Message != "after" || got[1].journal != "8" {
		t.Errorf("then %+v", got[1].rec)
	}
	if b, _ := os.ReadFile(args); strings.Count(string(b), "x") != 1 {
		t.Errorf("journalctl started %d times", strings.Count(string(b), "x"))
	}
}

// A container line longer than a message comes cut and marked, split over frames as it may be,
// and the next line comes whole.
func TestFramesCutLongLines(t *testing.T) {
	var stream bytes.Buffer
	frame := func(kind byte, s string) {
		var h [8]byte
		h[0] = kind
		binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
		stream.Write(h[:])
		stream.WriteString(s)
	}
	long := "2026-11-15T12:00:00Z " + strings.Repeat("y", 3*maxMessageBytes) + "\n"
	for i := 0; i < len(long); i += 10000 {
		frame(1, long[i:min(i+10000, len(long))])
	}
	frame(2, "2026-11-15T12:00:01Z next\n")
	var got []string
	var cuts []bool
	if err := frames(&stream, func(l string, _, cut bool) bool { got, cuts = append(got, l), append(cuts, cut); return true }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !cuts[0] || len(got[0]) != dockerLineBytes || got[1] != "2026-11-15T12:00:01Z next" || cuts[1] {
		t.Errorf("got %d lines, cuts %v", len(got), cuts)
	}
}
