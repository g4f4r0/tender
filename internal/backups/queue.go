package backups

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/g4f4r0/tender/internal/client"
)

var queued atomic.Int64

// pendingQueue keeps finished runs the hub couldn't take yet, one JSON file each. File names
// start with the time they were queued, so sorting them gives oldest first.
type pendingQueue struct {
	dir string
}

func (q pendingQueue) add(r *daemonRun, now time.Time) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%020d-%06d.json", now.UnixNano(), queued.Add(1)) // the counter orders items queued in the same instant
	return writeFileAtomic(filepath.Join(q.dir, name), b)
}

// names lists the queued files, oldest first.
func (q pendingQueue) names() ([]string, error) {
	entries, err := os.ReadDir(q.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (q pendingQueue) read(name string) (*daemonRun, error) {
	b, err := os.ReadFile(filepath.Join(q.dir, name))
	if err != nil {
		return nil, err
	}
	var r daemonRun
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (q pendingQueue) remove(name string) error {
	return os.Remove(filepath.Join(q.dir, name))
}

// replay reports queued runs oldest first through the same path as a live run. It stops at the
// first retryable failure (the hub is still down) and drops items the hub rejects for good.
func (a *Daemon) replay(ctx context.Context) {
	names, err := a.queue.names()
	if err != nil {
		a.Log.Error("read the report queue", "error", err)
		return
	}
	for _, name := range names {
		r, err := a.queue.read(name)
		if err != nil {
			a.Log.Error("dropping unreadable queue item", "item", name, "error", err)
			_ = a.queue.remove(name)
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, time.Minute)
		err = a.report(rctx, r)
		cancel()
		switch {
		case err == nil:
			a.Log.Info("reported a queued run", "type", r.Type, "status", r.Status, "created_at", r.CreatedAt)
		case ctx.Err() != nil:
			return
		case client.Retryable(err):
			a.Log.Warn("hub still unreachable, keeping the queue", "pending", len(names), "error", err)
			return
		default:
			a.Log.Error("dropping a queued run the hub rejected", "item", name, "error", err)
		}
		if err := a.queue.remove(name); err != nil {
			a.Log.Error("remove queue item", "item", name, "error", err)
		}
	}
}

// writeFileAtomic writes path through a temp file in the same folder: write, fsync, rename,
// then fsync the folder, so a crash leaves the old file or the whole new one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
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
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
