package logs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/g4f4r0/tender/internal/logs/sinks"
)

// BufferBytes is the most a sink's on-disk buffer holds on each machine.
const BufferBytes = 64 << 20

// spool is a sink's on-disk buffer: batches waiting to be sent, one file each, oldest first.
// A file is named <seq>-<records>.json, so the buffer knows its size and record count without
// reading the files. When it holds more than max bytes, it drops the oldest batches.
type spool struct {
	dir string
	max int64

	mu       sync.Mutex
	files    []spoolFile
	size     int64
	next     uint64
	inflight uint64 // the batch being sent, which is never dropped; 0 for none
	ready    chan struct{}
}

type spoolFile struct {
	seq     uint64
	records int
	size    int64
}

func (f spoolFile) name() string { return fmt.Sprintf("%020d-%d.json", f.seq, f.records) }

// openSpool opens the buffer in dir, making dir if it isn't there, and deletes any batch a crash
// left half written.
func openSpool(dir string, limit int64) (*spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	s := &spool{dir: dir, max: limit, next: 1, ready: make(chan struct{}, 1)}
	for _, e := range entries {
		name := e.Name()
		seqText, rest, ok := strings.Cut(strings.TrimSuffix(name, ".json"), "-")
		seq, err1 := strconv.ParseUint(seqText, 10, 64)
		records, err2 := strconv.Atoi(rest)
		info, err3 := e.Info()
		if !ok || !strings.HasSuffix(name, ".json") || err1 != nil || err2 != nil || err3 != nil {
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		s.files = append(s.files, spoolFile{seq: seq, records: records, size: info.Size()})
		s.size += info.Size()
		s.next = max(s.next, seq+1)
	}
	sort.Slice(s.files, func(i, j int) bool { return s.files[i].seq < s.files[j].seq })
	if len(s.files) > 0 {
		s.ready <- struct{}{}
	}
	return s, nil
}

// add writes a batch to the buffer, then drops the oldest batches while it holds more than max,
// keeping the newest and the one being sent. It returns how many records are lost: the batch's
// own when it can't be written, and those of each old batch it really removed. When removing an
// old batch fails, the buffer keeps it, and the next add tries again.
func (s *spool) add(batch []sinks.Record) (int, error) {
	body, err := json.Marshal(batch)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	f := spoolFile{seq: s.next, records: len(batch), size: int64(len(body))}
	s.next++
	s.mu.Unlock()

	if err := writeFile(filepath.Join(s.dir, f.name()), body); err != nil {
		return len(batch), err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = append(s.files, f)
	s.size += f.size
	dropped := 0
	for i := 0; s.size > s.max && i < len(s.files)-1; {
		old := s.files[i]
		if old.seq == s.inflight {
			i++
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, old.name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return dropped, err
		}
		s.files = append(s.files[:i], s.files[i+1:]...)
		s.size -= old.size
		dropped += old.records
	}
	select {
	case s.ready <- struct{}{}:
	default:
	}
	return dropped, nil
}

// writeFile writes body to path through a temporary file, synced, so a crash leaves the whole
// file or none of it.
func writeFile(path string, body []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// oldest returns the oldest batch and marks it as being sent. ok is false when the buffer is
// empty. A batch that can't be read is deleted and its records counted in dropped.
func (s *spool) oldest() (seq uint64, batch []sinks.Record, dropped int, ok bool) {
	for {
		s.mu.Lock()
		if len(s.files) == 0 {
			s.mu.Unlock()
			return 0, nil, dropped, false
		}
		f := s.files[0]
		s.inflight = f.seq
		s.mu.Unlock()

		body, err := os.ReadFile(filepath.Join(s.dir, f.name()))
		if err == nil {
			batch = nil
			if err = json.Unmarshal(body, &batch); err == nil {
				return f.seq, batch, dropped, true
			}
		}
		dropped += f.records
		s.done(f.seq)
	}
}

// done deletes the batch seq, once it was sent or given up on.
func (s *spool) done(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight == seq {
		s.inflight = 0
	}
	for i, f := range s.files {
		if f.seq == seq {
			_ = os.Remove(filepath.Join(s.dir, f.name()))
			s.files = append(s.files[:i], s.files[i+1:]...)
			s.size -= f.size
			return
		}
	}
}

// release says the batch seq is no longer being sent, so it may be dropped again.
func (s *spool) release(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight == seq {
		s.inflight = 0
	}
}

// bytes is how much the buffer holds.
func (s *spool) bytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}
