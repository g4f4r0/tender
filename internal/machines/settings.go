package machines

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/g4f4r0/tender/internal/server"
)

// portPattern is a port and its protocol, such as 41641/udp.
var portPattern = regexp.MustCompile(`^([0-9]{1,5})/(tcp|udp)$`)

// maxAllowedPorts bounds a machine's allowlist.
const maxAllowedPorts = 100

// validPorts checks an allowlist and returns it sorted, without repeats.
func validPorts(ports []string) ([]string, error) {
	if len(ports) > maxAllowedPorts {
		return nil, server.Invalid("security_allowed_ports takes at most %d ports.", maxAllowedPorts)
	}
	out := []string{}
	for _, p := range ports {
		m := portPattern.FindStringSubmatch(p)
		n := 0
		if m != nil {
			n, _ = strconv.Atoi(m[1])
		}
		if n < 1 || n > 65535 {
			return nil, server.Invalid("%q is not a port. Write each port with its protocol, such as 41641/udp or 443/tcp.", p)
		}
		out = append(out, p)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Update applies a merge patch to the machine called name and returns it.
func (s *Store) Update(ctx context.Context, name string, p Patch) (Overview, error) {
	id, err := s.Lookup(ctx, name)
	if err != nil {
		return Overview{}, err
	}
	if p.Updates != nil && !slices.Contains([]string{UpdatesInherit, UpdatesAuto, UpdatesManual}, *p.Updates) {
		return Overview{}, server.Invalid("updates must be inherit, auto or manual.")
	}
	var ports *string
	if p.SecurityAllowedPorts != nil {
		list, err := validPorts(*p.SecurityAllowedPorts)
		if err != nil {
			return Overview{}, err
		}
		b, err := json.Marshal(list)
		if err != nil {
			return Overview{}, err
		}
		v := string(b)
		ports = &v
	}
	if _, err := s.db.Write.ExecContext(ctx,
		`UPDATE machines SET updates = coalesce(?, updates), security_allowed_ports = coalesce(?, security_allowed_ports),
		 security_alerts = coalesce(?, security_alerts), updated_at = ? WHERE id = ?`,
		p.Updates, ports, p.SecurityAlerts, time.Now().Unix(), id); err != nil {
		return Overview{}, err
	}
	return s.Get(ctx, name)
}

// setVersion records the Tender version a machine's daemon reported.
func (s *Store) setVersion(ctx context.Context, machineID int64, version string) error {
	_, err := s.db.Write.ExecContext(ctx, `UPDATE machines SET version = ? WHERE id = ? AND version IS NOT ?`, version, machineID, version)
	return err
}

// versionPattern is a version a daemon may report, such as 0.3.1 or 0.3.1-4-gabc123-dirty.
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$|^dev$`)
