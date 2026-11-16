package security

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// commandTimeout bounds each program the collector runs.
const commandTimeout = time.Minute

// Published is a port Docker publishes for a container.
type Published struct {
	Protocol string // tcp or udp
	Address  string // the host address, such as 0.0.0.0
	Port     int
	Service  string // the container's name
}

// Collector reads this machine's security state. It only reads, and changes nothing.
type Collector struct {
	// Root is where the machine's files are, "/" outside tests.
	Root string
	// Published lists the ports Docker publishes. Nil, or an error, means none.
	Published func(ctx context.Context) ([]Published, error)
	// Now is the time; tests set it.
	Now func() time.Time
}

func (c *Collector) path(p string) string { return filepath.Join(c.Root, p) }

// Collect runs every check. On a machine that isn't Debian or Ubuntu each check says unsupported.
func (c *Collector) Collect(ctx context.Context) Report {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	r := Report{CollectedAt: now().UTC().Truncate(time.Second)}
	if !c.debian() {
		r.Firewall.Check, r.Listeners.Check, r.SSH.Check, r.Updates.Check, r.Reboot.Check =
			CheckUnsupported, CheckUnsupported, CheckUnsupported, CheckUnsupported, CheckUnsupported
		r.Listeners.Ports = []Listener{}
		return r
	}
	r.Firewall = firewall(ctx)
	r.Listeners = c.listeners(ctx)
	r.SSH = c.sshd(ctx)
	r.Updates = updates(ctx)
	r.Reboot = c.reboot()
	return r
}

// debian reports whether /etc/os-release names Debian or Ubuntu, or a system based on them.
func (c *Collector) debian() bool {
	b, err := os.ReadFile(c.path("/etc/os-release"))
	if err != nil {
		if b, err = os.ReadFile(c.path("/usr/lib/os-release")); err != nil {
			return false
		}
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || (k != "ID" && k != "ID_LIKE") {
			continue
		}
		for _, id := range strings.Fields(strings.Trim(v, `"'`)) {
			if id == "debian" || id == "ubuntu" {
				return true
			}
		}
	}
	return false
}

// errMissing means the program isn't installed.
var errMissing = errors.New("not installed")

// run runs a program with fixed arguments and returns its output. A missing program is errMissing.
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return nil, errMissing
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg != "" {
			return out, fmt.Errorf("%s failed: %s", name, msg)
		}
		return out, fmt.Errorf("%s failed: %w", name, err)
	}
	return out, nil
}

func ptr[T any](v T) *T { return &v }

func failed(err error) *string { return ptr(err.Error()) }

// firewall checks ufw, or without it nftables's input chains, or iptables's INPUT policy.
func firewall(ctx context.Context) Firewall {
	out, err := run(ctx, "ufw", "status", "verbose")
	switch {
	case err == nil:
		return parseUFW(out)
	case !errors.Is(err, errMissing):
		return Firewall{Check: CheckError, Error: failed(err), Tool: ptr("ufw")}
	}
	out, err = run(ctx, "nft", "-j", "list", "chains")
	switch {
	case err == nil:
		return parseNft(out)
	case !errors.Is(err, errMissing):
		return Firewall{Check: CheckError, Error: failed(err), Tool: ptr("nftables")}
	}
	out, err = run(ctx, "iptables", "-S", "INPUT")
	switch {
	case err == nil:
		return parseIptables(out)
	case !errors.Is(err, errMissing):
		return Firewall{Check: CheckError, Error: failed(err), Tool: ptr("iptables")}
	}
	return Firewall{Check: CheckOK, Tool: ptr("none")}
}

var ufwDefault = regexp.MustCompile(`(?m)^Default:\s*(\w+)\s*\(incoming\)`)

// parseUFW reads "ufw status verbose": "Status: active" and "Default: deny (incoming), ...".
func parseUFW(out []byte) Firewall {
	f := Firewall{Check: CheckOK, Tool: ptr("ufw")}
	f.Active = bytes.Contains(out, []byte("Status: active"))
	if m := ufwDefault.FindSubmatch(out); f.Active && m != nil {
		f.DefaultIncoming = ptr(policy(string(m[1])))
	}
	return f
}

// parseNft reads "nft -j list chains". The firewall is active when a chain hooks input; incoming
// traffic is denied by default when one of those chains drops what it doesn't accept.
func parseNft(out []byte) Firewall {
	f := Firewall{Check: CheckOK, Tool: ptr("nftables")}
	var doc struct {
		Nftables []struct {
			Chain *struct {
				Hook   string `json:"hook"`
				Policy string `json:"policy"`
			} `json:"chain"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		f.Check, f.Error = CheckError, ptr("could not read nft's output: "+err.Error())
		return f
	}
	in := "allow"
	for _, e := range doc.Nftables {
		if e.Chain == nil || e.Chain.Hook != "input" {
			continue
		}
		f.Active = true
		if p := policy(e.Chain.Policy); p != "allow" {
			in = p
		}
	}
	if f.Active {
		f.DefaultIncoming = &in
	}
	return f
}

// parseIptables reads "iptables -S INPUT", whose first line is "-P INPUT DROP" or "-P INPUT ACCEPT".
func parseIptables(out []byte) Firewall {
	f := Firewall{Check: CheckOK, Tool: ptr("iptables")}
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "-P INPUT "); ok {
			f.DefaultIncoming = ptr(policy(p))
			f.Active = *f.DefaultIncoming != "allow" || strings.Count(string(out), "\n-A INPUT") > 0
		}
	}
	return f
}

// policy names a firewall's default: deny, reject or allow.
func policy(p string) string {
	switch strings.ToLower(p) {
	case "deny", "drop":
		return "deny"
	case "reject":
		return "reject"
	}
	return "allow"
}

// The networks a listener may use without counting as public: loopback, link-local and the tailnet.
var private = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

func public(addr netip.Addr) bool {
	addr = addr.Unmap()
	return !slices.ContainsFunc(private, func(p netip.Prefix) bool { return p.Contains(addr) })
}

// maxListeners bounds a report.
const maxListeners = 500

// listeners lists the sockets listening on a public address, from ss, and the ports Docker
// publishes on every address, which go around ufw.
func (c *Collector) listeners(ctx context.Context) Listeners {
	out, err := run(ctx, "ss", "-H", "-l", "-n", "-t", "-u", "-p")
	if err != nil {
		return Listeners{Check: CheckError, Error: failed(err), Ports: []Listener{}}
	}
	l := Listeners{Check: CheckOK, Ports: parseSS(out, c.ephemeral())}
	if c.Published != nil {
		if ports, err := c.Published(ctx); err == nil {
			l.Ports = mergePublished(l.Ports, ports)
		}
	}
	if len(l.Ports) > maxListeners {
		l.Ports = l.Ports[:maxListeners]
	}
	return l
}

// dhcpClient are the UDP ports a DHCP client binds to get the machine's address: 68 for DHCPv4 and
// 546 for DHCPv6. systemd-networkd and dhclient bind them on the public address, but they only take
// the DHCP server's answers, so they serve nothing.
var dhcpClient = map[int]bool{68: true, 546: true}

// portRange is the first and last port of a range.
type portRange struct{ first, last int }

// defaultEphemeral is Linux's default ip_local_port_range.
var defaultEphemeral = portRange{32768, 60999}

// ephemeral is the kernel's range of ports for outgoing connections, from
// /proc/sys/net/ipv4/ip_local_port_range, or Linux's default when it can't be read.
func (c *Collector) ephemeral() portRange {
	b, err := os.ReadFile(c.path("/proc/sys/net/ipv4/ip_local_port_range"))
	if err != nil {
		return defaultEphemeral
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return defaultEphemeral
	}
	first, err1 := strconv.Atoi(f[0])
	last, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || first < 1 || last > 65535 || first > last {
		return defaultEphemeral
	}
	return portRange{first, last}
}

var ssProcess = regexp.MustCompile(`users:\(\("([^"]+)"`)

// parseSS reads "ss -H -l -n -t -u -p": Netid State Recv-Q Send-Q Local:Port Peer:Port [Process].
// A socket on a loopback, link-local or tailnet address, or bound to the loopback device, is left
// out, and so is a DHCP client's (see dhcpClient). So is a UDP socket on a port in ephemeral: ss
// lists a client's unconnected UDP socket, such as cloudflared's QUIC connection to Cloudflare, as
// listening on 0.0.0.0 and the port the kernel picked, which changes on each reconnect. A TCP
// socket is always listed, because one that listens is a server. Each port and protocol shows once.
func parseSS(out []byte, ephemeral portRange) []Listener {
	list := []Listener{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || (f[0] != "tcp" && f[0] != "udp") {
			continue
		}
		host, port, ok := splitAddress(f[4])
		if !ok || (f[0] == "udp" && (dhcpClient[port] || port >= ephemeral.first && port <= ephemeral.last)) {
			continue
		}
		if dev := strings.Index(host, "%"); dev >= 0 {
			if host[dev+1:] == "lo" {
				continue
			}
			host = host[:dev]
		}
		if host == "*" {
			host = "0.0.0.0"
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || !public(addr) {
			continue
		}
		l := Listener{Protocol: f[0], Address: addr.String(), Port: port}
		if m := ssProcess.FindStringSubmatch(sc.Text()); m != nil {
			l.Process = ptr(m[1])
		}
		if seen[l.Key()] {
			continue
		}
		seen[l.Key()] = true
		list = append(list, l)
	}
	return list
}

// splitAddress splits ss's "0.0.0.0:22", "[::]:22" or "*:22".
func splitAddress(s string) (string, int, bool) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		i := strings.LastIndexByte(s, ':')
		if i < 0 {
			return "", 0, false
		}
		host, port = strings.Trim(s[:i], "[]"), s[i+1:]
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, false
	}
	return host, n, true
}

// mergePublished marks the listeners Docker publishes, and adds the published ports no socket
// shows, as when Docker forwards them without its proxy. Only ports on every address, or on a
// public one, count.
func mergePublished(list []Listener, ports []Published) []Listener {
	for _, p := range ports {
		host := p.Address
		if host == "" {
			host = "0.0.0.0"
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || !public(addr) || p.Port < 1 || p.Port > 65535 || (p.Protocol != "tcp" && p.Protocol != "udp") {
			continue
		}
		key := strconv.Itoa(p.Port) + "/" + p.Protocol
		service := p.Service
		i := slices.IndexFunc(list, func(l Listener) bool { return l.Key() == key })
		if i < 0 {
			list = append(list, Listener{Protocol: p.Protocol, Address: addr.String(), Port: p.Port, Process: ptr("docker"), Docker: true, Service: &service})
			continue
		}
		list[i].Docker, list[i].Service = true, &service
	}
	return list
}

// sshd reads the SSH server's settings with "sshd -T". A machine without sshd has no SSH server.
func (c *Collector) sshd(ctx context.Context) SSH {
	bin := "sshd"
	if _, err := exec.LookPath(bin); err != nil {
		bin = c.path("/usr/sbin/sshd") // often outside a service's PATH
		if _, err := os.Stat(bin); err != nil {
			return SSH{Check: CheckOK}
		}
	}
	out, err := run(ctx, bin, "-T")
	if err != nil {
		return SSH{Check: CheckError, Error: failed(err), Installed: true}
	}
	s := SSH{Check: CheckOK, Installed: true}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch strings.ToLower(k) {
		case "passwordauthentication":
			s.PasswordAuthentication = ptr(strings.EqualFold(v, "yes"))
		case "permitrootlogin":
			s.PermitRootLogin = ptr(strings.ToLower(v))
		}
	}
	return s
}

// The apt-get options of a simulation that changes nothing: no lock, no package cache on disk. It
// simulates what tender machines upgrade --all-packages runs, apt-get upgrade --with-new-pkgs, so it
// counts what that would install. It reads the package lists as apt's daily update left them.
var simulate = []string{"-s", "-o", "Debug::NoLocking=1", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=", "upgrade", "--with-new-pkgs"}

// updates counts the pending updates and the security ones, from a simulated upgrade, and
// says whether unattended-upgrades is installed and turned on.
func updates(ctx context.Context) Updates {
	out, err := run(ctx, "apt-get", simulate...)
	if err != nil {
		return Updates{Check: CheckError, Error: failed(err)}
	}
	u := Updates{Check: CheckOK}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		u.Pending++
		if strings.Contains(strings.ToLower(line), "security") {
			u.Security++
		}
	}
	u.UnattendedUpgrades = unattended(ctx)
	return u
}

// unattended reports whether unattended-upgrade is installed and APT::Periodic::Unattended-Upgrade is on.
func unattended(ctx context.Context) bool {
	if _, err := exec.LookPath("unattended-upgrade"); err != nil {
		return false
	}
	out, err := run(ctx, "apt-config", "shell", "UU", "APT::Periodic::Unattended-Upgrade")
	if err != nil {
		return false
	}
	v := strings.TrimSpace(string(out))
	v = strings.Trim(strings.TrimPrefix(v, "UU="), `'"`)
	return v != "" && v != "0"
}

// reboot reads /var/run/reboot-required, which apt leaves when an update needs a reboot. Its age
// says since when.
func (c *Collector) reboot() Reboot {
	fi, err := os.Stat(c.path("/var/run/reboot-required"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Reboot{Check: CheckOK}
	case err != nil:
		return Reboot{Check: CheckError, Error: failed(err)}
	}
	return Reboot{Check: CheckOK, Required: true, Since: ptr(fi.ModTime().UTC().Truncate(time.Second))}
}
