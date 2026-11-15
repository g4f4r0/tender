package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/services"
)

const (
	secretKeyID  = "AKIA-TEST-KEY-ID"
	secretKey    = "test-secret-access-key-value"
	secretPasswd = "test-repository-password"
)

func mustRun(t *testing.T, h *fakeHub, a *answers, args ...string) string {
	t.Helper()
	out, err := runCommand(t, h, a, args...)
	if err != nil {
		t.Fatalf("tender %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func mustFail(t *testing.T, h *fakeHub, a *answers, want string, args ...string) {
	t.Helper()
	out, err := runCommand(t, h, a, args...)
	if err == nil || err.Error() != want {
		t.Errorf("tender %s: error %v, want %q\n%s", strings.Join(args, " "), err, want, out)
	}
}

func TestMachines(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	if out := mustRun(t, h, a, "machines", "list"); out != "No machines yet. Add one with: tender machines add NAME\n" {
		t.Errorf("empty list = %q", out)
	}

	out := mustRun(t, h, a, "machines", "add", "web-1")
	for _, want := range []string{"Added machine web-1.", "shown only now", "  token-web-1-1\n", "install.sh \\\n",
		"daemon --hub HUB_URL --machine web-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("add output lacks %q:\n%s", want, out)
		}
	}
	// Flags may come before or after the name.
	mustRun(t, h, a, "machines", "add", "--hub", h.url(), "db-1")

	ls := mustRun(t, h, a, "machines", "list")
	want := "NAME    LAST SEEN   MEMORY   DISK   VERSION\nweb-1   never       -        -      -\ndb-1    never       -        -      -\n"
	if ls != want {
		t.Errorf("list =\n%s\nwant\n%s", ls, want)
	}
	seen, usage := h.now.Add(-5*time.Minute), &machines.Usage{MemoryUsedPercent: 41.4, DiskUsedPercent: 63.6}
	h.machines["web-1"].seen, h.machines["web-1"].usage = &seen, usage
	if ls := mustRun(t, h, a, "machines", "list"); !strings.Contains(ls, "web-1   5 minutes ago   41%      64%") {
		t.Errorf("list with a reading =\n%s", ls)
	}

	out = mustRun(t, h, a, "machines", "rotate-token", "web-1")
	if !strings.Contains(out, "  token-web-1-1-rotated\n") || !strings.Contains(out, "The old one no longer works") ||
		!strings.Contains(out, "sudo tender install daemon --token-file PATH") {
		t.Errorf("rotate output:\n%s", out)
	}
	mustFail(t, h, a, `this removes db-1 and its history from the hub, with no undo. Run "tender machines remove db-1 --yes" to confirm`, "machines", "remove", "db-1")
	if h.machines["db-1"] == nil {
		t.Fatal("remove without --yes removed the machine")
	}
	if out := mustRun(t, h, a, "machines", "remove", "db-1", "--yes"); !strings.Contains(out, "Removed machine db-1 and its history.") {
		t.Errorf("remove output = %q", out)
	}
	if h.machines["db-1"] != nil {
		t.Error("the machine is still on the hub")
	}

	// The hub's sentence is the error, as it came.
	mustFail(t, h, a, `Machine "web-1" already exists. Choose another name.`, "machines", "add", "web-1")
	mustFail(t, h, a, `Machine "nope" doesn't exist. List the machines with "tender machines list".`, "machines", "remove", "nope", "--yes")
	mustFail(t, h, a, `Machine "nope" doesn't exist. List the machines with "tender machines list".`, "machines", "rotate-token", "nope")
	mustFail(t, h, a, `machines add needs a machine name. Run "tender machines add --help" for usage.`, "machines", "add")
	mustFail(t, h, a, `machines add takes one argument, got 2: a b. Run "tender machines add --help" for usage.`, "machines", "add", "a", "b")
	for _, r := range h.sent() {
		if r.Auth != "" {
			t.Errorf("%s %s sent an Authorization header %q; people routes are open", r.Method, r.Path, r.Auth)
		}
	}
}

func TestMachinesAddPrintsTheHubAddressWhenOtherMachinesCanReachIt(t *testing.T) {
	for hub, want := range map[string]string{
		"http://localhost:8080":      "HUB_URL",
		"http://[::1]:8080":          "HUB_URL",
		"https://hub.example.ts.net": "https://hub.example.ts.net",
	} {
		if got := daemonHubURL(hub); got != want {
			t.Errorf("daemonHubURL(%s) = %s, want %s", hub, got, want)
		}
	}
}

func TestDestinationsAskForSecretsWithEchoOff(t *testing.T) {
	h := newFakeHub(t)
	// The password is left empty, so the hub makes one.
	a := &answers{left: []string{secretKeyID, secretKey, ""}}
	out := mustRun(t, h, a, "destinations", "add", "r2", "--endpoint", "https://acct.r2.example", "--bucket", "backups", "--path", "tender")

	if got := strings.Join(a.labels, "|"); got != "Access key ID|Secret access key|Repository password, or empty for the hub to make one" {
		t.Errorf("asked for %q", got)
	}
	for _, secret := range []string{secretKeyID, secretKey} {
		if strings.Contains(out, secret) {
			t.Errorf("the output echoes a secret:\n%s", out)
		}
	}
	for _, want := range []string{"Added destination r2.", "made by the hub and shown only now", "  generated-repository-password\n",
		"Save it in a password manager now.", "cannot be restored", "Tender cannot show it again."} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "generated-repository-password"); n != 1 {
		t.Errorf("the generated password is printed %d times, want once", n)
	}
	var body backups.DestinationInput
	reqs := h.sent()
	if err := json.Unmarshal([]byte(reqs[len(reqs)-1].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Config["access_key_id"] != secretKeyID || body.Config["secret_access_key"] != secretKey || body.Password != "" || body.Type != "s3" || body.Config["path"] != "tender" {
		t.Errorf("the hub got %+v", body)
	}

	ls := mustRun(t, h, a, "destinations", "list")
	if want := "NAME   TYPE   LOCATION\nr2     s3     s3:https://acct.r2.example/backups/tender\n"; ls != want {
		t.Errorf("list =\n%s\nwant\n%s", ls, want)
	}
	for _, secret := range []string{secretKeyID, secretKey, "generated"} {
		if strings.Contains(ls, secret) {
			t.Errorf("the list shows a secret:\n%s", ls)
		}
	}
}

func TestDestinationsAddWithYourOwnPasswordPrintsNoPassword(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{left: []string{secretKeyID, secretKey, secretPasswd}}
	out := mustRun(t, h, a, "destinations", "add", "r2", "--endpoint", "https://acct.r2.example", "--bucket", "backups")
	if out != "Added destination r2.\n" {
		t.Errorf("output = %q", out)
	}
	if h.secrets["r2"].Password != secretPasswd {
		t.Errorf("the hub got password %q", h.secrets["r2"].Password)
	}
}

func TestDestinationsAddFromFilesNeedsNoTerminal(t *testing.T) {
	h := newFakeHub(t)
	dir := t.TempDir()
	files := map[string]string{"id": secretKeyID + "\n", "key": secretKey + "\n", "pw": secretPasswd + "\n"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := &answers{} // no terminal: any prompt fails
	out := mustRun(t, h, a, "destinations", "add", "r2", "--endpoint", "https://acct.r2.example", "--bucket", "backups",
		"--access-key-id-file", filepath.Join(dir, "id"), "--secret-access-key-file", filepath.Join(dir, "key"), "--password-file", filepath.Join(dir, "pw"))
	if len(a.labels) != 0 {
		t.Errorf("asked for %v although files were given", a.labels)
	}
	if got := h.secrets["r2"]; got.Config["access_key_id"] != secretKeyID || got.Config["secret_access_key"] != secretKey || got.Password != secretPasswd {
		t.Errorf("the hub got %+v", got)
	}
	if out != "Added destination r2.\n" {
		t.Errorf("output = %q", out)
	}

	// Keys from files and no terminal for the password: the hub makes one.
	out = mustRun(t, h, a, "destinations", "add", "b2", "--endpoint", "https://b2.example", "--bucket", "x",
		"--access-key-id-file", filepath.Join(dir, "id"), "--secret-access-key-file", filepath.Join(dir, "key"))
	if !strings.Contains(out, "  generated-repository-password\n") {
		t.Errorf("output = %q", out)
	}
}

func TestDestinationsAddErrors(t *testing.T) {
	h := newFakeHub(t)
	noTerminal := &answers{}
	mustFail(t, h, noTerminal, "there is no terminal to ask for the access key ID. Save it in a file and add --access-key-id-file PATH.",
		"destinations", "add", "r2", "--endpoint", "https://e.example", "--bucket", "b")
	mustFail(t, h, &answers{left: []string{secretKeyID}}, "there is no terminal to ask for the secret access key. Save it in a file and add --secret-access-key-file PATH.",
		"destinations", "add", "r2", "--endpoint", "https://e.example", "--bucket", "b")
	mustFail(t, h, &answers{left: []string{"", ""}}, `the access key ID and the secret access key cannot be empty. Run "tender destinations add --help" for usage.`,
		"destinations", "add", "r2", "--endpoint", "https://e.example", "--bucket", "b")
	mustFail(t, h, noTerminal, `destinations add needs --endpoint and --bucket. Run "tender destinations add --help" for usage.`,
		"destinations", "add", "r2")
	mustFail(t, h, noTerminal, `cannot read /no/such/file. Check the path and that you can read it: open /no/such/file: no such file or directory`,
		"destinations", "add", "r2", "--endpoint", "https://e.example", "--bucket", "b", "--access-key-id-file", "/no/such/file")
	// A secret is never an argument.
	mustFail(t, h, noTerminal, `unknown option --secret-access-key. Run "tender destinations add --help" for usage.`,
		"destinations", "add", "r2", "--endpoint", "https://e.example", "--bucket", "b", "--secret-access-key", "x")
	if len(h.sent()) != 0 {
		t.Error("a failed add reached the hub")
	}

	// The hub's own refusals come through.
	ok := &answers{left: []string{secretKeyID, secretKey, "pw", secretKeyID, secretKey, "pw"}}
	mustRun(t, h, ok, "destinations", "add", "r2", "--endpoint", "https://e.example", "--bucket", "b")
	mustFail(t, h, ok, `Destination "r2" already exists. Choose another name.`, "destinations", "add", "r2", "--endpoint", "https://e.example", "--bucket", "b")
}

func TestDestinationsRemove(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{left: []string{"k", "s", "p"}}
	mustRun(t, h, a, "destinations", "add", "r2", "--endpoint", "https://e.example", "--bucket", "b")
	mustRun(t, h, a, "machines", "add", "web-1")
	mustRun(t, h, a, "backups", "set", "web-1", "--destination", "r2", "--schedule", "30 3 * * *", "--retention-days", "30")
	mustFail(t, h, a, `Destination "r2" is used by web-1. Run "tender backups unset web-1" first.`, "destinations", "remove", "r2")
	mustRun(t, h, a, "backups", "unset", "web-1")
	if out := mustRun(t, h, a, "destinations", "remove", "r2"); out != "Removed destination r2. The backups already in it stay there.\n" {
		t.Errorf("output = %q", out)
	}
	if out := mustRun(t, h, a, "destinations", "list"); !strings.HasPrefix(out, "No destinations yet.") {
		t.Errorf("list = %q", out)
	}
}

func TestBackups(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{left: []string{"k", "s", "p"}}
	mustRun(t, h, a, "destinations", "add", "r2", "--endpoint", "https://e.example", "--bucket", "b")
	mustRun(t, h, a, "machines", "add", "web-1")
	mustRun(t, h, a, "machines", "add", "db-1")

	out := mustRun(t, h, a, "backups", "status")
	if !strings.Contains(out, "web-1     not set") || !strings.Contains(out, "web-1 has no backup settings. Set them with: tender backups set web-1 --destination NAME") {
		t.Errorf("status before set =\n%s", out)
	}

	out = mustRun(t, h, a, "backups", "set", "web-1", "--destination", "r2", "--schedule", "30 3 * * *", "--retention-days", "30")
	if out != "web-1 backs up to r2 at \"30 3 * * *\" (UTC) and keeps 30 days.\n" {
		t.Errorf("set output = %q", out)
	}
	h.history["web-1"] = &backups.History{Type: backups.TypeBackup, Status: backups.StatusCompleted, CreatedAt: h.now.Add(-3 * time.Hour)}
	want := "MACHINE   DESTINATION   SCHEDULE     KEEPS     LAST BACKUP   STATUS      SNAPSHOTS\n" +
		"web-1     r2            30 3 * * *   30 days   3 hours ago   completed   3\n" +
		"db-1      not set       -            -         never         -           0\n" +
		"\ndb-1 has no backup settings. Set them with: tender backups set db-1 --destination NAME --schedule \"30 3 * * *\" --retention-days 30\n"
	if got := mustRun(t, h, a, "backups", "status"); got != want {
		t.Errorf("status =\n%s\nwant\n%s", got, want)
	}
	one := mustRun(t, h, a, "backups", "status", "web-1")
	if !strings.Contains(one, "web-1     r2") || strings.Contains(one, "db-1") {
		t.Errorf("status web-1 =\n%s", one)
	}

	body := ""
	for _, r := range h.sent() {
		if r.Method == "PUT" {
			body = r.Body
		}
	}
	if body != `{"destination":"r2","schedule":"30 3 * * *","retention_days":30}`+"\n" && body != `{"destination":"r2","schedule":"30 3 * * *","retention_days":30}` {
		t.Errorf("PUT body = %s", body)
	}

	if out := mustRun(t, h, a, "backups", "unset", "web-1"); out != "web-1 no longer backs up. Its backups stay in the destination.\n" {
		t.Errorf("unset output = %q", out)
	}
	mustFail(t, h, a, `Destination "nope" doesn't exist. Add it with "tender destinations add".`, "backups", "set", "web-1", "--destination", "nope", "--schedule", "30 3 * * *", "--retention-days", "30")
	mustFail(t, h, a, `The schedule must have five cron fields, like "30 3 * * *".`, "backups", "set", "web-1", "--destination", "r2", "--schedule", "often", "--retention-days", "30")
	mustFail(t, h, a, `backups set needs --destination, --schedule and --retention-days (1 or more). Run "tender backups set --help" for usage.`, "backups", "set", "web-1", "--destination", "r2")
	mustFail(t, h, a, `Machine "nope" doesn't exist. List the machines with "tender machines list".`, "backups", "status", "nope")
}

func TestStatus(t *testing.T) {
	h := newFakeHub(t)
	a := &answers{}
	if out := mustRun(t, h, a, "status"); out != "No machines yet. Add one with: tender machines add NAME\n\nNo open problems.\n" {
		t.Errorf("empty status = %q", out)
	}
	mustRun(t, h, a, "machines", "add", "web-1")
	mustRun(t, h, a, "machines", "add", "db-1")
	seen := h.now.Add(-90 * time.Second)
	h.machines["web-1"].seen = &seen
	h.history["web-1"] = &backups.History{Type: backups.TypeBackup, Status: backups.StatusFailed, CreatedAt: h.now.Add(-26 * time.Hour)}
	web1, db1 := "web-1", "db-1"
	long := "The last backup on web-1 failed at 2026-11-13 10:00 UTC: " + strings.Repeat("restic said no. ", 10)
	h.alerts = []notifications.Notification{
		{ID: 4, Machine: &db1, Event: "backup.stale", Severity: "warning", Status: "pending", Message: "db-1 has never completed a backup.", CreatedAt: h.now.Add(-30 * time.Minute)},
		{ID: 3, Machine: &web1, Event: "backup.failed.escalated", Severity: "critical", Status: "sent", Message: long, CreatedAt: h.now.Add(-2 * time.Hour)},
		{ID: 2, Machine: &web1, Event: "machine.silent.resolved", Severity: "critical", Status: "sent", Message: "Resolved: old news", CreatedAt: h.now.Add(-8 * time.Hour)},
		{ID: 1, Machine: &web1, Event: "machine.silent", Severity: "critical", Status: "sent", Message: "old news", CreatedAt: h.now.Add(-9 * time.Hour)},
	}
	h.pageSize = 1 // one notification per page: status must follow next_cursor
	h.addServices(t, "web-1", lone("app", services.StateRunning), lone("worker", services.StateExited),
		lone("cron", services.StateRunning), lone("gone", services.StateRemoved))
	h.addServices(t, "db-1")
	h.docker["db-1"] = services.DockerUnreachable
	out := mustRun(t, h, a, "status")
	for _, want := range []string{
		"MACHINE   LAST SEEN      LAST BACKUP   STATUS   SERVICES\n",
		"web-1     1 minute ago   1 day ago     failed   2/3 running, 3 containers\n",
		"db-1      never          never         -        Docker unreachable\n",
		"Open problems: 2\n",
		"warning    30 minutes ago   backup.stale    db-1 has never completed a backup.\n",
		"critical   2 hours ago      backup.failed   The last backup on web-1 failed at 2026-11-13 10:00 UTC: restic said no. restic said no. restic s...\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "old news") {
		t.Error("a resolved problem is shown as open")
	}

	// A hub from before services has no /v1/services: the status leaves that column out.
	h.olderHub = true
	out = mustRun(t, h, a, "status")
	if !strings.Contains(out, "MACHINE   LAST SEEN      LAST BACKUP   STATUS\n") || !strings.Contains(out, "web-1     1 minute ago   1 day ago     failed\n") ||
		strings.Contains(out, "SERVICES") {
		t.Errorf("status from an older hub:\n%s", out)
	}
}

func TestHubThatDoesNotAnswer(t *testing.T) {
	h := newFakeHub(t)
	url := h.url()
	h.srv.Close()
	t.Setenv("TENDER_HUB", url)
	var out bytes.Buffer
	err := runStatus(nil, session{out: &out, now: time.Now})
	want := "cannot reach the hub at " + url + ". Check that the hub is running (systemctl status tenderhub) and that --hub or TENDER_HUB has its address."
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q", out.String())
	}
}

func TestHubAddressFlagBeatsEnvironmentAndIsChecked(t *testing.T) {
	good := newFakeHub(t)
	t.Setenv("TENDER_HUB", "http://127.0.0.1:1")
	var out bytes.Buffer
	if err := runMachines([]string{"list", "--hub", good.url() + "/"}, session{out: &out, now: time.Now}); err != nil {
		t.Fatalf("--hub did not override TENDER_HUB: %v", err)
	}
	err := runMachines([]string{"list", "--hub", "hub.example.net"}, session{out: &out, now: time.Now})
	if err == nil || !strings.Contains(err.Error(), `the hub address "hub.example.net" is not an http or https URL, such as http://127.0.0.1:8080.`) {
		t.Errorf("bad address error = %v", err)
	}
}

func TestHubErrorWithoutAProblemBody(t *testing.T) {
	srv := httptest.NewServer(nil) // the default mux answers 404 text/plain
	defer srv.Close()
	err := runMachines([]string{"list", "--hub", srv.URL}, session{out: &bytes.Buffer{}, now: time.Now})
	if err == nil || !strings.Contains(err.Error(), "answered 404 Not Found. Look at its log: journalctl -u tenderhub.") {
		t.Errorf("error = %v", err)
	}
}

func TestGroupsPrintUsage(t *testing.T) {
	for _, group := range []string{"machines", "destinations", "backups", "channels", "sinks"} {
		var out bytes.Buffer
		run := hubCommands[group]
		if err := run(nil, session{out: &out}); !errors.Is(err, errUsageShown) || !strings.HasPrefix(out.String(), "Usage: tender "+group+" COMMAND\n") {
			t.Errorf("tender %s: err %v, output %q", group, err, out.String())
		}
		out.Reset()
		if err := run([]string{"--help"}, session{out: &out}); err != nil || !strings.Contains(out.String(), "Commands:") {
			t.Errorf("tender %s --help: err %v", group, err)
		}
		if err := run([]string{"bogus"}, session{out: &out}); err == nil || err.Error() != `unknown `+group+` command "bogus". Run "tender `+group+` --help" for the commands.` {
			t.Errorf("tender %s bogus: %v", group, err)
		}
	}
}

func TestEveryHubCommandHasUsageThatFits(t *testing.T) {
	for _, args := range [][]string{
		{"status"}, {"machines", "list"}, {"machines", "add"}, {"machines", "remove"}, {"machines", "rotate-token"},
		{"destinations", "list"}, {"destinations", "add"}, {"destinations", "update"}, {"destinations", "test"}, {"destinations", "remove"},
		{"sinks", "list"}, {"sinks", "add"}, {"sinks", "show"}, {"sinks", "update"}, {"sinks", "remove"},
		{"backups", "status"}, {"backups", "set"}, {"backups", "unset"},
		{"channels", "list"}, {"channels", "add"}, {"channels", "show"}, {"channels", "update"}, {"channels", "remove"}, {"channels", "test"},
	} {
		var out bytes.Buffer
		err := hubCommands[args[0]](append(args[1:], "--help"), session{out: &out})
		if !errors.Is(err, flag.ErrHelp) {
			t.Errorf("tender %s --help returned %v", strings.Join(args, " "), err)
		}
		text := out.String()
		if !strings.HasPrefix(text, "Usage: tender "+strings.Join(args, " ")) || !strings.Contains(text, "--hub URL") {
			t.Errorf("usage of %v:\n%s", args, text)
		}
		for _, line := range strings.Split(text, "\n") {
			if len(line) > 99 {
				t.Errorf("tender %v: line over 99 characters: %q", args, line)
			}
		}
	}
	for _, text := range []string{machinesUsage, destinationsUsage, backupsUsage, channelsUsage, sinksUsage, usage} {
		for _, line := range strings.Split(text, "\n") {
			if len(line) > 99 {
				t.Errorf("line over 99 characters: %q", line)
			}
		}
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		-time.Minute: "just now", 30 * time.Second: "just now", time.Minute: "1 minute ago", 59 * time.Minute: "59 minutes ago",
		time.Hour: "1 hour ago", 23 * time.Hour: "23 hours ago", 24 * time.Hour: "1 day ago", 72 * time.Hour: "3 days ago",
	} {
		if got := ago(now, now.Add(-d)); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"one line.":                         "one line.",
		"first\nsecond":                     "first...",
		strings.Repeat("a", 100):            strings.Repeat("a", 100),
		strings.Repeat("a", 101):            strings.Repeat("a", 97) + "...",
		"  padded  ":                        "padded",
		"héllo " + strings.Repeat("é", 120): "héllo " + strings.Repeat("é", 91) + "...",
	} {
		if got := oneLine(in); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPositionalArgumentsMayComeAnywhere(t *testing.T) {
	fs := newFlagSet("machines add", "NAME", "x")
	fs.String("hub", "", "")
	rest, err := parseWithArgs(fs, []string{"a", "--hub", "u", "b"}, &bytes.Buffer{})
	if err != nil || strings.Join(rest, ",") != "a,b" {
		t.Errorf("rest = %v, %v", rest, err)
	}
}
