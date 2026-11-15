package notifications

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// The alerts about a machine's own system: its security checks and Tender's updates.
const (
	TypeFirewallOff            = "security.firewall_off"
	TypePortExposed            = "security.port_exposed"
	TypeSSHWeak                = "security.ssh_weak"
	TypeSecurityUpdatesPending = "security.updates_pending"
	TypeAutoUpdatesOff         = "security.auto_updates_off"
	TypeRebootRequired         = "security.reboot_required"
	TypeUpdateFailed           = "update.failed"
)

const (
	securityUpdatesAfter = 3 * 24 * time.Hour // security updates pending this long mean unattended-upgrades isn't working
	rebootAfter          = 7 * 24 * time.Hour
)

func init() {
	for typ, text := range map[string]struct{ title, tool string }{
		TypeFirewallOff:            {"firewall off", "get_security"},
		TypePortExposed:            {"exposed", "get_security"}, // follows the port: "8080/tcp exposed"
		TypeSSHWeak:                {"SSH allows passwords", "get_security"},
		TypeSecurityUpdatesPending: {"security updates pending", "get_security"},
		TypeAutoUpdatesOff:         {"automatic updates off", "get_security"},
		TypeRebootRequired:         {"reboot required", "get_security"},
		TypeUpdateFailed:           {"Tender update failed", "get_updates"},
	} {
		typeText[typ] = text
	}
}

// systemTypes are the alerts of a machine's security checks and updates, which stay, like a service's, while
// the machine is silent.
var systemTypes = []string{TypeFirewallOff, TypePortExposed, TypeSSHWeak, TypeSecurityUpdatesPending, TypeAutoUpdatesOff, TypeRebootRequired, TypeUpdateFailed}

// Security is what the rules need to know about a machine's security checks. The hub's wiring
// fills it from the security and machines stores.
type Security struct {
	AlertsEnabled        bool
	AllowedPorts         []string // ports such as 41641/udp that may listen on a public address
	Latest, Previous     *SecurityReport
	SecurityUpdatesSince *time.Time // since when the reports have found security updates pending
}

// SecurityReport is one report of a machine's checks. A check that didn't run, or reports
// unsupported, has its Checked false and never alerts.
type SecurityReport struct {
	FirewallChecked  bool
	FirewallOn       bool   // active, denying incoming traffic by default
	Firewall         string // what it found, in words
	ListenersChecked bool
	Listeners        []Listener
	SSHChecked       bool
	SSHPasswords     bool // password login, or root login with a password
	SSH              string
	UpdatesChecked   bool
	SecurityUpdates  int
	AutoUpdates      bool // unattended-upgrades is on
	RebootChecked    bool
	RebootSince      *time.Time // when a reboot became required, or nil
}

// Listener is a socket on a public address.
type Listener struct {
	Port    string // such as 8080/tcp
	Address string
	Process string
	Docker  bool   // Docker publishes it, which goes around ufw
	Service string // its container
}

// UpdateFailure is a machine's last update of Tender, which failed and rolled back.
type UpdateFailure struct {
	Version string
	Reason  string
}

// evaluateSystem returns the security and update findings of m. A security finding holds when the
// latest two reports both find it, so a quick change doesn't alert.
func evaluateSystem(m Machine, now time.Time) []finding {
	var out []finding
	if f := m.UpdateFailed; f != nil {
		content := fmt.Sprintf("Tender could not update %s to %s. %s", m.Name, f.Version, f.Reason)
		out = append(out, finding{TypeUpdateFailed, SeverityWarning, content, ""})
	}
	s := m.Security
	if s == nil || !s.AlertsEnabled || s.Latest == nil || s.Previous == nil {
		return out
	}
	both := func(f func(r *SecurityReport) bool) bool { return f(s.Latest) && f(s.Previous) }
	if both(func(r *SecurityReport) bool { return r.FirewallChecked && !r.FirewallOn }) {
		out = append(out, finding{TypeFirewallOff, SeverityCritical,
			fmt.Sprintf("%s has no active firewall that denies incoming traffic by default: %s.", m.Name, s.Latest.Firewall), ""})
	}
	if s.Latest.ListenersChecked && s.Previous.ListenersChecked {
		for _, l := range s.Latest.Listeners {
			if slices.Contains(s.AllowedPorts, l.Port) || !slices.ContainsFunc(s.Previous.Listeners, func(p Listener) bool { return p.Port == l.Port }) {
				continue
			}
			out = append(out, finding{TypePortExposed, SeverityWarning, portText(m.Name, l), l.Port})
		}
	}
	if both(func(r *SecurityReport) bool { return r.SSHChecked && r.SSHPasswords }) {
		out = append(out, finding{TypeSSHWeak, SeverityWarning, fmt.Sprintf("SSH on %s %s.", m.Name, s.Latest.SSH), ""})
	}
	if s.Latest.UpdatesChecked && s.Latest.SecurityUpdates > 0 && s.SecurityUpdatesSince != nil && now.Sub(*s.SecurityUpdatesSince) > securityUpdatesAfter {
		out = append(out, finding{TypeSecurityUpdatesPending, SeverityWarning,
			fmt.Sprintf("%s has had security updates pending since %s (%s), so unattended-upgrades isn't installing them. It has %s pending now. "+
				"Upgrade them with: tender machines upgrade %s", m.Name, formatTime(*s.SecurityUpdatesSince), formatDuration(now.Sub(*s.SecurityUpdatesSince)),
				plural(int64(s.Latest.SecurityUpdates), "security update"), m.Name), ""})
	}
	if both(func(r *SecurityReport) bool { return r.UpdatesChecked && !r.AutoUpdates }) {
		out = append(out, finding{TypeAutoUpdatesOff, SeverityWarning,
			fmt.Sprintf("unattended-upgrades is off on %s, so security updates wait for someone to install them. "+
				"Turn it on with: sudo apt install unattended-upgrades && sudo dpkg-reconfigure -plow unattended-upgrades", m.Name), ""})
	}
	if r := s.Latest; r.RebootChecked && r.RebootSince != nil && now.Sub(*r.RebootSince) > rebootAfter {
		out = append(out, finding{TypeRebootRequired, SeverityWarning,
			fmt.Sprintf("%s has needed a reboot since %s (%s) to finish installing updates. Tender never reboots a machine: reboot it when it suits you.",
				m.Name, formatTime(*r.RebootSince), formatDuration(now.Sub(*r.RebootSince))), ""})
	}
	return out
}

func portText(machine string, l Listener) string {
	who := ""
	if l.Process != "" {
		who = " (" + l.Process + ")"
	}
	text := fmt.Sprintf("%s listens on %s at %s%s, which isn't loopback or the tailnet.", machine, l.Port, l.Address, who)
	if l.Docker {
		service := "a container"
		if l.Service != "" {
			service = "the container " + l.Service
		}
		text = fmt.Sprintf("Docker publishes %s on %s at %s for %s. Docker's ports go around ufw, so anyone who can reach the machine can reach it.",
			l.Port, machine, l.Address, service)
	}
	return text + " If it should be reachable, add it to the machine's allowlist: tender machines set " + machine + " --allow-port " + l.Port
}

// isSystem reports whether typ is a security or update alert.
func isSystem(typ string) bool { return slices.Contains(systemTypes, typ) }

// SSHText words what makes an SSH server weak, for the hub's wiring: "allows password login".
func SSHText(passwordAuthentication bool, permitRootLogin string) string {
	var parts []string
	if passwordAuthentication {
		parts = append(parts, "allows password login (PasswordAuthentication yes)")
	}
	if permitRootLogin == "yes" {
		parts = append(parts, "lets root log in with a password (PermitRootLogin yes)")
	}
	return strings.Join(parts, " and ")
}
