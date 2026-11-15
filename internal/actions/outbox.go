package actions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/client"
)

// What a report says when the daemon can't say how an action ended.
const (
	stoppedWhileRunning = "The daemon stopped while it ran this, so it can't say how it ended."
	lostLogs            = "The daemon restarted before the hub had these logs, and it never keeps logs on disk. Ask for them again."
	notClaimed          = "The daemon could not confirm with the hub that it may run this, so it did not run it."
)

// sent is one report the hub doesn't have yet. Patch is nil while the action runs. Every try sends
// it with Key, so the hub takes it once.
type sent struct {
	ID    int64  `json:"id"`
	Key   string `json:"key"`
	Type  string `json:"type"`
	Patch *Patch `json:"patch,omitempty"`
}

func (d *Daemon) outbox() string {
	if d.StateDir == "" {
		return ""
	}
	return filepath.Join(d.StateDir, "actions")
}

// keep holds r until the hub has it, in memory and in the state folder. A logs action's output
// stays in memory only: logs can hold secrets, so they never go to disk.
func (d *Daemon) keep(r *sent) {
	if d.waiting == nil {
		d.waiting = map[int64]*sent{}
	}
	d.waiting[r.ID] = r
	dir := d.outbox()
	if dir == "" {
		return
	}
	disk := *r
	if r.Type == TypeLogs && r.Patch != nil {
		disk.Patch = &Patch{Status: r.Patch.Status}
	}
	b, err := json.Marshal(disk)
	if err == nil {
		err = writeFile(dir, strconv.FormatInt(r.ID, 10)+".json", b)
	}
	if err != nil {
		d.Log.Warn("could not keep an action's report on disk; it is lost if the daemon stops before the hub has it", "id", r.ID, "error", err)
	}
}

// writeFile writes name in dir whole or not at all, readable only by its owner.
func writeFile(dir, name string, b []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, name+".*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, name))
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

// drop forgets r: the hub has it, or refused it.
func (d *Daemon) drop(r *sent) {
	delete(d.waiting, r.ID)
	if dir := d.outbox(); dir != "" {
		if err := os.Remove(filepath.Join(dir, strconv.FormatInt(r.ID, 10)+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			d.Log.Warn("could not remove an action's report from disk", "id", r.ID, "error", err)
		}
	}
}

// load reads the reports a daemon before this one left. One without a result was running when
// that daemon stopped. A logs action's is sent as failed, since its logs weren't kept.
func (d *Daemon) load() {
	dir := d.outbox()
	if dir == "" {
		return
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, f := range files {
		var r sent
		b, err := os.ReadFile(f)
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err != nil || !strings.HasSuffix(f, ".json") || filepath.Base(f) != strconv.FormatInt(r.ID, 10)+".json" {
			d.Log.Warn("removed a file that isn't an action's report from the daemon's state folder", "file", f, "error", err)
			_ = os.Remove(f)
			continue
		}
		var out string
		switch {
		case r.Patch == nil:
			out = stoppedWhileRunning
		case r.Type == TypeLogs:
			out = lostLogs
		}
		if out != "" {
			r.Patch = &Patch{Status: StatusFailed, Output: &out}
		}
		d.keep(&r)
	}
}

// deliver sends the waiting reports, oldest first, until the hub can't take one.
func (d *Daemon) deliver(ctx context.Context) {
	ids := make([]int64, 0, len(d.waiting))
	for id, r := range d.waiting {
		if r.Patch != nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if !d.send(ctx, d.waiting[id]) {
			return
		}
	}
}

// send tells the hub how an action ended, and says whether the report is done with: the hub took
// it, or refused it for good. Otherwise it waits and goes again. A stopping daemon tries once more,
// briefly; the report waits on disk for the next one.
func (d *Daemon) send(ctx context.Context, r *sent) bool {
	sctx, cancel := ctx, context.CancelFunc(func() {})
	if ctx.Err() != nil {
		sctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	}
	defer cancel()
	err := d.Hub.Do(sctx, http.MethodPatch, d.path("/"+strconv.FormatInt(r.ID, 10)), r.Key, r.Patch, nil)
	switch {
	case err == nil:
		d.sendErr = ""
	case !client.Retryable(err):
		d.Log.Warn("the hub refused how an action ended", "id", r.ID, "type", r.Type, "error", err)
	default:
		if err.Error() != d.sendErr {
			d.Log.Warn("could not tell the hub how an action ended; it is sent again until the hub has it", "id", r.ID, "type", r.Type, "error", err)
			d.sendErr = err.Error()
		}
		return false
	}
	d.drop(r)
	return true
}
