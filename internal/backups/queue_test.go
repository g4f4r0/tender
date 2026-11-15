package backups

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueueWritesAtomicallyAndListsOldestFirst(t *testing.T) {
	q := pendingQueue{dir: filepath.Join(t.TempDir(), "queue")}
	now := time.Date(2026, 11, 14, 4, 0, 0, 0, time.UTC)
	// Queued out of clock order on purpose: the name follows the queue time.
	for i, typ := range []string{TypeCheck, TypeBackup, TypeCleanup} {
		offset := []int{2, 0, 1}[i]
		if err := q.add(&daemonRun{Type: typ, Status: StatusCompleted, CreatedAt: now, PostKey: "k"}, now.Add(time.Duration(offset)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	names, err := q.names()
	if err != nil || len(names) != 3 {
		t.Fatalf("names = %v, %v", names, err)
	}
	var order []string
	for _, n := range names {
		r, err := q.read(n)
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, r.Type)
	}
	if strings.Join(order, ",") != "backup,cleanup,check" {
		t.Errorf("order = %v, want oldest first", order)
	}
	entries, _ := os.ReadDir(q.dir)
	if len(entries) != 3 {
		t.Errorf("%d files in the queue folder, want 3 and no temp files", len(entries))
	}
	if info, _ := os.Stat(filepath.Join(q.dir, names[0])); info.Mode().Perm() != 0o600 {
		t.Errorf("queue file mode = %v, want 0600", info.Mode().Perm())
	}
	if err := q.remove(names[0]); err != nil {
		t.Fatal(err)
	}
	if names, _ := q.names(); len(names) != 2 {
		t.Errorf("names after remove = %v", names)
	}
}

func TestQueueNeverCreatedIsEmpty(t *testing.T) {
	q := pendingQueue{dir: filepath.Join(t.TempDir(), "none")}
	if names, err := q.names(); err != nil || len(names) != 0 {
		t.Errorf("names = %v, %v", names, err)
	}
}

func TestReplayDropsUnreadableItems(t *testing.T) {
	h := newHub(t)
	a, _ := testDaemon(t, h)
	if err := os.MkdirAll(a.queue.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.queue.dir, "00000000000000000001-000001.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.replay(context.Background())
	if names, _ := a.queue.names(); len(names) != 0 {
		t.Errorf("queue = %v, want the corrupt item dropped", names)
	}
	if got := h.seen(); len(got) != 0 {
		t.Errorf("requests = %v, want none", routes(got))
	}
}
