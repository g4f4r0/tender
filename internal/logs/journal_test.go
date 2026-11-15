package logs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeJournalctl writes a journalctl that logs its arguments to args, prints entries 1 to 5 after
// its --after-cursor, as journalctl -o json does, and then follows: it waits, or exits when
// exits is true.
func fakeJournalctl(t *testing.T, exits bool) (bin, args string) {
	t.Helper()
	dir := t.TempDir()
	args = filepath.Join(dir, "args")
	tail := "exec sleep 60"
	if exits {
		tail = "exit 1"
	}
	script := `#!/bin/sh
echo "$*" >>` + args + `
after=0
for a in "$@"; do case $a in --after-cursor=*) after=${a#--after-cursor=};; esac; done
i=$((after + 1))
while [ $i -le 5 ]; do
  printf '{"__CURSOR":"%s","__REALTIME_TIMESTAMP":"1794744000%06d","_SYSTEMD_UNIT":"nginx.service","PRIORITY":"%s","MESSAGE":"entry %s"}\n' $i $i $((i % 8)) $i
  i=$((i + 1))
done
[ $after -lt 6 ] && echo '{"__CURSOR":"6","SYSLOG_IDENTIFIER":"kernel","MESSAGE":[104,105,255]}'
echo 'not json'
` + tail + "\n"
	bin = filepath.Join(dir, "journalctl")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, args
}

func collect(out <-chan item, n int, wait time.Duration) []item {
	var got []item
	deadline := time.After(wait)
	for len(got) < n {
		select {
		case it := <-out:
			got = append(got, it)
		case <-deadline:
			return got
		}
	}
	return got
}

// The source follows the journal after the saved cursor, with the sink's filters, and turns
// each entry into a record.
func TestJournalSource(t *testing.T) {
	bin, args := fakeJournalctl(t, false)
	j := journalSource{bin: bin, filter: JournalSource{Units: []string{"nginx.service", "ssh.service"}, MinPriority: "warning"}, log: quiet}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan item, 10)
	go j.run(ctx, cursors{Journal: "2", JournalSince: time.Unix(1794744000, 0)}, out)
	got := collect(out, 4, 5*time.Second)
	if len(got) != 4 {
		t.Fatalf("got %d entries", len(got))
	}
	if got[0].journal != "3" || got[0].rec.Message != "entry 3" || got[0].rec.Unit != "nginx.service" || got[0].rec.Priority != "err" ||
		!got[0].rec.Time.Equal(time.UnixMicro(1794744000000003)) || got[0].rec.Source != "journal" {
		t.Errorf("first %+v", got[0])
	}
	if got[1].rec.Priority != "warning" || got[3].rec.Unit != "kernel" || got[3].rec.Message != "hi�" {
		t.Errorf("then %+v and %+v", got[1].rec, got[3].rec)
	}
	b, _ := os.ReadFile(args)
	if a := strings.TrimSpace(string(b)); a != "--follow --no-tail --output=json --no-pager --quiet --after-cursor=2 --unit=nginx.service --unit=ssh.service --priority=warning" {
		t.Errorf("args %q", a)
	}
}

// Without a cursor it starts at the sink's first start; when journalctl stops, it starts again
// after the last entry it sent.
func TestJournalSourceRestarts(t *testing.T) {
	bin, args := fakeJournalctl(t, true)
	j := journalSource{bin: bin, filter: JournalSource{MinPriority: "debug"}, log: quiet}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan item, 20)
	go j.run(ctx, cursors{JournalSince: time.Unix(1794744000, 0)}, out)
	got := collect(out, 6, 5*time.Second)
	if len(got) != 6 {
		t.Fatalf("got %d entries", len(got))
	}
	deadline := time.Now().Add(5 * time.Second)
	var lines []string
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(args)
		if lines = strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(lines) < 2 || lines[0] != "--follow --no-tail --output=json --no-pager --quiet --since=@1794744000" ||
		!strings.HasSuffix(lines[1], "--after-cursor=6") {
		t.Errorf("runs %q", lines)
	}
}

func TestJournalSourceMissing(t *testing.T) {
	j := journalSource{bin: "/nonexistent/journalctl", log: quiet}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { j.run(ctx, cursors{}, make(chan item)); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("run didn't return when ctx ended")
	}
}
