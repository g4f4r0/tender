package notifications

import (
	"context"
	"testing"
	"time"
)

func TestOpenAlerts(t *testing.T) {
	r := newRig(t)
	r.sender.fail = true
	r.tick(0, silent()) // machine_silent critical, pending
	r.sender.fail = false
	_, err := r.db.Write.Exec(`INSERT INTO notifications (machine_id, type, severity, status, content, created_at, updated_at)
		VALUES (1, 'disk_full', 'warning', 'sent', '{}', 1, 1), (1, 'disk_full', 'warning', 'resolved', '{}', 1, 1),
		       (1, 'log_errors', 'info', 'sent', '{}', 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.engine.store.OpenAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, c := range got {
		count[c.Type+" "+c.Severity] = c.Count
	}
	want := map[string]int{"machine_silent critical": 1, "disk_full warning": 1, "log_errors info": 1, "log_errors critical": 0,
		"backup_failed warning": 0, "channel_failing critical": 0}
	for k, v := range want {
		if n, ok := count[k]; !ok || n != v {
			t.Errorf("%s = %d (listed %v), want %d", k, n, ok, v)
		}
	}
	if len(got) != (len(typeText)+1)*3 || got[0].Type != "backup_failed" || got[0].Severity != "info" {
		t.Errorf("%d counts, first %+v; want every type at every severity, sorted", len(got), got[0])
	}
	// Resolved, it counts no more.
	r.tick(time.Minute, quiet(time.Minute))
	got, _ = r.engine.store.OpenAlerts(context.Background())
	for _, c := range got {
		if c.Type == TypeMachineSilent && c.Count != 0 {
			t.Errorf("after the resolve %+v", c)
		}
	}
}
