package main

import (
	"fmt"
	"slices"
	"time"

	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/security"
)

// The events of a machine's own system: its security checks and Tender's updates.
const (
	eventFirewallOff            = "security.firewall_off"
	eventPortExposed            = "security.port_exposed"
	eventSSHWeak                = "security.ssh_weak"
	eventSecurityUpdatesPending = "security.updates_pending"
	eventAutoUpdatesOff         = "security.auto_updates_off"
	eventRebootRequired         = "security.reboot_required"
	eventUpdateFailed           = "update.failed"
)

const (
	securityUpdatesAfter = 3 * 24 * time.Hour // security updates pending this long mean unattended-upgrades isn't working
	rebootAfter          = 7 * 24 * time.Hour
)

var (
	warning  = []string{notifications.SeverityWarning}
	critical = []string{notifications.SeverityCritical}
	twice    = "Two security reports in a row, 15 minutes apart, find "
)

// securityEvents are the security domain's events. A port_exposed notification's service is the
// port, such as 8080/tcp, so each port is its own problem.
var securityEvents = []notifications.EventType{
	{Name: eventFirewallOff, Severities: critical, Title: "firewall off", Tool: "get_security",
		Fires:    twice + "no active firewall that denies incoming traffic by default: ufw is inactive or allows by default, or without ufw no nftables or iptables input policy drops.",
		Resolves: "A report finds a firewall that denies by default, or the machine's security notifications are turned off."},
	{Name: eventPortExposed, Severities: warning, Title: "exposed", Tool: "get_security",
		Fires:    twice + "a TCP or UDP port listening on an address other than loopback, link-local or the tailnet, and not on the machine's allowlist. One problem per port.",
		Resolves: "A report finds the port closed or on loopback or the tailnet, it is added to the allowlist, or the machine's security notifications are turned off."},
	{Name: eventSSHWeak, Severities: warning, Title: "SSH allows passwords", Tool: "get_security",
		Fires:    twice + "sshd -T saying PasswordAuthentication yes, and with PermitRootLogin yes root too. Root login with a key only is not weak.",
		Resolves: "A report finds password login off, or the machine's security notifications are turned off."},
	{Name: eventSecurityUpdatesPending, Severities: warning, Title: "security updates pending", Tool: "get_security",
		Fires:    "Security updates have been pending for more than 3 days, so unattended-upgrades isn't installing them.",
		Resolves: "A report finds no security updates pending, or the machine's security notifications are turned off."},
	{Name: eventAutoUpdatesOff, Severities: warning, Title: "automatic updates off", Tool: "get_security",
		Fires:    twice + "unattended-upgrades not installed, or APT::Periodic::Unattended-Upgrade off.",
		Resolves: "A report finds unattended-upgrades on, or the machine's security notifications are turned off."},
	{Name: eventRebootRequired, Severities: warning, Title: "reboot required", Tool: "get_security",
		Fires:    "/var/run/reboot-required is more than 7 days old. Tender never reboots a machine.",
		Resolves: "A report finds no reboot required, or the machine's security notifications are turned off."},
}

// updateEvents are the updates domain's events.
var updateEvents = []notifications.EventType{
	{Name: eventUpdateFailed, Severities: warning, Title: "Tender update failed", Tool: "get_updates",
		Fires:    "An update of Tender on the machine failed and rolled back. It says the version and the reason.",
		Resolves: "The machine reports that version or a newer one, after a later rollout."},
}

// machineSecurity is what the security rules know about a machine's checks.
type machineSecurity struct {
	Enabled              bool     // the machine's security notifications are on
	AllowedPorts         []string // ports such as 41641/udp, and programs such as cloudflared, that may listen on a public address
	Latest, Previous     *securityFacts
	SecurityUpdatesSince *time.Time // since when the reports have found security updates pending
}

// securityFacts is one report of a machine's checks, in the rules' terms. A check that didn't run,
// or reports unsupported, has its Checked false and never fires.
type securityFacts struct {
	FirewallChecked  bool
	FirewallOn       bool   // active, denying incoming traffic by default
	Firewall         string // what it found, in words
	ListenersChecked bool
	Listeners        []listener
	SSHChecked       bool
	SSHPasswords     bool // password login, or root login with a password
	SSH              string
	UpdatesChecked   bool
	SecurityUpdates  int
	AutoUpdates      bool // unattended-upgrades is on
	RebootChecked    bool
	RebootSince      *time.Time // when a reboot became required, or nil
}

// listener is a socket on a public address.
type listener struct {
	Port    string // such as 8080/tcp
	Address string
	Process string
	Docker  bool   // Docker publishes it, which goes around ufw
	Service string // its container
}

// updateFailure is a machine's last update of Tender, which failed and rolled back.
type updateFailure struct {
	Version string
	Reason  string
}

// updateFindings is the update.failed finding of machine, when its last update failed.
func updateFindings(machine string, f *updateFailure) []notifications.Finding {
	if f == nil {
		return nil
	}
	return []notifications.Finding{{Event: eventUpdateFailed, Severity: notifications.SeverityWarning,
		Message: fmt.Sprintf("Tender could not update %s to %s. %s", machine, f.Version, f.Reason)}}
}

// securityFindings returns the security findings of machine. Most hold only when the latest two
// reports both find them, so a quick change doesn't notify.
func securityFindings(machine string, s *machineSecurity, now time.Time) []notifications.Finding {
	if s == nil || !s.Enabled || s.Latest == nil || s.Previous == nil {
		return nil
	}
	var out []notifications.Finding
	add := func(event, severity, message, service string) {
		out = append(out, notifications.Finding{Event: event, Severity: severity, Message: message, Service: service})
	}
	both := func(f func(r *securityFacts) bool) bool { return f(s.Latest) && f(s.Previous) }
	if both(func(r *securityFacts) bool { return r.FirewallChecked && !r.FirewallOn }) {
		add(eventFirewallOff, notifications.SeverityCritical,
			fmt.Sprintf("%s has no active firewall that denies incoming traffic by default: %s.", machine, s.Latest.Firewall), "")
	}
	if s.Latest.ListenersChecked && s.Previous.ListenersChecked {
		for _, l := range s.Latest.Listeners {
			if security.AllowedBy(s.AllowedPorts, l.Port, l.Process) || !slices.ContainsFunc(s.Previous.Listeners, func(p listener) bool { return p.Port == l.Port }) {
				continue
			}
			add(eventPortExposed, notifications.SeverityWarning, portText(machine, l), l.Port)
		}
	}
	if both(func(r *securityFacts) bool { return r.SSHChecked && r.SSHPasswords }) {
		add(eventSSHWeak, notifications.SeverityWarning, fmt.Sprintf("SSH on %s %s.", machine, s.Latest.SSH), "")
	}
	if s.Latest.UpdatesChecked && s.Latest.SecurityUpdates > 0 && s.SecurityUpdatesSince != nil && now.Sub(*s.SecurityUpdatesSince) > securityUpdatesAfter {
		pending := "1 security update"
		if s.Latest.SecurityUpdates != 1 {
			pending = fmt.Sprintf("%d security updates", s.Latest.SecurityUpdates)
		}
		add(eventSecurityUpdatesPending, notifications.SeverityWarning,
			fmt.Sprintf("%s has had security updates pending since %s (%s), so unattended-upgrades isn't installing them. It has %s pending now. "+
				"Upgrade them with: tender machines upgrade %s", machine, notifications.FormatTime(*s.SecurityUpdatesSince),
				notifications.FormatDuration(now.Sub(*s.SecurityUpdatesSince)), pending, machine), "")
	}
	if both(func(r *securityFacts) bool { return r.UpdatesChecked && !r.AutoUpdates }) {
		add(eventAutoUpdatesOff, notifications.SeverityWarning,
			fmt.Sprintf("unattended-upgrades is off on %s, so security updates wait for someone to install them. "+
				"Turn it on with: sudo apt install unattended-upgrades && sudo dpkg-reconfigure -plow unattended-upgrades", machine), "")
	}
	if r := s.Latest; r.RebootChecked && r.RebootSince != nil && now.Sub(*r.RebootSince) > rebootAfter {
		add(eventRebootRequired, notifications.SeverityWarning,
			fmt.Sprintf("%s has needed a reboot since %s (%s) to finish installing updates. Tender never reboots a machine: reboot it when it suits you.",
				machine, notifications.FormatTime(*r.RebootSince), notifications.FormatDuration(now.Sub(*r.RebootSince))), "")
	}
	return out
}

func portText(machine string, l listener) string {
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

// sshWeakText words what makes an SSH server weak: "allows password login", and root too when
// PermitRootLogin is yes. It is "" when nobody can log in with a password.
func sshWeakText(passwordAuthentication bool, permitRootLogin string) string {
	if !passwordAuthentication {
		return ""
	}
	text := "allows password login (PasswordAuthentication yes)"
	if permitRootLogin == "yes" {
		text += " and lets root log in with a password (PermitRootLogin yes)"
	}
	return text
}
