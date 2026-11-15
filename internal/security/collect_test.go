package security

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestSSHWeak(t *testing.T) {
	fakes(t, map[string]string{"sshd": `printf 'passwordauthentication yes\npermitrootlogin yes\n'`})
	s := (&Collector{Root: t.TempDir()}).sshd(context.Background())
	if !*s.PasswordAuthentication || *s.PermitRootLogin != "yes" {
		t.Errorf("ssh = %+v", s)
	}
}

// The upgrade runs in the tenderupgrade transient unit, with fixed arguments.
func TestUpgradeRunsInATransientUnit(t *testing.T) {
	dir := t.TempDir()
	stand := filepath.Join(dir, "systemd-run")
	log := filepath.Join(dir, "args")
	if err := os.WriteFile(stand, []byte("#!/bin/sh\necho \"$@\" >"+log+"\necho 'Reading package lists...'\necho 'All upgrades installed'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TENDER_TEST_SYSTEMD_RUN", stand)
	for packages, want := range map[string]string{
		"security": "--wait --collect --quiet --pipe --unit tenderupgrade --setenv DEBIAN_FRONTEND=noninteractive --setenv LC_ALL=C unattended-upgrade -v",
		"all": "--wait --collect --quiet --pipe --unit tenderupgrade --setenv DEBIAN_FRONTEND=noninteractive --setenv LC_ALL=C apt-get -y " +
			"-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade",
	} {
		out, err := Upgrade(context.Background(), packages)
		if err != nil || !strings.Contains(out, "All upgrades installed") {
			t.Fatalf("%s: %q %v", packages, out, err)
		}
		if b, _ := os.ReadFile(log); strings.TrimSpace(string(b)) != want {
			t.Errorf("%s ran %q", packages, b)
		}
	}
	if _, err := Upgrade(context.Background(), "some"); err == nil {
		t.Error("an unknown packages value ran")
	}
	if err := os.WriteFile(stand, []byte("#!/bin/sh\necho 'E: dpkg was interrupted'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := Upgrade(context.Background(), "all"); err == nil || !strings.Contains(err.Error(), "dpkg was interrupted") || !strings.Contains(out, "dpkg") {
		t.Errorf("a failed upgrade: %q %v", out, err)
	}
}
