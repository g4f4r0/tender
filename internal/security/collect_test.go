package security

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/config"
)

// fakes puts shell scripts on PATH, by name, and nothing else. A script prints its body's output.
func fakes(t *testing.T, scripts map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The scripts need sh and cat; keep only those from the real PATH.
	bin := t.TempDir()
	for _, tool := range []string{"sh", "cat"} {
		for _, d := range filepath.SplitList(os.Getenv("PATH")) {
			if p := filepath.Join(d, tool); fileExists(p) {
				if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+bin)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func rootWith(t *testing.T, osRelease string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if osRelease != "" {
		if err := os.WriteFile(filepath.Join(root, "etc/os-release"), []byte(osRelease), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const ubuntu = "NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\n"

const ssOut = `udp   UNCONN 0      0           127.0.0.53%lo:53         0.0.0.0:*    users:(("systemd-resolve",pid=1,fd=3))
udp   UNCONN 0      0                 0.0.0.0:41641      0.0.0.0:*    users:(("tailscaled",pid=2,fd=4))
udp   UNCONN 0      0                    [::]:41641         [::]:*    users:(("tailscaled",pid=2,fd=5))
tcp   LISTEN 0      4096       100.123.64.92%tailscale0:35961   0.0.0.0:*
tcp   LISTEN 0      4096            127.0.0.1:8080       0.0.0.0:*    users:(("tender",pid=3,fd=7))
tcp   LISTEN 0      4096              0.0.0.0:22         0.0.0.0:*    users:(("sshd",pid=4,fd=3))
tcp   LISTEN 0      4096                 [::]:22            [::]:*    users:(("sshd",pid=4,fd=4))
tcp   LISTEN 0      4096      [fd7a:115c:a1e0::cc37:405d]%tailscale0:51275   [::]:*
tcp   LISTEN 0      4096                    *:8443             *:*    users:(("caddy",pid=5,fd=9))
tcp   LISTEN 0      4096              0.0.0.0:8080       0.0.0.0:*    users:(("docker-proxy",pid=6,fd=4))
tcp   LISTEN 0      4096          [fe80::1%eth0]:546         [::]:*
`

const aptOut = `NOTE: This is only a simulation!
Inst libssl3 [3.0.13-0ubuntu3.4] (3.0.13-0ubuntu3.5 Ubuntu:26.04/resolute-security [amd64])
Inst openssh-server [1:9.6p1-3ubuntu13.5] (1:9.6p1-3ubuntu13.6 Ubuntu:26.04/resolute-updates, Ubuntu:26.04/resolute-security [amd64])
Inst vim [2:9.1.0016-1ubuntu7.3] (2:9.1.0016-1ubuntu7.4 Ubuntu:26.04/resolute-updates [amd64])
Conf libssl3 (3.0.13-0ubuntu3.5 Ubuntu:26.04/resolute-security [amd64])
`

func TestCollectOnUbuntu(t *testing.T) {
	fakes(t, map[string]string{
		"ufw":                "cat <<'EOF'\nStatus: active\nLogging: on (low)\nDefault: deny (incoming), allow (outgoing), disabled (routed)\nEOF",
		"ss":                 "cat <<'EOF'\n" + ssOut + "EOF",
		"sshd":               `[ "$1" = -T ] && printf 'port 22\npasswordauthentication no\npermitrootlogin without-password\n'`,
		"apt-get":            "cat <<'EOF'\n" + aptOut + "EOF",
		"apt-config":         `echo "UU='1'"`,
		"unattended-upgrade": "exit 0",
	})
	root := rootWith(t, ubuntu)
	if err := os.MkdirAll(filepath.Join(root, "var/run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "var/run/reboot-required"), []byte("*** System restart required ***\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(filepath.Join(root, "var/run/reboot-required"), old, old); err != nil {
		t.Fatal(err)
	}
	c := &Collector{Root: root, Published: func(context.Context) ([]Published, error) {
		return []Published{
			{Protocol: "tcp", Address: "0.0.0.0", Port: 8080, Service: "web"},
			{Protocol: "tcp", Address: "0.0.0.0", Port: 5432, Service: "db"}, // no docker-proxy: Docker forwards it in the kernel
			{Protocol: "tcp", Address: "127.0.0.1", Port: 9000, Service: "minio"},
		}, nil
	}}
	r := c.Collect(context.Background())

	if r.Firewall.Check != CheckOK || *r.Firewall.Tool != "ufw" || !r.Firewall.Active || *r.Firewall.DefaultIncoming != "deny" {
		t.Errorf("firewall = %+v", r.Firewall)
	}
	var keys []string
	for _, l := range r.Listeners.Ports {
		keys = append(keys, l.Key()+"@"+l.Address)
	}
	want := []string{"41641/udp@0.0.0.0", "22/tcp@0.0.0.0", "8443/tcp@0.0.0.0", "8080/tcp@0.0.0.0", "5432/tcp@0.0.0.0"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("listeners = %v, want %v", keys, want)
	}
	for _, l := range r.Listeners.Ports {
		switch l.Port {
		case 8080, 5432:
			if !l.Docker || l.Service == nil {
				t.Errorf("%s is Docker's: %+v", l.Key(), l)
			}
		case 22:
			if l.Docker || l.Process == nil || *l.Process != "sshd" {
				t.Errorf("sshd: %+v", l)
			}
		}
	}
	if !r.SSH.Installed || *r.SSH.PasswordAuthentication || *r.SSH.PermitRootLogin != "without-password" {
		t.Errorf("ssh = %+v", r.SSH)
	}
	if r.Updates != (Updates{Check: CheckOK, Pending: 3, Security: 2, UnattendedUpgrades: true}) {
		t.Errorf("updates = %+v", r.Updates)
	}
	if !r.Reboot.Required || r.Reboot.Since == nil || !r.Reboot.Since.Equal(old) {
		t.Errorf("reboot = %+v", r.Reboot)
	}
}

// A DHCP client's socket on the public address, as systemd-networkd's, is not an open port. Only
// UDP 68 and 546 are left out: a TCP socket on those ports is still listed.
func TestParseSSLeavesOutDHCPClients(t *testing.T) {
	out := `udp   UNCONN 0      0      167.104.162.114%eth0:68        0.0.0.0:*    users:(("systemd-network",pid=7,fd=19))
udp   UNCONN 0      0                 0.0.0.0:68         0.0.0.0:*    users:(("dhclient",pid=8,fd=6))
udp   UNCONN 0      0      [2a01:4f8::1]%eth0:546          [::]:*    users:(("systemd-network",pid=7,fd=20))
udp   UNCONN 0      0                 0.0.0.0:67         0.0.0.0:*    users:(("dnsmasq",pid=9,fd=4))
tcp   LISTEN 0      4096              0.0.0.0:546        0.0.0.0:*    users:(("odd",pid=10,fd=3))
`
	var keys []string
	for _, l := range parseSS([]byte(out)) {
		keys = append(keys, l.Key())
	}
	if want := []string{"67/udp", "546/tcp"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("listeners = %v, want %v", keys, want)
	}
}

func TestCollectWithoutUfw(t *testing.T) {
	for name, c := range map[string]struct {
		scripts map[string]string
		tool    string
		active  bool
		policy  string
	}{
		"nft drop": {map[string]string{"nft": `echo '{"nftables":[{"metainfo":{}},{"chain":{"family":"inet","table":"filter","name":"input","hook":"input","prio":0,"policy":"drop"}},{"chain":{"family":"inet","table":"filter","name":"output","hook":"output","policy":"accept"}}]}'`},
			"nftables", true, "deny"},
		"nft accept": {map[string]string{"nft": `echo '{"nftables":[{"chain":{"family":"inet","table":"filter","name":"input","hook":"input","policy":"accept"}}]}'`},
			"nftables", true, "allow"},
		"nft empty":     {map[string]string{"nft": `echo '{"nftables":[{"metainfo":{}}]}'`}, "nftables", false, ""},
		"iptables drop": {map[string]string{"iptables": `printf -- '-P INPUT DROP\n-A INPUT -i lo -j ACCEPT\n'`}, "iptables", true, "deny"},
		"no firewall":   {map[string]string{}, "none", false, ""},
		"ufw inactive":  {map[string]string{"ufw": `echo "Status: inactive"`}, "ufw", false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			fakes(t, c.scripts)
			f := firewall(context.Background())
			if f.Check != CheckOK || *f.Tool != c.tool || f.Active != c.active || (c.policy == "") != (f.DefaultIncoming == nil) ||
				(f.DefaultIncoming != nil && *f.DefaultIncoming != c.policy) {
				t.Errorf("firewall = %+v", f)
			}
		})
	}
}

// A check that can't run says why, and the others still run.
func TestCollectErrors(t *testing.T) {
	fakes(t, map[string]string{
		"ufw":     `echo "ERROR: You need to be root to run this script" >&2; exit 1`,
		"ss":      "exit 2",
		"apt-get": `echo "E: Could not open lock file" >&2; exit 100`,
	})
	r := (&Collector{Root: rootWith(t, ubuntu)}).Collect(context.Background())
	if r.Firewall.Check != CheckError || !strings.Contains(*r.Firewall.Error, "You need to be root") {
		t.Errorf("firewall = %+v", r.Firewall)
	}
	if r.Listeners.Check != CheckError || r.Listeners.Ports == nil {
		t.Errorf("listeners = %+v", r.Listeners)
	}
	if r.Updates.Check != CheckError || !strings.Contains(*r.Updates.Error, "Could not open lock file") {
		t.Errorf("updates = %+v", r.Updates)
	}
	if r.SSH.Check != CheckOK || r.SSH.Installed {
		t.Errorf("a machine without sshd has no SSH server: %+v", r.SSH)
	}
	if r.Reboot.Check != CheckOK || r.Reboot.Required {
		t.Errorf("reboot = %+v", r.Reboot)
	}
}

// On a system that isn't Debian or Ubuntu every check says unsupported, and nothing runs.
func TestCollectUnsupported(t *testing.T) {
	fakes(t, map[string]string{"ufw": "touch ran; echo 'Status: active'", "ss": "touch ran"})
	for _, release := range []string{"ID=fedora\nID_LIKE=\"rhel centos\"\n", ""} {
		r := (&Collector{Root: rootWith(t, release)}).Collect(context.Background())
		for name, check := range map[string]string{"firewall": r.Firewall.Check, "listeners": r.Listeners.Check, "ssh": r.SSH.Check,
			"updates": r.Updates.Check, "reboot": r.Reboot.Check} {
			if check != CheckUnsupported {
				t.Errorf("%q: %s = %s", release, name, check)
			}
		}
	}
	if fileExists("ran") {
		t.Error("a check ran on an unsupported system")
	}
}

func TestUnattendedOff(t *testing.T) {
	fakes(t, map[string]string{"apt-get": "true", "apt-config": `echo "UU='0'"`, "unattended-upgrade": "true"})
	if u := updates(context.Background()); u.UnattendedUpgrades || u.Pending != 0 {
		t.Errorf("updates = %+v", u)
	}
	fakes(t, map[string]string{"apt-get": "true", "apt-config": `echo "UU='1'"`})
	if u := updates(context.Background()); u.UnattendedUpgrades {
		t.Errorf("without unattended-upgrade installed it is off: %+v", u)
	}
}

// The check counts what the upgrade installs: apt-get upgrade --with-new-pkgs, which takes new
// packages such as a kernel but never removes one.
func TestUpdatesCountLikeTheUpgrade(t *testing.T) {
	args := filepath.Join(t.TempDir(), "args")
	fakes(t, map[string]string{"apt-get": `echo "$@" >` + args, "apt-config": `echo "UU='1'"`})
	updates(context.Background())
	b, _ := os.ReadFile(args)
	if got := strings.TrimSpace(string(b)); !strings.HasSuffix(got, " upgrade --with-new-pkgs") || !strings.HasPrefix(got, "-s ") {
		t.Errorf("apt-get %s", got)
	}
}

func TestSSHWeak(t *testing.T) {
	fakes(t, map[string]string{"sshd": `printf 'passwordauthentication yes\npermitrootlogin yes\n'`})
	s := (&Collector{Root: t.TempDir()}).sshd(context.Background())
	if !*s.PasswordAuthentication || *s.PermitRootLogin != "yes" {
		t.Errorf("ssh = %+v", s)
	}
}

// upgradeFakes puts apt-get, unattended-upgrade and the fake systemd in place, and returns the fake
// systemd's folder.
func upgradeFakes(t *testing.T, scripts map[string]string) string {
	t.Helper()
	fakes(t, scripts)
	bin, err := filepath.Abs("../config/testdata/fakesystemd")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("FAKE_SYSTEMD_DIR", dir)
	t.Setenv("TENDER_TEST_SYSTEMD_RUN", filepath.Join(bin, "systemd-run"))
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	return dir
}

// The upgrade runs in the tenderupgrade transient unit, with fixed arguments, without --wait or
// --pipe: its output goes to a file in the state folder, and systemctl show says how it ended.
func TestUpgradeRunsInATransientUnit(t *testing.T) {
	sd := upgradeFakes(t, map[string]string{
		"apt-get":            `[ "$1" = update ] && echo "Hit:1 http://archive.ubuntu.com/ubuntu noble InRelease" || echo "1 upgraded"`,
		"unattended-upgrade": `echo "All upgrades installed"`,
	})
	state := t.TempDir()
	ctx := context.Background()
	for packages, want := range map[string]string{
		"security": "systemd-run --unit tenderupgrade DEBIAN_FRONTEND=noninteractive LC_ALL=C /bin/sh -c apt-get update -q && exec unattended-upgrade -v",
		"all": "systemd-run --unit tenderupgrade DEBIAN_FRONTEND=noninteractive LC_ALL=C /bin/sh -c apt-get update -q && exec apt-get -y " +
			"-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade --with-new-pkgs",
	} {
		out, err := Upgrade(ctx, state, packages, false)
		if err != nil || !strings.Contains(out, "Hit:1") || (packages == "security") != strings.Contains(out, "All upgrades installed") {
			t.Fatalf("%s: %q %v", packages, out, err)
		}
		if b, _ := os.ReadFile(filepath.Join(sd, "log")); !strings.Contains(string(b), want+"\n") {
			t.Errorf("%s ran:\n%s", packages, b)
		}
		if u, _ := config.ShowUnit(ctx, UpgradeUnit); u.Exists() {
			t.Errorf("%s left its unit: %+v", packages, u)
		}
	}
	if _, err := Upgrade(ctx, state, "some", false); err == nil {
		t.Error("an unknown packages value ran")
	}
	upgradeFakes(t, map[string]string{"apt-get": "true", "unattended-upgrade": `echo "E: dpkg was interrupted"; exit 1`})
	if out, err := Upgrade(ctx, state, "security", false); err == nil || !strings.Contains(err.Error(), "dpkg was interrupted") || !strings.Contains(out, "dpkg") {
		t.Errorf("a failed upgrade: %q %v", out, err)
	}
}

// A daemon that restarts while an upgrade runs waits for that one and reports it, and never starts
// another. One that finds no upgrade says so.
func TestUpgradeIsPickedUpAfterARestart(t *testing.T) {
	sd := upgradeFakes(t, nil)
	state := t.TempDir()
	ctx := context.Background()
	if _, err := Upgrade(ctx, state, "security", true); err == nil || !strings.Contains(err.Error(), "didn't start") {
		t.Errorf("nothing to pick up: %v", err)
	}
	if err := config.StartUnit(ctx, UpgradeUnit, filepath.Join(state, UpgradeLog), nil, "/bin/sh", "-c", "sleep 0.3; echo All upgrades installed"); err != nil {
		t.Fatal(err)
	}
	if out, err := Upgrade(ctx, state, "all", true); err != nil || !strings.Contains(out, "All upgrades installed") {
		t.Errorf("picked up: %q %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(sd, "log")); strings.Count(string(b), "systemd-run") != 1 {
		t.Errorf("started another upgrade:\n%s", b)
	}

	// The wait ends with its context; the upgrade runs on.
	if err := config.StartUnit(ctx, UpgradeUnit, filepath.Join(state, UpgradeLog), nil, "sleep", "1"); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := Upgrade(cctx, state, "security", false); err == nil || !strings.Contains(err.Error(), "still runs") {
		t.Errorf("the wait outlived its context: %v", err)
	}
	if u, _ := config.ShowUnit(ctx, UpgradeUnit); !u.Running() {
		t.Error("the end of the wait stopped the upgrade")
	}
}
