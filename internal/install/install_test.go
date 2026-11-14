package install

import (
	"bytes"
	"compress/bzip2"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/config"
)

const (
	testToken = "s3cret-hub-token"
	testHub   = "https://apps.example.ts.net"

	findmntJSON = `{"filesystems":[
		{"target":"/","fstype":"ext4"},{"target":"/boot/efi","fstype":"vfat"},{"target":"/boot","fstype":"ext4"},
		{"target":"/srv/share","fstype":"nfs4"},{"target":"/tmp","fstype":"tmpfs"},{"target":"/","fstype":"ext4"}]}`
	noServe  = `{}`
	ownServe = `{"TCP":{"443":{"HTTPS":true}},"Web":{"apps.example.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8080"}}}}}`
	oldServe = `{"TCP":{"443":{"HTTPS":true}},"Web":{"apps.example.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}}}`
)

// fake records every command and answers like the real ones would.
type fake struct {
	commands   []string
	userExists bool
	serve      string // what "tailscale serve status --json" prints
}

func (f *fake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.commands = append(f.commands, cmd)
	switch {
	case name == "id":
		if !f.userExists {
			return nil, errors.New("no such user")
		}
	case name == "useradd":
		f.userExists = true
	case name == "findmnt":
		return []byte(findmntJSON), nil
	case cmd == "tailscale serve status --json":
		return []byte(f.serve), nil
	case cmd == "tailscale status --json":
		return []byte(`{"Self":{"DNSName":"apps.example.ts.net."}}`), nil
	}
	return nil, nil
}

type env struct {
	in   *Installer
	fake *fake
	out  *bytes.Buffer
	root string
}

// newEnv makes an installer on a temporary root with systemd "running", a fake restic download and a fake tender binary.
func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "run/systemd/system"), 0o755); err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(t.TempDir(), "tender")
	if err := os.WriteFile(self, []byte("tender binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile("testdata/restic.bz2")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(archive)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
	t.Cleanup(srv.Close)

	f := &fake{serve: noServe}
	out := &bytes.Buffer{}
	return &env{fake: f, out: out, root: root, in: &Installer{
		Root: root, Run: f.run, Out: out, Platform: "linux/amd64", Binary: self,
		ResticURL: srv.URL, ResticArchiveSHA256: sum(archive), ResticBinarySHA256: sum(bin),
	}}
}

func (e *env) path(p string) string { return filepath.Join(e.root, p) }

func (e *env) read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(e.path(p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *env) mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(e.path(p))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// tree lists every file and folder under the root with its mode, content and modification time.
func (e *env) tree(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(e.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == e.root {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(e.root, p)
		entry := fmt.Sprintf("%v", fi.Mode())
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			entry += fmt.Sprintf(" %d %x", fi.ModTime().UnixNano(), b)
		}
		got[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// commands returns what ran since the last call.
func (e *env) commands() []string {
	c := e.fake.commands
	e.fake.commands = nil
	return c
}

func (e *env) noSecret(t *testing.T, secret string) {
	t.Helper()
	if strings.Contains(e.out.String(), secret) || strings.Contains(strings.Join(e.fake.commands, "\n"), secret) {
		t.Errorf("the secret leaked into the output or a command")
	}
}

var agentOpts = AgentOptions{Hub: testHub, Machine: "code", Token: testToken}

func TestAgentFreshInstall(t *testing.T) {
	e := newEnv(t)
	if err := e.in.Agent(context.Background(), agentOpts); err != nil {
		t.Fatal(err)
	}

	creds := e.path("/etc/tender/credentials")
	wantCommands := []string{
		"findmnt --real --list --json --output TARGET,FSTYPE",
		"id -u tender",
		"useradd --system --user-group --no-create-home --home-dir /var/lib/tender --shell /usr/sbin/nologin tender",
		"chown root:tender " + creds,
		"chown root:tender " + creds + "/hub-token",
		"systemctl daemon-reload",
		"systemctl enable --now tender-agent",
	}
	if got := e.commands(); !reflect.DeepEqual(got, wantCommands) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantCommands, "\n"))
	}

	files := map[string]os.FileMode{
		"/usr/local/bin/restic":                    0o755,
		"/usr/local/bin/tender":                    0o755,
		"/etc/tender/agent.toml":                   0o644,
		"/etc/tender/credentials/hub-token":        0o640,
		"/etc/systemd/system/tender-agent.service": 0o644,
		"/etc/tender":                              0o755,
		"/etc/tender/credentials":                  0o750,
	}
	for p, m := range files {
		if got := e.mode(t, p); got != m {
			t.Errorf("%s has mode %v, want %v", p, got, m)
		}
	}
	// restic, tender and the unit are the exact bytes; nothing else is in the root.
	if got := e.read(t, "/usr/local/bin/tender"); got != "tender binary" {
		t.Errorf("tender binary = %q", got)
	}
	if got := e.read(t, "/usr/local/bin/restic"); !strings.Contains(got, "fake restic") {
		t.Errorf("restic = %q", got)
	}
	if got := e.read(t, "/etc/tender/credentials/hub-token"); got != testToken {
		t.Errorf("hub-token = %q", got)
	}
	unit, _ := units.ReadFile("units/tender-agent.service")
	if got := e.read(t, "/etc/systemd/system/tender-agent.service"); got != string(unit) {
		t.Error("the unit file differs from the embedded one")
	}
	fileCount := 0
	for _, entry := range e.tree(t) {
		if !strings.HasPrefix(entry, "d") {
			fileCount++
		}
	}
	if fileCount != 5 {
		t.Errorf("%d files in the root, want 5", fileCount)
	}

	cfg, err := config.LoadAgent(e.path("/etc/tender/agent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hub != testHub || cfg.Machine != "code" || !reflect.DeepEqual(cfg.Backups.Paths, []string{"/", "/boot", "/boot/efi"}) ||
		!reflect.DeepEqual(cfg.Backups.Exclude, config.DefaultExclude) {
		t.Errorf("agent.toml = %+v", cfg)
	}
	e.noSecret(t, testToken)
}

func TestAgentSecondRunChangesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.in.Agent(ctx, agentOpts); err != nil {
		t.Fatal(err)
	}
	before := e.tree(t)
	e.commands()
	e.out.Reset()

	// The same token, then no token at all: both leave the files alone.
	for _, token := range []string{testToken, ""} {
		o := agentOpts
		o.Token = token
		if err := e.in.Agent(ctx, o); err != nil {
			t.Fatal(err)
		}
		if after := e.tree(t); !reflect.DeepEqual(before, after) {
			t.Errorf("token %q: the second run changed files", token)
		}
		want := []string{"id -u tender", "systemctl enable --now tender-agent"}
		if got := e.commands(); !reflect.DeepEqual(got, want) {
			t.Errorf("token %q: second run commands = %v, want %v", token, got, want)
		}
	}
	if !strings.Contains(e.out.String(), "kept") || strings.Contains(e.out.String(), "wrote") {
		t.Errorf("output = %q", e.out.String())
	}
}

func TestAgentKeepsConfigUnlessForced(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.in.Agent(ctx, agentOpts); err != nil {
		t.Fatal(err)
	}
	custom := "hub = \"https://other.example.net\"\nmachine = \"mine\"\n"
	if err := os.WriteFile(e.path("/etc/tender/agent.toml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	e.commands()

	if err := e.in.Agent(ctx, agentOpts); err != nil {
		t.Fatal(err)
	}
	if got := e.read(t, "/etc/tender/agent.toml"); got != custom {
		t.Errorf("existing config was replaced: %q", got)
	}
	for _, c := range e.commands() {
		if strings.HasPrefix(c, "findmnt") {
			t.Error("findmnt ran although the config stays")
		}
	}

	o := agentOpts
	o.Force = true
	if err := e.in.Agent(ctx, o); err != nil {
		t.Fatal(err)
	}
	if cfg, err := config.LoadAgent(e.path("/etc/tender/agent.toml")); err != nil || cfg.Machine != "code" {
		t.Errorf("forced config = %+v, %v", cfg, err)
	}
	// The config changed under a running service: it restarts.
	got := e.commands()
	if last := got[len(got)-1]; last != "systemctl restart tender-agent" {
		t.Errorf("commands = %v", got)
	}
}

func TestAgentNewTokenRestarts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.in.Agent(ctx, agentOpts); err != nil {
		t.Fatal(err)
	}
	e.commands()
	o := agentOpts
	o.Token = "another-token"
	if err := e.in.Agent(ctx, o); err != nil {
		t.Fatal(err)
	}
	want := []string{"id -u tender", "chown root:tender " + e.path("/etc/tender/credentials/hub-token"),
		"systemctl enable --now tender-agent", "systemctl restart tender-agent"}
	if got := e.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
	e.noSecret(t, "another-token")
}

func TestAgentNeedsTokenOnFirstRun(t *testing.T) {
	e := newEnv(t)
	o := agentOpts
	o.Token = ""
	if err := e.in.Agent(context.Background(), o); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %v", err)
	}
	if len(e.fake.commands) != 0 {
		t.Errorf("commands ran: %v", e.fake.commands)
	}
}

func TestAgentRejectsBadSettingsBeforeChangingAnything(t *testing.T) {
	for name, o := range map[string]AgentOptions{
		"hub is not a URL": {Hub: "apps", Machine: "code", Token: testToken},
		"no machine":       {Hub: testHub, Token: testToken},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			before := e.tree(t)
			if err := e.in.Agent(context.Background(), o); err == nil {
				t.Fatal("no error")
			}
			if !reflect.DeepEqual(before, e.tree(t)) {
				t.Error("files were written")
			}
			for _, c := range e.fake.commands {
				if !strings.HasPrefix(c, "findmnt") {
					t.Errorf("command ran: %s", c)
				}
			}
		})
	}
}

func TestResticChecksBeforeWriting(t *testing.T) {
	notFound := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(notFound.Close)
	for name, tamper := range map[string]func(in *Installer){
		"archive hash": func(in *Installer) { in.ResticArchiveSHA256 = strings.Repeat("0", 64) },
		"binary hash":  func(in *Installer) { in.ResticBinarySHA256 = strings.Repeat("0", 64) },
		"bad status":   func(in *Installer) { in.ResticURL = notFound.URL },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			tamper(e.in)
			before := e.tree(t)
			if err := e.in.Agent(context.Background(), agentOpts); err == nil {
				t.Fatal("no error")
			}
			if !reflect.DeepEqual(before, e.tree(t)) {
				t.Error("something was written")
			}
			for _, c := range e.fake.commands {
				if !strings.HasPrefix(c, "findmnt") {
					t.Errorf("command ran before the check: %s", c)
				}
			}
		})
	}
}

func TestResticSkippedWhenCurrent(t *testing.T) {
	e := newEnv(t)
	if err := e.in.Agent(context.Background(), agentOpts); err != nil {
		t.Fatal(err)
	}
	// A dead download URL no longer matters once the right binary is in place.
	e.in.ResticURL = "http://127.0.0.1:1/restic.bz2"
	if err := e.in.Agent(context.Background(), agentOpts); err != nil {
		t.Fatal(err)
	}
}

func TestRefusesOtherPlatformsAndNoSystemd(t *testing.T) {
	e := newEnv(t)
	e.in.Platform = "linux/arm64"
	if err := e.in.Agent(context.Background(), agentOpts); err == nil || !strings.Contains(err.Error(), "linux/amd64 only") {
		t.Errorf("err = %v", err)
	}
	if err := e.in.Hub(context.Background(), HubOptions{}); err == nil {
		t.Error("hub on arm64 was accepted")
	}

	e = newEnv(t)
	if err := os.RemoveAll(e.path("/run")); err != nil {
		t.Fatal(err)
	}
	if err := e.in.Agent(context.Background(), agentOpts); err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Errorf("err = %v", err)
	}
	if len(e.fake.commands) != 0 {
		t.Errorf("commands ran: %v", e.fake.commands)
	}
}

func TestCredential(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, bad := range []string{"", "R2", "../etc", "a/b", ".", "..", "-x", "a b", "a_b"} {
		if err := e.in.Credential(ctx, bad, "value"); err == nil {
			t.Errorf("name %q was accepted", bad)
		}
	}
	if err := e.in.Credential(ctx, "r2-apps.restic-password", ""); err == nil {
		t.Error("empty value accepted")
	}
	if len(e.fake.commands) != 0 || len(e.tree(t)) != 3 {
		t.Errorf("a rejected credential changed something: %v", e.fake.commands)
	}

	if err := e.in.Credential(ctx, "r2-apps.restic-password", "the-restic-password"); err != nil {
		t.Fatal(err)
	}
	const p = "/etc/tender/credentials/r2-apps.restic-password"
	if e.read(t, p) != "the-restic-password" || e.mode(t, p) != 0o640 || e.mode(t, "/etc/tender/credentials") != 0o750 {
		t.Error("wrong content or modes")
	}
	want := []string{"chown root:tender " + e.path("/etc/tender/credentials"), "chown root:tender " + e.path(p)}
	if got := e.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
	// Saving the same value again does nothing; a new value replaces it.
	if err := e.in.Credential(ctx, "r2-apps.restic-password", "the-restic-password"); err != nil || len(e.commands()) != 0 {
		t.Errorf("repeat changed something: %v", err)
	}
	if err := e.in.Credential(ctx, "r2-apps.restic-password", "new"); err != nil || e.read(t, p) != "new" {
		t.Errorf("replace failed: %v", err)
	}
	e.noSecret(t, "the-restic-password")
}

func TestHubFreshInstall(t *testing.T) {
	e := newEnv(t)
	if err := e.in.Hub(context.Background(), HubOptions{}); err != nil {
		t.Fatal(err)
	}
	creds := e.path("/etc/tender/credentials")
	want := []string{
		"id -u tender",
		"useradd --system --user-group --no-create-home --home-dir /var/lib/tender --shell /usr/sbin/nologin tender",
		"chown root:tender " + creds,
		"systemctl daemon-reload",
		"systemctl enable --now tender-hub",
		"tailscale serve status --json",
		"tailscale serve --bg --https=443 http://127.0.0.1:8080",
		"tailscale status --json",
	}
	if got := e.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if e.mode(t, "/etc/tender/hub.toml") != 0o644 || e.mode(t, "/etc/systemd/system/tender-hub.service") != 0o644 || e.mode(t, "/usr/local/bin/tender") != 0o755 {
		t.Error("wrong modes")
	}
	if _, err := os.Stat(e.path("/usr/local/bin/restic")); err == nil {
		t.Error("the hub installed restic")
	}
	h, err := config.LoadHub(e.path("/etc/tender/hub.toml"))
	if err != nil || h.Listen != "127.0.0.1:8080" || h.Database != "/var/lib/tender/hub/hub.db" || h.Email != nil {
		t.Errorf("hub.toml = %+v, %v", h, err)
	}
	if strings.Contains(e.read(t, "/etc/tender/hub.toml"), "email") {
		t.Error("hub.toml has an email table without email flags")
	}
	if strings.Contains(e.read(t, "/etc/systemd/system/tender-hub.service"), "LoadCredential") {
		t.Error("the unit loads smtp-password, which does not exist")
	}
	if !strings.Contains(e.out.String(), "Hub URL: https://apps.example.ts.net\n") {
		t.Errorf("output = %q", e.out.String())
	}

	// Second run: nothing changes, and Serve already shows the hub.
	before := e.tree(t)
	e.fake.serve = ownServe
	if err := e.in.Hub(context.Background(), HubOptions{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, e.tree(t)) {
		t.Error("the second run changed files")
	}
	for _, c := range e.commands() {
		if strings.HasPrefix(c, "tailscale serve --") {
			t.Errorf("serve ran again: %s", c)
		}
	}
}

func TestHubEmailAndSMTPCredential(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.in.Credential(ctx, "smtp-password", "smtp-secret"); err != nil {
		t.Fatal(err)
	}
	mail := &config.Email{Host: "smtp.example.net", Port: 465, Username: "api", From: "tender <noreply@example.net>", To: []string{"a@example.net", "b@example.net"}}
	if err := e.in.Hub(ctx, HubOptions{Email: mail}); err != nil {
		t.Fatal(err)
	}
	h, err := config.LoadHub(e.path("/etc/tender/hub.toml"))
	if err != nil || !reflect.DeepEqual(h.Email, mail) {
		t.Errorf("email = %+v, %v", h.Email, err)
	}
	if !strings.Contains(e.read(t, "/etc/systemd/system/tender-hub.service"), "LoadCredential=smtp-password:/etc/tender/credentials/smtp-password\n") {
		t.Error("the unit does not load the existing smtp-password")
	}
	e.noSecret(t, "smtp-secret")

	// A bad address stops the install before it writes.
	bad := newEnv(t)
	bad.in.Run = e.fake.run
	m := *mail
	m.From = "not an address"
	if err := bad.in.Hub(ctx, HubOptions{Email: &m}); err == nil {
		t.Error("bad email accepted")
	}
	if _, err := os.Stat(bad.path("/etc/tender/hub.toml")); err == nil {
		t.Error("hub.toml was written")
	}
}

// smtp-password added after the hub is installed goes into the hub's unit, and the hub restarts.
func TestSMTPCredentialAfterHub(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.in.Hub(ctx, HubOptions{}); err != nil {
		t.Fatal(err)
	}
	unit := "/etc/systemd/system/tender-hub.service"
	if strings.Contains(e.read(t, unit), "LoadCredential") || !strings.Contains(e.read(t, unit), "InaccessiblePaths=-/etc/tender/credentials\n") {
		t.Fatalf("unit before:\n%s", e.read(t, unit))
	}
	e.commands() // reset the recorded commands
	if err := e.in.Credential(ctx, "smtp-password", "smtp-secret"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.read(t, unit), "LoadCredential=smtp-password:/etc/tender/credentials/smtp-password\n") {
		t.Error("the unit does not load the new smtp-password")
	}
	chown := "chown root:tender " + e.path("/etc/tender/credentials/smtp-password")
	want := []string{chown, "systemctl daemon-reload", "systemctl enable --now tender-hub", "systemctl restart tender-hub"}
	if got := e.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
	e.noSecret(t, "smtp-secret")

	// Another credential doesn't touch the hub.
	if err := e.in.Credential(ctx, "r2-apps.restic-password", "x"); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.commands() {
		if strings.HasPrefix(c, "systemctl") {
			t.Errorf("an agent credential ran %q", c)
		}
	}
}

func TestHubServeCases(t *testing.T) {
	for name, tc := range map[string]struct {
		serve       string
		wantServe   bool
		wantURL     bool
		wantMessage string
	}{
		"free":     {serve: noServe, wantServe: true, wantURL: true},
		"ours":     {serve: ownServe, wantURL: true, wantMessage: "already publishes"},
		"busy":     {serve: oldServe, wantMessage: "left it alone.\nTo publish the hub there, run: tailscale serve --bg --https=443 http://127.0.0.1:8080"},
		"no serve": {serve: "not json", wantMessage: "Tailscale did not answer"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.fake.serve = tc.serve
			if err := e.in.Hub(context.Background(), HubOptions{}); err != nil {
				t.Fatal(err)
			}
			ran := false
			for _, c := range e.fake.commands {
				ran = ran || c == "tailscale serve --bg --https=443 http://127.0.0.1:8080"
			}
			if ran != tc.wantServe {
				t.Errorf("serve command ran = %v, want %v", ran, tc.wantServe)
			}
			out := e.out.String()
			if strings.Contains(out, "Hub URL") != tc.wantURL {
				t.Errorf("hub URL printed = %v, want %v\n%s", !tc.wantURL, tc.wantURL, out)
			}
			if !strings.Contains(out, tc.wantMessage) {
				t.Errorf("output lacks %q:\n%s", tc.wantMessage, out)
			}
		})
	}
}

func TestUnitsFollowTheDecisions(t *testing.T) {
	agent, _ := units.ReadFile("units/tender-agent.service")
	hub, _ := units.ReadFile("units/tender-hub.service")
	for _, want := range []string{"User=tender", "AmbientCapabilities=CAP_DAC_READ_SEARCH", "CapabilityBoundingSet=CAP_DAC_READ_SEARCH",
		"StateDirectory=tender/agent", "Restart=always", "ExecStart=/usr/local/bin/tender agent --config /etc/tender/agent.toml"} {
		if !strings.Contains(string(agent), want+"\n") {
			t.Errorf("agent unit lacks %s", want)
		}
	}
	if strings.Contains(string(agent), "LoadCredential") {
		t.Error("agent unit still loads credentials")
	}
	for _, want := range []string{"User=tender", "CapabilityBoundingSet=", "StateDirectory=tender/hub", "Restart=always",
		"ExecStart=/usr/local/bin/tender hub --config /etc/tender/hub.toml"} {
		if !strings.Contains(string(hub), want+"\n") {
			t.Errorf("hub unit lacks %s", want)
		}
	}
	if strings.Contains(string(hub), "DynamicUser") || strings.Contains(string(hub), "webhook") || strings.Contains(string(hub), "deadman") {
		t.Error("hub unit keeps stale settings")
	}
}
