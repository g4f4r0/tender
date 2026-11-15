package install

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const newBinary = "tender binary, the new one"

// release serves a fake GitHub: the latest-release answer and the files of tag v0.2.0.
type release struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []string
	sums string // the SHA256SUMS body
}

var goodSums = sum([]byte(newBinary)) + "  tender_linux_amd64\n"

func newRelease(t *testing.T, e *env, sums string) *release {
	t.Helper()
	r := &release{sums: sums}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`)) })
	mux.HandleFunc("/download/v0.2.0/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(r.sums)) })
	mux.HandleFunc("/download/v0.2.0/tender_linux_amd64", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(newBinary)) })
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.hits = append(r.hits, req.URL.Path)
		r.mu.Unlock()
		mux.ServeHTTP(w, req)
	}))
	t.Cleanup(r.srv.Close)
	e.in.LatestURL = r.srv.URL + "/latest"
	e.in.DownloadURL = r.srv.URL + "/download"
	e.in.Version = "0.1.0"
	return r
}

// installBoth installs the daemon and the hub, then forgets what ran.
func installBoth(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	if err := e.in.Daemon(ctx, daemonOpts); err != nil {
		t.Fatal(err)
	}
	if err := e.in.Hub(ctx, HubOptions{}); err != nil {
		t.Fatal(err)
	}
	e.commands()
	e.out.Reset()
}

func TestUpdateToLatest(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	newRelease(t, e, goodSums)
	if err := e.in.Update(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := e.read(t, "/usr/local/bin/tender"); got != newBinary {
		t.Errorf("binary = %q", got)
	}
	if e.mode(t, "/usr/local/bin/tender") != 0o755 {
		t.Error("the binary is not executable")
	}
	want := []string{e.path("/usr/local/bin/tender") + " install refresh"}
	if got := e.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
	if !strings.Contains(e.out.String(), "Updating Tender 0.1.0 to v0.2.0") || !strings.Contains(e.out.String(), "Updated to Tender v0.2.0") {
		t.Errorf("output = %q", e.out.String())
	}
}

func TestUpdateSpecificVersionSkipsLookup(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	r := newRelease(t, e, goodSums)
	if err := e.in.Update(context.Background(), "0.2.0"); err != nil {
		t.Fatal(err)
	}
	for _, h := range r.hits {
		if h == "/latest" {
			t.Error("asked for the latest release although a version was given")
		}
	}
	if got := e.read(t, "/usr/local/bin/tender"); got != newBinary {
		t.Errorf("binary = %q", got)
	}
}

func TestUpdateSameVersionDoesNothing(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	r := newRelease(t, e, goodSums)
	e.in.Version = "0.2.0"
	before := e.tree(t)
	if err := e.in.Update(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, e.tree(t)) || len(e.commands()) != 0 {
		t.Error("an update to the running version changed something")
	}
	if !strings.Contains(e.out.String(), "already installed") {
		t.Errorf("output = %q", e.out.String())
	}
	for _, h := range r.hits {
		if strings.Contains(h, "download") {
			t.Errorf("downloaded %s", h)
		}
	}
}

func TestUpdateStopsOnBadChecksum(t *testing.T) {
	for name, sums := range map[string]string{
		"mismatch":   strings.Repeat("0", 64) + "  tender_linux_amd64\n",
		"not listed": sum([]byte(newBinary)) + "  other_file\n",
		"empty":      "",
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			installBoth(t, e)
			newRelease(t, e, sums)
			before := e.tree(t)
			err := e.in.Update(context.Background(), "v0.2.0")
			if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(before, e.tree(t)) || len(e.commands()) != 0 {
				t.Error("the failed update changed something")
			}
		})
	}
}

func TestUpdateErrors(t *testing.T) {
	e := newEnv(t)
	newRelease(t, e, goodSums)
	if err := e.in.Update(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("update without an install: %v", err)
	}
	installBoth(t, e)
	if err := e.in.Update(context.Background(), "../../x"); err == nil || !strings.Contains(err.Error(), "not a tender version") {
		t.Errorf("bad version: %v", err)
	}
	e.in.DownloadURL = e.in.DownloadURL + "/missing"
	if err := e.in.Update(context.Background(), "v0.3.0"); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("missing release: %v", err)
	}
	e.in.LatestURL = e.in.DownloadURL + "/missing"
	if err := e.in.Update(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "--version") {
		t.Errorf("latest lookup failure: %v", err)
	}
}

func TestRefreshRestartsInstalledServices(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	e.fake.serve = ownServe
	before := e.tree(t)
	if err := e.in.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, e.tree(t)) {
		t.Error("refresh changed files that were already current")
	}
	want := []string{
		"id -u tender", "systemctl enable --now tenderd", "systemctl restart tenderd",
		"id -u tender", "systemctl enable --now tenderhub", "systemctl restart tenderhub",
		"tailscale serve status --json", "tailscale status --json",
	}
	if got := e.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(e.out.String(), "Next:") {
		t.Errorf("refresh printed first-install steps: %q", e.out.String())
	}
}

func TestRefreshRerendersUnitsAndRepairsRestic(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	for _, p := range []string{"/etc/systemd/system/tenderd.service", "/usr/local/bin/restic"} {
		if err := os.WriteFile(e.path(p), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.in.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	unit, _ := units.ReadFile("units/tenderd.service")
	if e.read(t, "/etc/systemd/system/tenderd.service") != string(unit) {
		t.Error("the unit was not re-rendered")
	}
	if !strings.Contains(e.read(t, "/usr/local/bin/restic"), "fake restic") {
		t.Error("restic was not re-installed")
	}
	if !strings.Contains(strings.Join(e.commands(), "\n"), "systemctl daemon-reload") {
		t.Error("no daemon-reload after the unit changed")
	}
}

func TestRefreshOnlyTouchesWhatIsInstalled(t *testing.T) {
	e := newEnv(t)
	if err := e.in.Daemon(context.Background(), daemonOpts); err != nil {
		t.Fatal(err)
	}
	e.commands()
	if err := e.in.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.commands() {
		if strings.Contains(c, "tenderhub") || strings.HasPrefix(c, "tailscale") {
			t.Errorf("touched the hub: %s", c)
		}
	}
	if err := newEnv(t).in.Refresh(context.Background()); err == nil {
		t.Error("refresh on a machine without tender succeeded")
	}
}

func TestDaemonReusesConfigAndToken(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.in.Daemon(ctx, daemonOpts); err != nil {
		t.Fatal(err)
	}
	before := e.tree(t)
	e.commands()
	// Nothing is passed: the installed config and token stay.
	if err := e.in.Daemon(ctx, DaemonOptions{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, e.tree(t)) {
		t.Error("files changed")
	}
	// A fresh machine with no options says what is missing.
	err := newEnv(t).in.Daemon(ctx, DaemonOptions{Token: testToken})
	if err == nil || !strings.Contains(err.Error(), "--hub URL and --machine NAME") {
		t.Errorf("err = %v", err)
	}
}
