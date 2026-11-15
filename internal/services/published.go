package services

import (
	"context"
	"strings"
)

// Port is a port Docker publishes for a running container on the host.
type Port struct {
	Protocol string // tcp or udp
	Address  string // the host address, such as 0.0.0.0 or ::
	Port     int
	Service  string // the container's name
}

// Published lists the ports Docker publishes for its running containers, from the container list.
// Docker forwards them in the kernel, so they reach the container whatever ufw says.
func (d *Docker) Published(ctx context.Context) ([]Port, error) {
	var list []struct {
		Names []string
		State string
		Ports []struct {
			IP         string
			PublicPort int
			Type       string
		}
	}
	if err := d.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return nil, err
	}
	var out []Port
	for _, c := range list {
		if c.State != StateRunning {
			continue
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		for _, p := range c.Ports {
			if p.PublicPort > 0 {
				out = append(out, Port{Protocol: p.Type, Address: p.IP, Port: p.PublicPort, Service: name})
			}
		}
	}
	return out, nil
}
