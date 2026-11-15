package notifications

import (
	"context"
	"testing"
	"time"
)

func TestOpenProblems(t *testing.T) {
	r := newRig(t)
	r.sender.fail = true
	r.tick(0, silent()) // machine.silent critical, pending
	r.sender.fail = false
	// An open disk.full that escalated counts once, at its latest severity; a resolved one and a
	// test don't count; an event this hub doesn't know still does.
	r.exec(`INSERT INTO notifications (machine_id, event, severity, status, message, created_at, updated_at) VALUES
		(1, 'disk.full', 'warning', 'pending', 'x', 1, 1), (1, 'disk.full.escalated', 'critical', 'pending', 'x', 2, 2),
		(NULL, 'backup.failed', 'critical', 'pending', 'x', 1, 1), (NULL, 'backup.failed.resolved', 'critical', 'pending', 'x', 2, 2),
		(NULL, 'notification.test', 'info', 'pending', 'x', 1, 1), (1, 'logs.errors', 'info', 'pending', 'x', 1, 1)`)
	got, err := r.engine.store.OpenProblems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, c := range got {
		count[c.Event+" "+c.Severity] = c.Count
	}
	want := map[string]int{"machine.silent critical": 1, "disk.full critical": 1, "disk.full warning": 0, "logs.errors info": 1, "logs.errors critical": 0,
		"backup.failed critical": 0, "channel.failing critical": 0}
	for k, v := range want {
		if n, ok := count[k]; !ok || n != v {
			t.Errorf("%s = %d (listed %v), want %d", k, n, ok, v)
		}
	}
	if _, ok := count["notification.test info"]; ok {
		t.Error("a test counts as a problem")
	}
	if len(got) != len(Events())*3 || got[0].Event != "backup.failed" || got[0].Severity != "info" {
		t.Errorf("%d counts, first %+v; want every event at every severity, sorted", len(got), got[0])
	}
	// Resolved, it counts no more.
	r.tick(time.Minute, quiet(time.Minute))
	got, _ = r.engine.store.OpenProblems(context.Background())
	for _, c := range got {
		if c.Event == EventMachineSilent && c.Count != 0 {
			t.Errorf("after the resolve %+v", c)
		}
	}
}
