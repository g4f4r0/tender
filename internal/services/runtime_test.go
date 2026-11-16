package services

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/client"
)

// fakeRuntime is a runtime that isn't Docker: it lists what it holds and records what it is asked.
type fakeRuntime struct {
	name string
	list []ContainerInput
	did  []string
}

func (f *fakeRuntime) Name() string               { return f.name }
func (f *fakeRuntime) Ping(context.Context) error { return nil }
func (f *fakeRuntime) Stats(context.Context, string) (sample, error) {
	return sample{memory: 64 << 20, cpu: 1, system: 1, cpus: 1}, nil
}
func (f *fakeRuntime) Containers(context.Context) ([]ContainerInput, error) {
	return append([]ContainerInput(nil), f.list...), nil
}
func (f *fakeRuntime) Act(_ context.Context, typ, name string) error {
	f.did = append(f.did, typ+" "+name)
	return nil
}
func (f *fakeRuntime) Logs(_ context.Context, name string, lines int, _ time.Duration) (string, error) {
	f.did = append(f.did, "logs "+name)
	return strings.Repeat("line\n", lines), nil
}

// The daemon reaches services only through its Runtime, and names that runtime in each service.
func TestDaemonUsesItsRuntime(t *testing.T) {
	h := newTestHub(t)
	rt := &fakeRuntime{name: RuntimeDocker, list: []ContainerInput{running("web", "c1")}}
	var logs syncBuffer
	d := NewDaemon(client.New(h.URL, "token-a"), "a", "/nonexistent/docker.sock", slog.New(slog.NewTextHandler(&logs, nil)))
	d.Runtime = rt
	ctx := context.Background()
	s := NewStore(h.db)

	d.reportNow(ctx)
	web := lone(t, s, 1, "web")
	if web.Runtime != RuntimeDocker || web.MemoryBytes == nil || *web.MemoryBytes != 64<<20 {
		t.Fatalf("web = %+v; want it from the fake runtime, with its usage", web)
	}
	if _, err := d.Service(ctx, actRestart, "web", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if out, err := d.Service(ctx, actLogs, "web", "", 2, 0); err != nil || out != "line\nline\n" {
		t.Errorf("logs = %q, %v", out, err)
	}
	if strings.Join(rt.did, ",") != "restart web,logs web" {
		t.Errorf("the runtime was asked %q", rt.did)
	}

	// A runtime this hub doesn't know is refused, and the hub keeps the last list.
	rt.name, rt.list = "systemd", []ContainerInput{running("web", "c2")}
	d.reportNow(ctx)
	if web := lone(t, s, 1, "web"); web.ContainerID != "c1" || web.Runtime != RuntimeDocker {
		t.Errorf("after an unknown runtime web = %+v", web)
	}
	if !strings.Contains(logs.String(), `runtime \"systemd\"; this hub knows only docker`) {
		t.Errorf("the daemon didn't log the refusal:\n%s", logs.String())
	}
}
