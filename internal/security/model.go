package security

import (
	"strconv"
	"time"
)

// What each check says about itself.
const (
	CheckOK          = "ok"          // it ran, and its fields say what it found
	CheckError       = "error"       // it couldn't run; error says why
	CheckUnsupported = "unsupported" // the machine isn't Debian or Ubuntu, so it doesn't run and never alerts
)

// Firewall is what the firewall check found: ufw, or without it nftables or iptables.
type Firewall struct {
	Check           string  `json:"check"`
	Error           *string `json:"error"`
	Tool            *string `json:"tool"`             // ufw, nftables, iptables, or none
	Active          bool    `json:"active"`           // the firewall filters incoming traffic
	DefaultIncoming *string `json:"default_incoming"` // deny, reject or allow: what happens to incoming traffic no rule allows
}

// Listener is one socket listening on an address other than loopback or the tailnet.
type Listener struct {
	Protocol string  `json:"protocol"` // tcp or udp
	Address  string  `json:"address"`  // such as 0.0.0.0, ::, or one of the machine's addresses
	Port     int     `json:"port"`
	Process  *string `json:"process"` // the program behind it, when the daemon could name it
	Docker   bool    `json:"docker"`  // Docker publishes it, which goes around ufw
	Service  *string `json:"service"` // the container that publishes it
	Allowed  bool    `json:"allowed"` // on the machine's security_allowed_ports; set by the hub
}

// Key is the listener's port and protocol, such as 41641/udp, as security_allowed_ports lists it.
func (l Listener) Key() string { return strconv.Itoa(l.Port) + "/" + l.Protocol }

// Listeners is what the listeners check found.
type Listeners struct {
	Check string     `json:"check"`
	Error *string    `json:"error"`
	Ports []Listener `json:"ports"`
}

// SSH is what the SSH check found, from sshd -T.
type SSH struct {
	Check                  string  `json:"check"`
	Error                  *string `json:"error"`
	Installed              bool    `json:"installed"`                // the machine has an SSH server
	PasswordAuthentication *bool   `json:"password_authentication"` // people can log in with a password
	PermitRootLogin        *string `json:"permit_root_login"`       // yes, no, prohibit-password, forced-commands-only
}

// Updates is what the updates check found, from a simulated apt-get dist-upgrade.
type Updates struct {
	Check              string  `json:"check"`
	Error              *string `json:"error"`
	Pending            int     `json:"pending"`             // packages with an update
	Security           int     `json:"security"`            // of those, from a security repository
	UnattendedUpgrades bool    `json:"unattended_upgrades"` // unattended-upgrades is installed and turned on
}

// Reboot is what the reboot check found, from /var/run/reboot-required.
type Reboot struct {
	Check    string     `json:"check"`
	Error    *string    `json:"error"`
	Required bool       `json:"required"`
	Since    *time.Time `json:"since"` // when the reboot became required
}

// Report is what a daemon collects, the body of PUT /v1/machines/{machine}/security.
type Report struct {
	Firewall    Firewall  `json:"firewall"`
	Listeners   Listeners `json:"listeners"`
	SSH         SSH       `json:"ssh"`
	Updates     Updates   `json:"updates"`
	Reboot      Reboot    `json:"reboot"`
	CollectedAt time.Time `json:"collected_at"`
}

// State is a machine's latest report, as GET /v1/machines/{machine}/security returns it.
type State struct {
	ID      int64  `json:"id"`
	Machine string `json:"machine"`
	Report
	// SecurityUpdatesSince is since when the reports have found security updates pending, or null.
	SecurityUpdatesSince *time.Time `json:"security_updates_since"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"` // when the latest report arrived
	// Previous is the report before the latest, for the rule that an alert opens only when two
	// reports in a row find the problem. It isn't shown.
	Previous *Report `json:"-"`
}
