package logs

import (
	"context"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/logs/sinks"
)

// starts is a source that records the cursors it started from.
type starts struct {
	k   string
	got chan cursors
}

func (s starts) kind() string { return s.k }
func (s starts) run(ctx context.Context, start cursors, _ chan<- item) {
	s.got <- start
	<-ctx.Done()
}

// A source added to a sink later starts when it is added, not when the sink was; one turned
// off and on again starts again then. The other source keeps its place.
func TestSourceStartsWhenAdded(t *testing.T) {
	dir := t.TempDir()
	at := func(when time.Time, src ...source) cursors {
		t.Helper()
		sp, err := openSpool(dir+"/buffer", BufferBytes)
		if err != nil {
			t.Fatal(err)
		}
		p := &pipeline{sink: DaemonSink{ID: 1, Name: "test"}, dir: dir, sender: &fakeSink{}, spool: sp, sources: src, log: quiet,
			changed: func() {}, now: func() time.Time { return when }}
		if err := p.start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer p.stop()
		return <-src[0].(starts).got
	}
	t1, t2, t3, t4 := time.Unix(1000, 0).UTC(), time.Unix(2000, 0).UTC(), time.Unix(3000, 0).UTC(), time.Unix(4000, 0).UTC()
	journal := func() starts { return starts{sinks.SourceJournal, make(chan cursors, 1)} }
	services := func() starts { return starts{sinks.SourceService, make(chan cursors, 1)} }

	if c := at(t1, journal()); !c.JournalSince.Equal(t1) || !c.ServicesSince.IsZero() {
		t.Errorf("first start: %+v", c)
	}
	if c := at(t2, services(), journal()); !c.ServicesSince.Equal(t2) || !c.JournalSince.Equal(t1) {
		t.Errorf("services added at t2: %+v, want services from t2 and the journal still from t1", c)
	}
	if c := at(t3, services()); !c.JournalSince.IsZero() || !c.ServicesSince.Equal(t2) {
		t.Errorf("journal off: %+v", c)
	}
	if c := at(t4, journal(), services()); !c.JournalSince.Equal(t4) {
		t.Errorf("journal on again: %+v, want it from t4", c)
	}
}

// A container's cursor is its own: a container re-created under the same name starts from the
// source's start, not from the old container's last line; old cursors go after cursorsKept.
func TestContainerCursorsByID(t *testing.T) {
	now := time.Unix(10_000_000, 0).UTC()
	var c cursors
	c.begin(false, true, now)
	c.apply(item{container: "old", rec: sinks.Record{Source: sinks.SourceService, Service: "web", Time: now.Add(time.Minute)}})
	c.apply(item{container: "new", rec: sinks.Record{Source: sinks.SourceService, Service: "web", Time: now.Add(time.Second)}})
	if !c.Services["old"].Equal(now.Add(time.Minute)) || !c.Services["new"].Equal(now.Add(time.Second)) {
		t.Errorf("cursors %v", c.Services)
	}
	c.begin(false, true, now.Add(cursorsKept+2*time.Minute))
	if len(c.Services) != 0 {
		t.Errorf("after %s: %v", cursorsKept, c.Services)
	}
}
