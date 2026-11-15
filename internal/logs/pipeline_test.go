package logs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/logs/sinks"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// counting is a journal of n entries, cursors "1" to "n", that it sends after the saved cursor,
// as journalctl --after-cursor does, then waits.
type counting struct {
	n     int
	pause chan struct{} // when set, it waits here after each entry
}

func (counting) kind() string { return sinks.SourceJournal }

func (c counting) run(ctx context.Context, start cursors, out chan<- item) {
	from, _ := strconv.Atoi(start.Journal)
	for i := from + 1; i <= c.n; i++ {
		it := item{journal: strconv.Itoa(i), rec: sinks.Record{Time: time.Unix(int64(i), 0).UTC(), Source: sinks.SourceJournal,
			Unit: "x.service", Priority: "info", Message: "entry " + strconv.Itoa(i)}}
		if !send(ctx, out, it) {
			return
		}
		if c.pause != nil {
			select {
			case <-c.pause:
			case <-ctx.Done():
				return
			}
		}
	}
	<-ctx.Done()
}

// fakeSink keeps every batch, and fails while err is set.
type fakeSink struct {
	mu      sync.Mutex
	batches [][]sinks.Record
	err     error
	calls   int
}

func (f *fakeSink) Send(_ context.Context, records []sinks.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.batches = append(f.batches, append([]sinks.Record(nil), records...))
	return nil
}

func (f *fakeSink) records() []sinks.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sinks.Record
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

func (f *fakeSink) sizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, b := range f.batches {
		out = append(out, len(b))
	}
	return out
}

func (f *fakeSink) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func newTestPipeline(t *testing.T, dir string, sink *fakeSink, limit int64, src ...source) *pipeline {
	t.Helper()
	return startPipeline(t, dir, sink, limit, func() {}, src...)
}

func startPipeline(t *testing.T, dir string, sink *fakeSink, limit int64, changed func(), src ...source) *pipeline {
	t.Helper()
	sp, err := openSpool(dir+"/buffer", limit)
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline{sink: DaemonSink{ID: 1, Name: "test"}, dir: dir, sender: sink, spool: sp, sources: src, log: quiet,
		changed: changed, now: time.Now}
	if err := p.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fast(t *testing.T) {
	old, oldMin, oldMax := batchWait, minBackoff, maxBackoff
	batchWait, minBackoff, maxBackoff = 50*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { batchWait, minBackoff, maxBackoff = old, oldMin, oldMax })
}

// A batch holds at most 1000 records, and a smaller one goes after the wait.
func TestBatches(t *testing.T) {
	fast(t)
	sink := &fakeSink{}
	p := newTestPipeline(t, t.TempDir(), sink, BufferBytes, counting{n: 2500})
	defer p.stop()
	eventually(t, "2500 records", func() bool { return len(sink.records()) == 2500 })
	sizes := sink.sizes()
	if fmt.Sprint(sizes) != "[1000 1000 500]" {
		t.Errorf("batches %v", sizes)
	}
	if r := p.report(); r.RecordsSent != 2500 || r.LastSentAt == nil || r.FailingSince != nil {
		t.Errorf("report %+v", r)
	}
}

// A batch holds at most 1 MiB.
func TestBatchBytes(t *testing.T) {
	fast(t)
	sink := &fakeSink{}
	big := strings.Repeat("x", 60<<10)
	src := sourceFunc(func(ctx context.Context, _ cursors, out chan<- item) {
		for i := 0; i < 40; i++ {
			send(ctx, out, item{rec: sinks.Record{Source: sinks.SourceJournal, Priority: "info", Message: big}})
		}
		<-ctx.Done()
	})
	p := newTestPipeline(t, t.TempDir(), sink, BufferBytes, src)
	defer p.stop()
	eventually(t, "40 records", func() bool { return len(sink.records()) == 40 })
	for _, n := range sink.sizes() {
		if n*(60<<10) > maxBatchBytes {
			t.Errorf("a batch of %d records is over 1 MiB", n)
		}
	}
}

type sourceFunc func(ctx context.Context, start cursors, out chan<- item)

func (sourceFunc) kind() string { return sinks.SourceJournal }

func (f sourceFunc) run(ctx context.Context, start cursors, out chan<- item) { f(ctx, start, out) }

// A restart resumes after the saved cursor and sends what the buffer held: nothing is lost or
// sent twice, whether the sink was up or down when the daemon stopped.
func TestRestartLosesAndDoublesNothing(t *testing.T) {
	fast(t)
	dir := t.TempDir()
	sink := &fakeSink{}
	pause := make(chan struct{})
	p := newTestPipeline(t, dir, sink, BufferBytes, counting{n: 3000, pause: pause})
	for i := 0; i < 1200; i++ {
		pause <- struct{}{}
	}
	eventually(t, "the first batch", func() bool { return len(sink.records()) >= 1000 })
	sink.setErr(fmt.Errorf("down"))
	for i := 0; i < 300; i++ {
		pause <- struct{}{}
	}
	p.stop() // some records in the buffer, unsent, some in the batcher, unwritten

	sink.setErr(nil)
	p = newTestPipeline(t, dir, sink, BufferBytes, counting{n: 3000})
	defer p.stop()
	eventually(t, "3000 records", func() bool { return len(sink.records()) >= 3000 })
	time.Sleep(100 * time.Millisecond)
	seen := map[string]int{}
	for _, r := range sink.records() {
		seen[r.Message]++
	}
	for i := 1; i <= 3000; i++ {
		if n := seen["entry "+strconv.Itoa(i)]; n != 1 {
			t.Fatalf("entry %d arrived %d times", i, n)
		}
	}
	if r := p.report(); r.RecordsSent != 3000 {
		t.Errorf("records_sent %d after a restart, want the count to go on", r.RecordsSent)
	}
}

// A dead sink fills the buffer, which drops the oldest records and counts them; sending again
// clears the failure, after the drops.
func TestDeadSinkDrops(t *testing.T) {
	fast(t)
	sink := &fakeSink{err: &sinks.Error{Status: 503}}
	var changes int
	var mu sync.Mutex
	p := startPipeline(t, t.TempDir(), sink, 64<<10, func() { mu.Lock(); changes++; mu.Unlock() }, counting{n: 5000})
	defer p.stop()
	eventually(t, "drops", func() bool { return p.report().RecordsDropped > 0 })
	r := p.report()
	if r.FailingSince == nil || r.LastError == nil || r.LastDroppedAt == nil || r.RecordsSent != 0 {
		t.Errorf("report %+v", r)
	}
	if p.spool.bytes() > 64<<10+maxBatchBytes {
		t.Errorf("the buffer holds %d bytes", p.spool.bytes())
	}
	sink.setErr(nil)
	eventually(t, "sending again", func() bool { return p.report().FailingSince == nil })
	r = p.report()
	if r.LastError != nil || r.LastSentAt == nil || !r.LastSentAt.After(*r.LastDroppedAt) && !r.LastSentAt.Equal(*r.LastDroppedAt) {
		t.Errorf("after recovery %+v", r)
	}
	eventually(t, "everything accounted for", func() bool { r := p.report(); return r.RecordsSent+r.RecordsDropped == 5000 })
	mu.Lock()
	defer mu.Unlock()
	if changes == 0 {
		t.Error("no report was asked for")
	}
}

// A batch the sink refuses for good is dropped, so it doesn't block the ones behind it.
func TestPermanentFailureDrops(t *testing.T) {
	fast(t)
	sink := &fakeSink{err: &sinks.Error{Status: 400, Permanent: true}}
	p := newTestPipeline(t, t.TempDir(), sink, BufferBytes, counting{n: 10})
	defer p.stop()
	eventually(t, "a drop", func() bool { return p.report().RecordsDropped == 10 })
	sink.setErr(nil)
	if r := p.report(); r.FailingSince == nil {
		t.Errorf("a refusal is a failure: %+v", r)
	}
}
