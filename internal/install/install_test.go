package install

import (
	"bytes"
	"compress/bzip2"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	failOnce   string // this command fails the next time it runs
}

func (f *fake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.commands = append(f.commands, cmd)
	if cmd == f.failOnce {
		f.failOnce = ""
		return nil, errors.New("failed on purpose")
	}
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

var daemonOpts = DaemonOptions{Hub: testHub, Machine: "code", Token: testToken}

func TestDaemonFreshInstall(t *testing.T) {
	e := newEnv(t)
	if err := e.in.Daemon(context.Background(), daemonOpts); err != nil {
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
		"systemctl enable --now tenderd",
	}
	if got := e.commands(); !reflect.DeepEqual(got, wantCommands) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantCommands, "\n"))
	}

	files := map[string]os.FileMode{
		"/usr/local/bin/restic":               0o755,
		"/usr/local/bin/tender":               0o755,
		"/etc/tender/daemon.toml":             0o644,
		"/etc/tender/credentials/hub-token":   0o640,
		"/etc/systemd/system/tenderd.service": 0o644,
		"/etc/tender":                         0o755,
		"/etc/tender/credentials":             0o750,
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
	unit, _ := units.ReadFile("units/tenderd.service")
	if got := e.read(t, "/etc/systemd/system/tenderd.service"); got != string(unit) {
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

	cfg, err := config.LoadDaemon(e.path("/etc/tender/daemon.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hub != testHub || cfg.Machine != "code" || !reflect.DeepEqual(cfg.Backups.Paths, []string{"/", "/boot", "/boot/efi"}) ||
		!reflect.DeepEqual(cfg.Backups.Exclude, config.DefaultExclude) {
		t.Errorf("daemon.toml = %+v", cfg)
	}
	e.noSecret(t, testToken)

	// Each step a person cares about gets one line, then the numbered next steps.
	if strings.Contains(e.out.String(), "install credential") {
		t.Error("the next steps still send the person to the removed credential command")
	}
	for _, want := range []string{"Installing restic 0.19.1\n", "Created the tender user\n", "Saved the hub token\n", "Started tenderd\n",
		"The daemon is running. It gets its destination, schedule and keys from the hub. Next:",
		`tender backups set code --destination DESTINATION --schedule "30 3 * * *" --retention-days 30`} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, e.out.String())
		}
	}
}

func TestDaemonSecondRunChangesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.in.Daemon(ctx, daemonOpts); err != nil {
		t.Fatal(err)
	}
	before := e.tree(t)
	e.commands()
	e.out.Reset()

	// The same token, then no token at all: both leave the files alone.
	for _, token := range []string{testToken, ""} {
		o := daemonOpts
		o.Token = token
		if err := e.in.Daemon(ctx, o); err != nil {
			t.Fatal(err)
		}
		if after := e.tree(t); !reflect.DeepEqual(before, after) {
			t.Errorf("token %q: the second run changed files", token)
		}
		want := []string{"id -u tender", "systemctl enable --now tenderd"}
		if got := e.commands(); !reflect.DeepEqual(got, want) {
			t.Errorf("token %q: second run commands = %v, want %v", token, got, want)
		}
	}
	if !strings.Contains(e.out.String(), "Nothing to change") || strings.Contains(e.out.String(), "Wrote") || strings.Contains(e.out.String(), "Next:") {
		t.Errorf("output = %q", e.out.String())
	}
}

func TestDaemonKeepsConfigUnlessForced(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.in.Daemon(ctx, daemonOpts); err != nil {
		t.Fatal(err)
	}
	custom := "hub = \"https://other.example.net\"\nmachine = \"mine\"\n"
	if err := os.WriteFile(e.path("/etc/tender/daemon.toml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	e.commands()

	if err := e.in.Daemon(ctx, daemonOpts); err != nil {
		t.Fatal(err)
	}
	if got := e.read(t, "/etc/tender/daemon.toml"); got != custom {
		t.Errorf("existing config was replaced: %q", got)
	}
	for _, c := range e.commands() {
		if strings.HasPrefix(c, "findmnt") {
			t.Error("findmnt ran although the config stays")
		}
	}

	o := daemonOpts
	o.Force = true
	if err := e.in.Daemon(ctx, o); err != nil {
		t.Fatal(err)
	}
	if cfg, err := config.LoadDaemon(e.path("/etc/tender/daemon.toml")); err != nil || cfg.Machine != "code" {
		t.Errorf("forced config = %+v, %v", cfg, err)
	}
	// The config changed under a running service: it restarts.
	got := e.commands()
	if last := got[len(got)-1]; last != "systemctl restart tenderd" {
		t.Errorf("commands = %v", got)
	}
}

func TestDaemonNewTokenRestarts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.in.Daemon(ctx, daemonOpts); err != nil {
		t.Fatal(err)
	}
	e.commands()
	o := daemonOpts
	o.Token = "another-token"
	if err := e.in.Daemon(ctx, o); err != nil {
		t.Fatal(err)
	}
	want := []string{"id -u tender", "chown root:tender " + e.path("/etc/tender/credentials/hub-token"),
		"systemctl enable --now tenderd", "systemctl restart tenderd"}
	if got := e.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
	e.noSecret(t, "another-token")
}

func TestDaemonNeedsTokenOnFirstRun(t *testing.T) {
	e := newEnv(t)
	o := daemonOpts
	o.Token = ""
	if err := e.in.Daemon(context.Background(), o); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %v", err)
	}
	if len(e.fake.commands) != 0 {
		t.Errorf("commands ran: %v", e.fake.commands)
	}
}

func TestDaemonRejectsBadSettingsBeforeChangingAnything(t *testing.T) {
	for name, o := range map[string]DaemonOptions{
		"hub is not a URL": {Hub: "apps", Machine: "code", Token: testToken},
		"no machine":       {Hub: testHub, Token: testToken},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			before := e.tree(t)
			if err := e.in.Daemon(context.Background(), o); err == nil {
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
			if err := e.in.Daemon(context.Background(), daemonOpts); err == nil {
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
	if err := e.in.Daemon(context.Background(), daemonOpts); err != nil {
		t.Fatal(err)
	}
	// A dead download URL no longer matters once the right binary is in place.
	e.in.ResticURL = "http://127.0.0.1:1/restic.bz2"
	if err := e.in.Daemon(context.Background(), daemonOpts); err != nil {
		t.Fatal(err)
	}
}

func TestRefusesOtherPlatformsAndNoSystemd(t *testing.T) {
	e := newEnv(t)
	e.in.Platform = "linux/arm64"
	if err := e.in.Daemon(context.Background(), daemonOpts); err == nil || !strings.Contains(err.Error(), "linux/amd64 only") {
		t.Errorf("err = %v", err)
	}
	if err := e.in.Hub(context.Background(), HubOptions{}); err == nil {
		t.Error("hub on arm64 was accepted")
	}

	e = newEnv(t)
	if err := os.RemoveAll(e.path("/run")); err != nil {
		t.Fatal(err)
	}
	if err := e.in.Daemon(context.Background(), daemonOpts); err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Errorf("err = %v", err)
	}
	if len(e.fake.commands) != 0 {
		t.Errorf("commands ran: %v", e.fake.commands)
	}
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
		"systemctl enable --now tenderhub",
		"tailscale serve status --json",
		"tailscale serve --bg --https=443 http://127.0.0.1:8080",
		"tailscale status --json",
	}
	if got := e.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if e.mode(t, "/etc/tender/hub.env") != 0o600 || e.mode(t, "/etc/systemd/system/tenderhub.service") != 0o644 || e.mode(t, "/usr/local/bin/tender") != 0o755 {
		t.Error("wrong modes")
	}
	if _, err := os.Stat(e.path("/etc/tender/hub.toml")); err == nil {
		t.Error("the hub wrote a hub.toml")
	}
	if _, err := os.Stat(e.path("/usr/local/bin/restic")); err == nil {
		t.Error("the hub installed restic")
	}
	hubEnvText := e.read(t, "/etc/tender/hub.env")
	key := envValue([]byte(hubEnvText), "TENDER_ENCRYPTION_KEY")
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 32 {
		t.Errorf("TENDER_ENCRYPTION_KEY = %q is not 32 bytes of base64 (%v)", key, err)
	}
	if strings.Contains(hubEnvText, "SMTP") {
		t.Errorf("hub.env has SMTP settings without flags:\n%s", hubEnvText)
	}
	if unit := e.read(t, "/etc/systemd/system/tenderhub.service"); strings.Contains(unit, "LoadCredential") ||
		!strings.Contains(unit, "EnvironmentFile=/etc/tender/hub.env\n") || !strings.Contains(unit, "InaccessiblePaths=-/etc/tender/credentials\n") {
		t.Errorf("hub unit:\n%s", unit)
	}
	e.noSecret(t, key)
	for _, want := range []string{"Published the hub with Tailscale Serve at https://apps.example.ts.net\n", "The hub is running at https://apps.example.ts.net.", "tender destinations add NAME --endpoint URL --bucket BUCKET",
		"tender machines add NAME", "tender backups set NAME --destination NAME", "tender recipients add you@example.com",
		"daemon --hub https://apps.example.ts.net --machine NAME", "Alert emails are off until the hub has an SMTP server"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, e.out.String())
		}
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

// The encryption key is made once. Every later run keeps it, and a damaged one stops the install.
func TestHubKeepsItsEncryptionKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.in.Hub(ctx, HubOptions{}); err != nil {
		t.Fatal(err)
	}
	first := e.read(t, "/etc/tender/hub.env")
	if !strings.Contains(e.out.String(), "with a new encryption key. Back this file up") {
		t.Errorf("output does not say to back up the key:\n%s", e.out.String())
	}

	e.out.Reset()
	if err := e.in.Hub(ctx, HubOptions{SMTPHost: "smtp.example.net", SMTPFrom: "Tender <alerts@example.net>", SMTPPort: 587, SMTPUsername: "api", SMTPPassword: `p"a\ss $word`}); err != nil {
		t.Fatal(err)
	}
	second := e.read(t, "/etc/tender/hub.env")
	if envValue([]byte(first), "TENDER_ENCRYPTION_KEY") != envValue([]byte(second), "TENDER_ENCRYPTION_KEY") {
		t.Error("the encryption key changed on the second run")
	}
	for name, want := range map[string]string{"TENDER_SMTP_HOST": "smtp.example.net", "TENDER_SMTP_FROM": "Tender <alerts@example.net>",
		"TENDER_SMTP_PORT": "587", "TENDER_SMTP_USERNAME": "api", "TENDER_SMTP_PASSWORD": `p"a\ss $word`} {
		if got := envValue([]byte(second), name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if !strings.Contains(second, "TENDER_SMTP_FROM=\"Tender <alerts@example.net>\"\n") {
		t.Errorf("a value with spaces is not quoted for systemd:\n%s", second)
	}
	if e.mode(t, "/etc/tender/hub.env") != 0o600 {
		t.Error("hub.env is not 0600")
	}
	if strings.Contains(e.out.String(), "new encryption key") || !strings.Contains(e.out.String(), "Updated") {
		t.Errorf("output = %q", e.out.String())
	}
	e.noSecret(t, "ss $word")
	e.noSecret(t, envValue([]byte(second), "TENDER_ENCRYPTION_KEY"))

	// A run without flags keeps the SMTP settings and a line the owner added.
	edited := second + "TENDER_LISTEN=127.0.0.1:9090\n"
	if err := os.WriteFile(e.path("/etc/tender/hub.env"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.in.Hub(ctx, HubOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := e.read(t, "/etc/tender/hub.env"); got != edited {
		t.Errorf("a run without flags changed hub.env:\n%s", got)
	}
	// A damaged key is never replaced: the secrets in the database need it.
	bad := strings.Replace(second, envValue([]byte(second), "TENDER_ENCRYPTION_KEY"), "short", 1)
	if err := os.WriteFile(e.path("/etc/tender/hub.env"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.in.Hub(ctx, HubOptions{})
	if err == nil || !strings.Contains(err.Error(), "TENDER_ENCRYPTION_KEY") || e.read(t, "/etc/tender/hub.env") != bad {
		t.Errorf("a damaged key: err = %v", err)
	}
}

// The hub is published at the address hub.env sets.
func TestHubServesAtTheConfiguredAddress(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.path("/etc/tender"), 0o755); err != nil {
		t.Fatal(err)
	}
	key, _ := newEncryptionKey()
	if err := os.WriteFile(e.path("/etc/tender/hub.env"), []byte("TENDER_ENCRYPTION_KEY="+key+"\nTENDER_LISTEN=127.0.0.1:9090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.in.Hub(context.Background(), HubOptions{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.commands(), "tailscale serve --bg --https=443 http://127.0.0.1:9090") {
		t.Error("the hub was not published at TENDER_LISTEN")
	}
}

func TestHubRejectsHalfAnEmailSetupBeforeChangingAnything(t *testing.T) {
	for name, o := range map[string]HubOptions{
		"host without sender": {SMTPHost: "smtp.example.net"},
		"sender without host": {SMTPFrom: "alerts@example.net"},
		"line break":          {SMTPHost: "smtp.example.net", SMTPFrom: "a@example.net", SMTPPassword: "x\ny"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			before := e.tree(t)
			if err := e.in.Hub(context.Background(), o); err == nil {
				t.Fatal("no error")
			}
			if !reflect.DeepEqual(before, e.tree(t)) || len(e.fake.commands) != 0 {
				t.Errorf("the install changed something: %v", e.fake.commands)
			}
		})
	}
}

func TestHubServeCases(t *testing.T) {
	for name, tc := range map[string]struct {
		serve       string
		wantServe   bool
		wantURL     bool
		wantMessage string
	}{
		"free":     {serve: noServe, wantServe: true, wantURL: true, wantMessage: "Published the hub with Tailscale Serve at https://apps.example.ts.net\n"},
		"ours":     {serve: ownServe, wantURL: true, wantMessage: "The hub is running at https://apps.example.ts.net.\n"},
		"busy":     {serve: oldServe, wantMessage: "left it alone. To publish the hub there, run: tailscale serve --bg --https=443 http://127.0.0.1:8080\n"},
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
			if strings.Contains(out, "https://apps.example.ts.net") != tc.wantURL {
				t.Errorf("hub URL printed = %v, want %v\n%s", !tc.wantURL, tc.wantURL, out)
			}
			if !strings.Contains(out, tc.wantMessage) {
				t.Errorf("output lacks %q:\n%s", tc.wantMessage, out)
			}
		})
	}
}

func TestUnitsFollowTheDecisions(t *testing.T) {
	daemon, _ := units.ReadFile("units/tenderd.service")
	hub, _ := units.ReadFile("units/tenderhub.service")
	for _, want := range []string{"User=tender", "AmbientCapabilities=CAP_DAC_READ_SEARCH", "CapabilityBoundingSet=CAP_DAC_READ_SEARCH",
		"StateDirectory=tender/daemon", "Restart=always", "ExecStart=/usr/local/bin/tender daemon --config /etc/tender/daemon.toml"} {
		if !strings.Contains(string(daemon), want+"\n") {
			t.Errorf("daemon unit lacks %s", want)
		}
	}
	if strings.Contains(string(daemon), "LoadCredential") {
		t.Error("daemon unit still loads credentials")
	}
	for _, want := range []string{"User=tender", "CapabilityBoundingSet=", "StateDirectory=tender/hub", "Restart=always",
		"EnvironmentFile=/etc/tender/hub.env", "ExecStart=/usr/local/bin/tender hub",
		"InaccessiblePaths=-/etc/tender/credentials"} {
		if !strings.Contains(string(hub), want+"\n") {
			t.Errorf("hub unit lacks %s", want)
		}
	}
	if strings.Contains(string(hub), "hub.toml") || strings.Contains(string(hub), "LoadCredential") {
		t.Error("hub unit still uses hub.toml or LoadCredential")
	}
	if strings.Contains(string(hub), "DynamicUser") || strings.Contains(string(hub), "webhook") || strings.Contains(string(hub), "deadman") {
		t.Error("hub unit keeps stale settings")
	}
}

// With a docker group, the daemon joins it to reach Docker's socket; without one the unit stays as
// it is embedded, since systemd won't start a unit whose group doesn't exist.
func TestDaemonJoinsTheDockerGroup(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.path("/etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.path("/etc/group"), []byte("root:x:0:\ndockerish:x:998:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := e.in.Daemon(ctx, daemonOpts); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.read(t, "/etc/systemd/system/tenderd.service"), "SupplementaryGroups") {
		t.Error("the unit joins a docker group this machine doesn't have")
	}

	if err := os.WriteFile(e.path("/etc/group"), []byte("root:x:0:\ndocker:x:999:johan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.in.Refreshing = true // "tender install refresh", after Docker was installed
	if err := e.in.Daemon(ctx, DaemonOptions{}); err != nil {
		t.Fatal(err)
	}
	unit := e.read(t, "/etc/systemd/system/tenderd.service")
	if !strings.Contains(unit, "Group=tender\n# Docker's socket. The docker group is root-equivalent") ||
		strings.Count(unit, "SupplementaryGroups=docker\n") != 1 {
		t.Errorf("unit:\n%s", unit)
	}
}

// With a systemd-journal group the daemon joins it too, to read the whole journal; with both
// groups the unit has one SupplementaryGroups line.
func TestDaemonJoinsTheJournalGroup(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.path("/etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, c := range []struct{ groups, want string }{
		{"root:x:0:\nsystemd-journal:x:101:\n", "SupplementaryGroups=systemd-journal\n"},
		{"root:x:0:\ndocker:x:999:\nsystemd-journal:x:101:\n", "SupplementaryGroups=docker systemd-journal\n"},
	} {
		if err := os.WriteFile(e.path("/etc/group"), []byte(c.groups), 0o644); err != nil {
			t.Fatal(err)
		}
		e.in.Refreshing = false
		opts := daemonOpts
		if _, err := os.Stat(e.path("/etc/systemd/system/tenderd.service")); err == nil {
			e.in.Refreshing, opts = true, DaemonOptions{}
		}
		if err := e.in.Daemon(ctx, opts); err != nil {
			t.Fatal(err)
		}
		unit := e.read(t, "/etc/systemd/system/tenderd.service")
		if strings.Count(unit, "SupplementaryGroups=") != 1 || !strings.Contains(unit, c.want) || !strings.Contains(unit, "# The whole journal") {
			t.Errorf("with %q, unit:\n%s", c.groups, unit)
		}
	}
}
