package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/logs/sinks"
)

func recs(n int, msg string) []sinks.Record {
	out := make([]sinks.Record, n)
	for i := range out {
		out[i] = sinks.Record{Time: time.Unix(int64(i), 0).UTC(), Source: sinks.SourceJournal, Unit: "x.service", Priority: "info", Message: msg}
	}
	return out
}

func TestSpoolOrderAndReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := openSpool(dir, BufferBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"first", "second"} {
		if dropped, err := s.add(recs(2, m)); err != nil || dropped != 0 {
			t.Fatal(dropped, err)
		}
	}
	// a crash in the middle of a write leaves a .tmp file, which a reopen deletes.
	_ = os.WriteFile(filepath.Join(dir, "00000000000000000009-2.json.tmp"), []byte("[{"), 0o600)
	s, err = openSpool(dir, BufferBytes)
	if err != nil {
		t.Fatal(err)
	}
	seq, batch, _, ok := s.oldest()
	if !ok || batch[0].Message != "first" {
		t.Fatalf("oldest = %v %v", batch, ok)
	}
	s.done(seq)
	if _, batch, _, _ = s.oldest(); batch[0].Message != "second" {
		t.Errorf("then %v", batch)
	}
	if _, err := os.Stat(filepath.Join(dir, "00000000000000000009-2.json.tmp")); !os.IsNotExist(err) {
		t.Error("the half-written batch is still there")
	}
	if dropped, _ := s.add(recs(1, "third")); dropped != 0 {
		t.Error(dropped)
	}
	names, _ := os.ReadDir(dir)
	if len(names) != 2 || !strings.HasPrefix(names[1].Name(), "00000000000000000003-1") {
		t.Errorf("files %v: a reopen goes on numbering after the newest", names)
	}
}

// Past its size, the buffer drops its oldest batches and counts their records, but never the
// one being sent or the one just written.
func TestSpoolDropsOldest(t *testing.T) {
	s, err := openSpool(t.TempDir(), 3100)
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 900)
	if d, _ := s.add(recs(1, "a"+big)); d != 0 {
		t.Fatal(d)
	}
	seq, _, _, _ := s.oldest() // a is being sent
	total := 0
	for _, m := range []string{"b", "c", "d", "e"} {
		d, err := s.add(recs(1, m+big))
		if err != nil {
			t.Fatal(err)
		}
		total += d
	}
	if total != 2 || s.bytes() > 3100 {
		t.Errorf("dropped %d records, holds %d bytes", total, s.bytes())
	}
	s.done(seq)
	var left []string
	for {
		seq, batch, _, ok := s.oldest()
		if !ok {
			break
		}
		left = append(left, batch[0].Message[:1])
		s.done(seq)
	}
	if strings.Join(left, "") != "de" {
		t.Errorf("left %v, want the newest, d and e", left)
	}
}

// A batch that can't be read is deleted and counted as dropped.
func TestSpoolUnreadable(t *testing.T) {
	dir := t.TempDir()
	s, _ := openSpool(dir, BufferBytes)
	_, _ = s.add(recs(3, "a"))
	_, _ = s.add(recs(1, "b"))
	entries, _ := os.ReadDir(dir)
	_ = os.WriteFile(filepath.Join(dir, entries[0].Name()), []byte("not json"), 0o600)
	_, batch, unreadable, ok := s.oldest()
	if !ok || unreadable != 3 || batch[0].Message != "b" {
		t.Errorf("oldest = %v %d %v", batch, unreadable, ok)
	}
}
