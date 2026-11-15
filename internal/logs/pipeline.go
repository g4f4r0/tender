package logs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/g4f4r0/tender/internal/logs/sinks"
)

// The limits of one batch: a batch goes to the buffer once it has maxBatchRecords records or
// maxBatchBytes of them, or batchWait after its first record, whichever comes first.
const (
	maxBatchRecords = 1000
	maxBatchBytes   = 1 << 20
)

// batchWait is a variable so tests can wait less.
var batchWait = 5 * time.Second

// The waits after failed sends: from minBackoff, doubling up to maxBackoff. Variables so tests
// can wait less.
var (
	minBackoff = time.Second
	maxBackoff = time.Minute
)

// sendGrace is how long a send that is under way may go on once the daemon stops.
const sendGrace = 5 * time.Second

// pipeline sends one sink's logs: its sources feed a batcher, which writes each batch to the
// on-disk buffer and then saves the sources' cursors; a sender sends the buffer's batches, oldest
// first, and deletes each one the sink took.
type pipeline struct {
	sink    DaemonSink
	dir     string
	sender  sinks.Sender
	spool   *spool
	sources []source
	log     *slog.Logger
	changed func() // asks for a report soon: sending started or stopped failing, or records were dropped
	now     func() time.Time

	mu     sync.Mutex
	status sinkStatus

	stop func() // stops the pipeline and waits for it
}

// sinkStatus is what a pipeline reports, kept in status.json so it outlives a restart.
type sinkStatus struct {
	RecordsSent    int64      `json:"records_sent"`
	RecordsDropped int64      `json:"records_dropped"`
	LastSentAt     *time.Time `json:"last_sent_at"`
	LastError      *string    `json:"last_error"`
	FailingSince   *time.Time `json:"failing_since"`
	LastDroppedAt  *time.Time `json:"last_dropped_at"`
}

func (p *pipeline) cursorsFile() string { return filepath.Join(p.dir, "cursors.json") }
func (p *pipeline) statusFile() string  { return filepath.Join(p.dir, "status.json") }

// load reads the saved cursors and status. A sink seen for the first time starts now.
func (p *pipeline) load() (cursors, error) {
	var cur cursors
	if err := readJSON(p.statusFile(), &p.status); err != nil {
		p.log.Warn("the saved log sink status can't be read, so its counters start again", "sink", p.sink.Name, "error", err)
	}
	if err := readJSON(p.cursorsFile(), &cur); err != nil || cur.Since.IsZero() {
		if err != nil {
			p.log.Warn("the saved log cursors can't be read, so the sink gets the logs from now", "sink", p.sink.Name, "error", err)
		}
		cur = cursors{Since: p.now().UTC()}
		if err := writeJSON(p.cursorsFile(), cur); err != nil {
			return cur, err
		}
	}
	return cur, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFile(path, b)
}

// start runs the pipeline until ctx ends or stop is called.
func (p *pipeline) start(ctx context.Context) error {
	cur, err := p.load()
	if err != nil {
		return err
	}
	srcCtx, stopSources := context.WithCancel(ctx)
	items := make(chan item, maxBatchRecords)
	var sources sync.WaitGroup
	for _, src := range p.sources {
		sources.Add(1)
		go func() {
			defer sources.Done()
			src.run(srcCtx, cur.clone(), items)
		}()
	}
	go func() {
		sources.Wait()
		close(items)
	}()
	batched := make(chan struct{})
	go func() {
		defer close(batched)
		p.batch(items, cur)
	}()

	sendCtx, cancelSend := context.WithCancel(context.Background())
	stopSending := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		p.send(sendCtx, stopSending)
	}()

	var once sync.Once
	p.stop = func() {
		once.Do(func() {
			stopSources()
			<-batched
			close(stopSending)
			t := time.AfterFunc(sendGrace, cancelSend)
			<-sent
			t.Stop()
			cancelSend()
		})
	}
	return nil
}

// batch collects records into batches and writes each to the buffer, then saves the cursors
// past it. It returns once in is closed, after writing what it holds.
func (p *pipeline) batch(in <-chan item, cur cursors) {
	var batch []sinks.Record
	size := 0
	var timer *time.Timer
	var due <-chan time.Time
	flush := func() {
		if timer != nil {
			timer.Stop()
			timer, due = nil, nil
		}
		if len(batch) == 0 {
			return
		}
		dropped, err := p.spool.add(batch)
		if err != nil {
			p.log.Error("can't write logs to the buffer or make room in it", "sink", p.sink.Name, "records_dropped", dropped, "error", err)
		}
		if dropped > 0 {
			p.dropped(dropped, "the buffer is full")
		}
		if err := writeJSON(p.cursorsFile(), cur); err != nil {
			p.log.Error("can't save the log cursors; a restart may send some logs again", "sink", p.sink.Name, "error", err)
		}
		batch, size = nil, 0
	}
	for {
		select {
		case it, ok := <-in:
			if !ok {
				flush()
				return
			}
			b, _ := json.Marshal(it.rec)
			if len(batch) > 0 && size+len(b) > maxBatchBytes {
				flush()
			}
			cur.apply(it)
			batch = append(batch, it.rec)
			size += len(b) + 1
			if len(batch) >= maxBatchRecords {
				flush()
			} else if timer == nil {
				timer = time.NewTimer(batchWait)
				due = timer.C
			}
		case <-due:
			timer, due = nil, nil
			flush()
		}
	}
}

// send sends the buffer's batches, oldest first, until stop is closed. A batch the sink refuses
// for good is dropped; any other failure is tried again after a backoff.
func (p *pipeline) send(ctx context.Context, stop <-chan struct{}) {
	backoff := time.Duration(0)
	for {
		select {
		case <-stop:
			return
		default:
		}
		seq, batch, unreadable, ok := p.spool.oldest()
		if unreadable > 0 {
			p.dropped(unreadable, "a buffered batch couldn't be read")
		}
		if !ok {
			select {
			case <-p.spool.ready:
				continue
			case <-stop:
				return
			}
		}
		err := p.sender.Send(ctx, batch)
		var se *sinks.Error
		switch {
		case err == nil:
			p.spool.done(seq)
			p.sent(len(batch))
			backoff = 0
			continue
		case ctx.Err() != nil: // the daemon is stopping: the batch stays for the next start
			p.spool.release(seq)
			return
		case errors.As(err, &se) && se.Permanent:
			p.spool.done(seq)
			p.failed(err)
			p.dropped(len(batch), "the sink refused them")
			continue
		}
		p.spool.release(seq)
		p.failed(err)
		backoff = min(max(2*backoff, minBackoff), maxBackoff)
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		case <-stop:
			t.Stop()
			return
		}
	}
}

func (p *pipeline) saveStatus() {
	if err := writeJSON(p.statusFile(), p.status); err != nil {
		p.log.Error("can't save the log sink's counters", "sink", p.sink.Name, "error", err)
	}
}

func (p *pipeline) sent(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now().UTC()
	p.status.RecordsSent += int64(n)
	p.status.LastSentAt = &now
	if p.status.FailingSince != nil {
		p.log.Info("sending logs works again", "sink", p.sink.Name)
		p.status.FailingSince, p.status.LastError = nil, nil
		p.changed()
	}
	p.saveStatus()
}

func (p *pipeline) failed(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	msg := err.Error()
	p.status.LastError = &msg
	if p.status.FailingSince == nil {
		now := p.now().UTC()
		p.status.FailingSince = &now
		p.log.Warn("can't send logs; the daemon keeps them in its buffer and tries again", "sink", p.sink.Name, "error", msg)
		p.changed()
	}
	p.saveStatus()
}

func (p *pipeline) dropped(n int, why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now().UTC()
	if p.status.LastDroppedAt == nil || (p.status.LastSentAt != nil && p.status.LastSentAt.After(*p.status.LastDroppedAt)) {
		p.log.Warn("dropping logs", "sink", p.sink.Name, "records", n, "why", why)
	}
	p.status.RecordsDropped += int64(n)
	p.status.LastDroppedAt = &now
	p.changed()
	p.saveStatus()
}

// report is the pipeline's status for the hub.
func (p *pipeline) report() StatusInput {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.status
	return StatusInput{SinkID: p.sink.ID, RecordsSent: s.RecordsSent, RecordsDropped: s.RecordsDropped, LastSentAt: s.LastSentAt,
		LastError: s.LastError, FailingSince: s.FailingSince, LastDroppedAt: s.LastDroppedAt}
}
