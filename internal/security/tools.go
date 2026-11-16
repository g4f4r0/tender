package security

import (
	"context"

	"github.com/g4f4r0/tender/internal/mcp"
)

type machineInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
}

// RegisterTools adds the security tools to m. They use the same store methods as the REST routes.
func (s *Store) RegisterTools(m *mcp.Server, lookup mcp.MachineLookup) {
	mcp.AddReadOnly(m, "get_security", "Get a machine's security checks",
		"Get the latest security checks of a machine, which its daemon collects when it starts and every 15 minutes, read-only, on "+
			"Debian and Ubuntu. Each check has check (ok, error or unsupported on other systems) and error. firewall: tool (ufw, "+
			"nftables, iptables or none), active and default_incoming (deny, reject or allow). listeners.ports: every TCP or UDP socket "+
			"listening on an address other than loopback, link-local or the tailnet, with protocol, address, port, process, docker "+
			"(Docker publishes it, which goes around ufw), service (its container) and allowed (on the machine's "+
			"security_allowed_ports). ssh: installed, password_authentication and permit_root_login. updates: pending, security and "+
			"unattended_upgrades. reboot: required and since. Also id, machine, security_updates_since, collected_at, created_at and updated_at. "+
			"The alerts security.firewall_off, security.port_exposed, security.ssh_weak, security.updates_pending, security.auto_updates_off and security.reboot_required come "+
			"from these. Change the allowlist with update_machine, and upgrade packages with upgrade_machine.",
		func(ctx context.Context, in machineInput) (State, error) {
			id, err := mcp.Machine(ctx, lookup, in.Machine)
			if err != nil {
				return State{}, err
			}
			return s.Get(ctx, id)
		})
}
