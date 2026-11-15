package actions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/g4f4r0/tender/internal/server"
)

// fakeDestinations knows the destination disk, whose secret is disk-secret.
type fakeDestinations struct{}

func (fakeDestinations) Check(_ context.Context, name string) error {
	if name != "disk" {
		return server.NotFound("Destination %q doesn't exist.", name)
	}
	return nil
}

func (f fakeDestinations) Target(ctx context.Context, name string) (json.RawMessage, error) {
	if err := f.Check(ctx, name); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"name":"disk","secret":"disk-secret"}`), nil
}

// fakeBackups records the targets it was handed.
type fakeBackups struct {
	mu      sync.Mutex
	targets []string
	fail    bool
}

func (f *fakeBackups) TestDestination(_ context.Context, target json.RawMessage) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, string(target))
	if f.fail {
		return "", errors.New("wrong password or no key found")
	}
	return "Reached local:/mnt/disk/a.\n", nil
}

func (f *fakeBackups) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.targets...)
}

// A test is queued on the named machine, handed to its daemon with the destination's secrets, run
// by the backups side of the daemon, and reported like any action. People never see the secrets.
func TestDestinationTest(t *testing.T) {
	h := newTestHub(t)
	h.store.Destinations = fakeDestinations{}

	for _, c := range []struct {
		name, path string
		body       any
		want       int
		says       string
	}{
		{"no machine", "/v1/backups/destinations/disk/test", map[string]any{}, 422, "machine is required"},
		{"unknown machine", "/v1/backups/destinations/disk/test", map[string]any{"machine": "zzz"}, 422, `zzz\" doesn't exist`},
		{"unknown destination", "/v1/backups/destinations/nope/test", map[string]any{"machine": "a"}, 404, `nope\" doesn't exist`},
		{"unknown field", "/v1/backups/destinations/disk/test", map[string]any{"machine": "a", "lines": 3}, 400, ""},
		{"test through the actions collection", "/v1/machines/a/actions", map[string]any{"type": "test"}, 422, "POST /v1/backups/destinations/{destination}/test"},
		{"destination on the actions collection", "/v1/machines/a/actions", map[string]any{"type": "test", "destination": "disk"}, 400, ""},
	} {
		code, _, body := h.call(t, "POST", c.path, "", c.body)
		if code != c.want || !strings.Contains(body, c.says) {
			t.Errorf("%s: %d %s, want %d containing %q", c.name, code, body, c.want, c.says)
		}
	}

	code, header, body := h.call(t, "POST", "/v1/backups/destinations/disk/test", "", map[string]any{"machine": "a"})
	var a Action
	if err := json.Unmarshal([]byte(body), &a); err != nil || code != 202 || a.Type != TypeTest || deref(a.Destination) != "disk" ||
		a.Status != StatusPending || header.Get("Location") != "/v1/machines/a/actions/1" || strings.Contains(body, "disk-secret") {
		t.Fatalf("queue a test: %d %s %v", code, body, header)
	}

	backups := &fakeBackups{}
	d, _, stop := startDaemon(t, h.URL, t.TempDir(), &fakeRunner{})
	d.Backups = backups
	eventually(t, "the test to finish", func() bool {
		got, _ := h.store.Get(context.Background(), 1, a.ID)
		return got.Finished()
	})
	stop()
	got, _ := h.store.Get(context.Background(), 1, a.ID)
	if got.Status != StatusCompleted || deref(got.Output) != "Reached local:/mnt/disk/a.\n" {
		t.Errorf("the finished test = %+v", got)
	}
	if seen := backups.seen(); len(seen) != 1 || !strings.Contains(seen[0], "disk-secret") {
		t.Errorf("the daemon was handed %v, want the destination with its secret", seen)
	}
	for _, path := range []string{"/v1/machines/a/actions", "/v1/machines/a/actions/1"} {
		if _, _, body := h.call(t, "GET", path, "", nil); strings.Contains(body, "disk-secret") || !strings.Contains(body, `"destination":"disk"`) {
			t.Errorf("GET %s: %s, want the destination's name and no secret", path, body)
		}
	}
	if strings.Contains(h.logs.String(), "disk-secret") {
		t.Error("the hub logged the destination's secret")
	}
}

// A failed test says which destination and machine, with the reason.
func TestDestinationTestFails(t *testing.T) {
	h := newTestHub(t)
	h.store.Destinations = fakeDestinations{}
	_, _, body := h.call(t, "POST", "/v1/backups/destinations/disk/test", "", map[string]any{"machine": "a"})
	var a Action
	_ = json.Unmarshal([]byte(body), &a)
	d, _, stop := startDaemon(t, h.URL, t.TempDir(), &fakeRunner{})
	d.Backups = &fakeBackups{fail: true}
	eventually(t, "the test to finish", func() bool {
		got, _ := h.store.Get(context.Background(), 1, a.ID)
		return got.Finished()
	})
	stop()
	got, _ := h.store.Get(context.Background(), 1, a.ID)
	if got.Status != StatusFailed || deref(got.Output) != "Could not reach the destination disk from a: wrong password or no key found." {
		t.Errorf("failed test = %+v %q", got.Status, deref(got.Output))
	}
}
