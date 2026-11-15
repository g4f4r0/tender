package install

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/config"
)

const newBinary = "tender binary, the new one"

// release serves a fake GitHub: the releases API and the files of each release.
type release struct {
	srv        *httptest.Server
	mu         sync.Mutex
	hits       []string
	latest     string             // the tag of the latest release
	pre        map[string]bool    // tags that are prereleases
	sums       string             // the SHA256SUMS body
	sig        string             // SHA256SUMS.sig; "" signs sums with key
	key        ed25519.PrivateKey // the release key
	noSig      bool               // the release has no SHA256SUMS.sig, like v0.2.2
	binVersion string             // what the release's binary says it is with "tender version"
}

var goodSums = sum([]byte(newBinary)) + "  tender_linux_amd64\n"

func newRelease(t *testing.T, e *env, sums string) *release {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &release{sums: sums, key: key, latest: "v0.2.0", pre: map[string]bool{}, binVersion: "0.2.0"}
	describe := func(w http.ResponseWriter, tag string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"tag_name":%q,"prerelease":%v,"draft":false,"assets":[{"name":"tender_linux_amd64"}]}`, tag, r.pre[tag])
	}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.hits = append(r.hits, req.URL.Path)
		r.mu.Unlock()
		p := req.URL.Path
		switch {
		case p == "/api/latest":
			describe(w, r.latest)
		case strings.HasPrefix(p, "/api/tags/"):
			describe(w, strings.TrimPrefix(p, "/api/tags/"))
		case strings.HasSuffix(p, "/SHA256SUMS"):
			_, _ = w.Write([]byte(r.sums))
		case strings.HasSuffix(p, "/SHA256SUMS.sig"):
			if r.noSig {
				http.NotFound(w, req)
				return
			}
			sig := r.sig
			if sig == "" {
				sig = base64.StdEncoding.EncodeToString(ed25519.Sign(r.key, []byte(r.sums))) + "\n"
			}
			_, _ = w.Write([]byte(sig))
		case strings.HasSuffix(p, "/tender_linux_amd64"):
			_, _ = w.Write([]byte(newBinary))
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	e.in.ReleasesURL = r.srv.URL + "/api"
	e.in.DownloadURL = r.srv.URL + "/download"
	e.in.ReleaseKeys = []ed25519.PublicKey{pub}
	e.in.Version = "0.1.0"
	e.in.BusyWait, e.in.HealthWait, e.in.HealthEvery = time.Second, 300*time.Millisecond, 20*time.Millisecond

	// The hub on this machine answers /v1/health where hub.env says it listens.
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) }))
	t.Cleanup(health.Close)
	if env, err := os.ReadFile(e.path(hubEnvPath)); err == nil {
		env = []byte(setEnv(string(env), "TENDER_LISTEN", health.Listener.Addr().String()))
		if err := os.WriteFile(e.path(hubEnvPath), env, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The new binary prints its version. On "install refresh" the restarted daemon reports to the hub on
	// the version of the binary in place, unless the new one is unhealthy and never reports.
	e.fake.respond = func(name string, args []string) ([]byte, bool, error) {
		cmd := strings.Join(args, " ")
		switch {
		case name == e.path(newBinaryPath) && cmd == "version":
			return []byte("Tender " + r.binVersion + "\n"), true, nil
		case name == e.path(binaryPath) && cmd == "install refresh":
			isNew := e.read(t, binaryPath) == newBinary
			if isNew && e.fake.unhealthy {
				return []byte("Restarted tenderd\n"), true, nil
			}
			v := e.in.Version
			if isNew {
				v = r.binVersion
			}
			dir := e.path(config.DefaultStateDir)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, true, err
			}
			return []byte("Restarted tenderd\n"), true, config.WriteReportedVersion(dir, v)
		}
		return nil, false, nil
	}
	return r
}

// installBoth installs the daemon and the hub, with a hub that answers /v1/health, then forgets what ran.
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
	old := e.read(t, "/usr/local/bin/tender")
	if err := e.in.Update(context.Background(), ""); err != nil {
		t.Fatalf("%v\n%s", err, e.out.String())
	}
	if got := e.read(t, "/usr/local/bin/tender"); got != newBinary {
		t.Errorf("binary = %q", got)
	}
	if e.mode(t, "/usr/local/bin/tender") != 0o755 {
		t.Error("the binary is not executable")
	}
	if got := e.read(t, "/usr/local/bin/tender.previous"); got != old {
		t.Errorf("tender.previous = %q, want the old binary", got)
	}
	if exists(e, "/usr/local/bin/tender.new") {
		t.Error("the download was left behind")
	}
	cmds := strings.Join(e.commands(), "\n")
	for _, want := range []string{
		e.path(newBinaryPath) + " version",
		e.path(newBinaryPath) + " install check",
		e.path("/usr/local/bin/tender") + " install refresh",
		"systemctl is-active --quiet tenderd",
		"systemctl is-active --quiet tenderhub",
	} {
		if !strings.Contains(cmds, want) {
			t.Errorf("did not run %q:\n%s", want, cmds)
		}
	}
	if !strings.Contains(e.out.String(), "Updating Tender 0.1.0 to v0.2.0") || !strings.Contains(e.out.String(), "Updated to Tender v0.2.0") ||
		!strings.Contains(e.out.String(), "checked its signature") {
		t.Errorf("output = %q", e.out.String())
	}
}

func TestUpdateSpecificVersionSkipsLatest(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	r := newRelease(t, e, goodSums)
	if err := e.in.Update(context.Background(), "0.2.0"); err != nil {
		t.Fatal(err)
	}
	for _, h := range r.hits {
		if h == "/api/latest" {
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

// An update installs only a release whose SHA256SUMS carries a valid signature by a release key.
func TestUpdateRefusesBadSignatureOrChecksum(t *testing.T) {
	_, otherKey, _ := ed25519.GenerateKey(nil)
	for name, c := range map[string]struct {
		sums, sig string
		noSig     bool
		want      string
	}{
		"checksum mismatch":   {sums: strings.Repeat("0", 64) + "  tender_linux_amd64\n", want: "does not match its checksum"},
		"not listed":          {sums: sum([]byte(newBinary)) + "  other_file\n", want: "do not list"},
		"empty":               {sums: "", want: "do not list"},
		"no signature":        {sums: goodSums, noSig: true, want: "signature"},
		"garbage signature":   {sums: goodSums, sig: "not base64!", want: "does not match its checksums"},
		"another key's":       {sums: goodSums, sig: base64.StdEncoding.EncodeToString(ed25519.Sign(otherKey, []byte(goodSums))), want: "does not match its checksums"},
		"signature of others": {sums: goodSums, sig: "", want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			installBoth(t, e)
			r := newRelease(t, e, c.sums)
			r.sig, r.noSig = c.sig, c.noSig
			if name == "signature of others" { // a signature of other sums, so the sums were changed after signing
				r.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(r.key, []byte("deadbeef  tender_linux_amd64\n")))
				c.want = "does not match its checksums"
			}
			before := e.tree(t)
			err := e.in.Update(context.Background(), "v0.2.0")
			if err == nil || !strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(before, e.tree(t)) {
				t.Error("the refused update changed something")
			}
			for _, cmd := range e.commands() {
				if strings.Contains(cmd, "refresh") {
					t.Errorf("ran %s", cmd)
				}
			}
		})
	}
}

// No update path installs a prerelease, and only "sudo tender update --version" downgrades.
func TestUpdateRefusesPrereleasesAndDowngrades(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	r := newRelease(t, e, goodSums)
	r.pre["v0.4.0-rc.1"] = true
	e.in.Version = "0.3.0"
	ctx := context.Background()

	if err := e.in.Update(ctx, "v0.4.0-rc.1"); err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Errorf("prerelease by hand: %v", err)
	}
	r.latest = "v0.4.0-rc.1"
	if err := e.in.Update(ctx, ""); err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Errorf("a latest release marked prerelease: %v", err)
	}
	r.pre["v0.4.0-rc.1"] = false // a tag with a prerelease part, though GitHub doesn't mark it
	if err := e.in.Apply(ctx, "v0.4.0-rc.1"); err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Errorf("apply of a prerelease tag: %v", err)
	}

	r.latest = "v0.2.0"
	if err := e.in.Update(ctx, ""); err != nil || !strings.Contains(e.out.String(), "newer than the latest release") {
		t.Errorf("latest older than this machine: %v %q", err, e.out.String())
	}
	if err := e.in.Apply(ctx, "v0.2.0"); err == nil || !strings.Contains(err.Error(), "never downgrades") {
		t.Errorf("apply of an older release: %v", err)
	}
	if got := e.read(t, "/usr/local/bin/tender"); got == newBinary {
		t.Fatal("a refused update installed the binary")
	}
	res, ok, _ := config.ReadUpdateResult(e.path(config.DefaultStateDir))
	if !ok || res.Status != "failed" || !strings.Contains(res.Output, "never downgrades") || res.Version != "0.2.0" {
		t.Errorf("the refused apply's result = %+v %v", res, ok)
	}

	if err := e.in.Update(ctx, "v0.2.0"); err != nil {
		t.Fatalf("root downgrades by hand: %v\n%s", err, e.out.String())
	}
	if got := e.read(t, "/usr/local/bin/tender"); got != newBinary {
		t.Error("the downgrade by hand didn't install")
	}
}

// The update job leaves how it ended for the daemon to report.
func TestApplyLeavesItsResult(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	newRelease(t, e, goodSums)
	if err := e.in.Apply(context.Background(), "v0.2.0"); err != nil {
		t.Fatalf("%v\n%s", err, e.out.String())
	}
	res, ok, err := config.ReadUpdateResult(e.path(config.DefaultStateDir))
	if !ok || err != nil || res != (config.UpdateResult{Version: "0.2.0", Status: "completed", Output: "Updated Tender 0.1.0 to v0.2.0."}) {
		t.Errorf("result = %+v %v %v", res, ok, err)
	}
}

// A new version that doesn't become healthy is rolled back to the previous binary.
func TestUpdateRollsBackAnUnhealthyRelease(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	newRelease(t, e, goodSums)
	e.fake.unhealthy = true
	old := e.read(t, "/usr/local/bin/tender")
	err := e.in.Apply(context.Background(), "v0.2.0")
	if err == nil || !strings.Contains(err.Error(), "rolled back to Tender 0.1.0") || !strings.Contains(err.Error(), "has not reported to the hub on Tender 0.2.0") {
		t.Fatalf("err = %v", err)
	}
	if got := e.read(t, "/usr/local/bin/tender"); got != old {
		t.Errorf("binary after the rollback = %q, want the old one", got)
	}
	if n := strings.Count(strings.Join(e.commands(), "\n"), "install refresh"); n != 2 {
		t.Errorf("refreshed %d times, want 2: the new binary, then the old", n)
	}
	res, _, _ := config.ReadUpdateResult(e.path(config.DefaultStateDir))
	if res.Status != "failed" || !strings.Contains(res.Output, "rolled back") {
		t.Errorf("result = %+v", res)
	}
}

// A hub that doesn't answer /v1/health fails the health check too.
func TestUpdateChecksTheHubsHealth(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	newRelease(t, e, goodSums)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := l.Addr().String()
	_ = l.Close()
	if err := os.WriteFile(e.path(hubEnvPath), []byte("TENDER_LISTEN="+dead+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = e.in.Update(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "/v1/health") || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v", err)
	}
}

// A binary that isn't the release it claims, or can't read the configs, never replaces the running one.
func TestUpdateChecksTheNewBinaryFirst(t *testing.T) {
	for name, setup := range map[string]func(e *env, r *release){
		"wrong version": func(_ *env, r *release) { r.binVersion = "0.1.9" },
		"bad config":    func(e *env, _ *release) { e.fake.failOnce = e.path(newBinaryPath) + " install check" },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			installBoth(t, e)
			r := newRelease(t, e, goodSums)
			setup(e, r)
			old := e.read(t, "/usr/local/bin/tender")
			if err := e.in.Update(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("err = %v", err)
			}
			if e.read(t, "/usr/local/bin/tender") != old || exists(e, "/usr/local/bin/tender.new") {
				t.Error("the bad binary was installed or left behind")
			}
		})
	}
}

// An update waits for a running backup or action, and gives up after BusyWait.
func TestUpdateWaitsForARunningBackup(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	newRelease(t, e, goodSums)
	dir := e.path(config.DefaultStateDir)
	release, err := config.HoldBusy(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.in.BusyWait = 200 * time.Millisecond
	if err := e.in.Update(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("err = %v", err)
	}
	if e.read(t, "/usr/local/bin/tender") == newBinary {
		t.Fatal("updated during a backup")
	}
	e.in.BusyWait = 5 * time.Second
	done := make(chan error, 1)
	go func() { done <- e.in.Update(context.Background(), "") }()
	time.Sleep(300 * time.Millisecond)
	if e.read(t, "/usr/local/bin/tender") == newBinary {
		t.Fatal("swapped the binary while the backup ran")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "Waiting up to 5s for the running backup or action to finish") {
		t.Errorf("output = %q", e.out.String())
	}
}

func TestUpdateErrors(t *testing.T) {
	e := newEnv(t)
	newRelease(t, e, goodSums)
	if err := e.in.Update(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("update without an install: %v", err)
	}
	installBoth(t, e)
	if err := e.in.Update(context.Background(), "../../x"); err == nil || !strings.Contains(err.Error(), "not a Tender version") {
		t.Errorf("bad version: %v", err)
	}
	e.in.ReleasesURL += "/missing"
	if err := e.in.Update(context.Background(), "v0.3.0"); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("missing release: %v", err)
	}
	if err := e.in.Update(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "--version") {
		t.Errorf("latest lookup failure: %v", err)
	}
}

func TestCheckReadsTheInstalledConfigs(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	if err := e.in.Check(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.path("/etc/tender/daemon.toml"), []byte("hub = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.in.Check(); err == nil {
		t.Error("a broken daemon.toml passed the check")
	}
}

func TestReleaseURLsForTests(t *testing.T) {
	api, dl := ReleaseURLs(func(string) string { return "" })
	if api != "https://api.github.com/repos/g4f4r0/tender/releases" || dl != "https://github.com/g4f4r0/tender/releases/download" {
		t.Errorf("production URLs = %s %s", api, dl)
	}
	api, dl = ReleaseURLs(func(k string) string {
		if k == EnvTestGitHub {
			return "http://127.0.0.1:1/"
		}
		return ""
	})
	if api != "http://127.0.0.1:1/repos/g4f4r0/tender/releases" || dl != "http://127.0.0.1:1/g4f4r0/tender/releases/download" {
		t.Errorf("test URLs = %s %s", api, dl)
	}
}

// The embedded key is the one the release workflow's secret signs with.
func TestReleaseKeysAreEmbedded(t *testing.T) {
	keys := ReleaseKeys()
	if len(keys) == 0 || base64.StdEncoding.EncodeToString(keys[0]) != "FFCQetX09NSfXliwZi1eVws0iGQkNwZch0dLQTru3TI=" {
		t.Errorf("keys = %v", keys)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.3.0", "v0.3.0", 0}, {"0.3.1", "0.3.0", 1}, {"0.2.9", "0.3.0", -1}, {"1.0.0", "0.99.99", 1},
		{"0.3.0-rc.1", "0.3.0", -1}, {"0.3.0", "0.3.0-rc.1", 1}, {"dev", "0.1.0", -1}, {"0.10.0", "0.9.0", 1},
		{"0.3.0-5-gabc123", "0.3.0", -1}, {"0.3.0-rc.10", "0.3.0-rc.9", 1}, {"0.3.0-rc.9", "0.3.0-rc.10", -1},
		{"0.3.0-rc.1", "0.3.0-rc.beta", -1}, {"0.3.0-alpha", "0.3.0-alpha.1", -1}, {"0.3.0-rc.1", "0.3.0-rc.1", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// A build between releases is a development build: the hub never updates it.
func TestDevBuild(t *testing.T) {
	for v, want := range map[string]bool{
		"0.3.1": false, "v0.3.1": false, "0.3.1-rc.1": false, "0.3.1-4-gabc123": true, "0.3.1-4-gabc123-dirty": true,
		"0.3.1-dirty": true, "dev": true, "": true,
	} {
		if got := DevBuild(v); got != want {
			t.Errorf("DevBuild(%q) = %v, want %v", v, got, want)
		}
	}
}

// The update job refuses to replace a development build, which may be newer than the release;
// root still can, by hand.
func TestApplyRefusesADevBuild(t *testing.T) {
	e := newEnv(t)
	installBoth(t, e)
	newRelease(t, e, goodSums)
	e.in.Version = "0.1.0-3-gabc123"
	if err := e.in.Apply(context.Background(), "v0.2.0"); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Fatalf("err = %v", err)
	}
	if err := e.in.Update(context.Background(), ""); err != nil {
		t.Fatalf("by hand: %v\n%s", err, e.out.String())
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
