package deploy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Rendered is the part of "docker compose config --format json" that Tender's rules and override
// read. Compose has resolved it: interpolated, merged, with relative paths made absolute.
type Rendered struct {
	Services map[string]renderedService `json:"services"`
	Volumes  map[string]renderedVolume  `json:"volumes"`
	Networks map[string]struct {
		External json.RawMessage `json:"external"`
	} `json:"networks"`
	Secrets map[string]fileRef `json:"secrets"`
	Configs map[string]fileRef `json:"configs"`
}

type renderedService struct {
	Image         string            `json:"image"`
	Build         json.RawMessage   `json:"build"`
	Ports         []json.RawMessage `json:"ports"`
	NetworkMode   string            `json:"network_mode"`
	Pid           string            `json:"pid"`
	Ipc           string            `json:"ipc"`
	Uts           string            `json:"uts"`
	UsernsMode    string            `json:"userns_mode"`
	Privileged    bool              `json:"privileged"`
	Devices       []json.RawMessage `json:"devices"`
	SecurityOpt   []string          `json:"security_opt"`
	CapAdd        []string          `json:"cap_add"`
	Volumes       []mount           `json:"volumes"`
	ContainerName string            `json:"container_name"`
	MemLimit      json.RawMessage   `json:"mem_limit"`
	Deploy        *struct {
		Resources struct {
			Limits struct {
				Memory json.RawMessage `json:"memory"`
			} `json:"limits"`
		} `json:"resources"`
	} `json:"deploy"`
	Restart string            `json:"restart"`
	EnvFile []json.RawMessage `json:"env_file"`
}

type mount struct {
	Type   string `json:"type"`
	Source string `json:"source"`
	Target string `json:"target"`
}

type renderedVolume struct {
	External   json.RawMessage `json:"external"`
	Driver     string          `json:"driver"`
	DriverOpts json.RawMessage `json:"driver_opts"`
}

type fileRef struct {
	File string `json:"file"`
}

// ParseRendered reads compose's rendered config.
func ParseRendered(b []byte) (Rendered, error) {
	var r Rendered
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("docker compose config printed something that isn't its JSON: %w", err)
	}
	if len(r.Services) == 0 {
		return r, fmt.Errorf("the compose file has no services. Add one under services:, each with an image")
	}
	return r, nil
}

// The capabilities a service may not add: each is enough to take over the machine.
var deniedCaps = []string{"ALL", "SYS_ADMIN", "SYS_MODULE", "SYS_PTRACE", "SYS_RAWIO", "DAC_READ_SEARCH", "NET_ADMIN"}

// set reports whether a value compose rendered is there and not false, empty or zero.
func set(raw json.RawMessage) bool {
	v := string(bytes.TrimSpace(raw))
	return v != "" && v != "null" && v != "false" && v != `""` && v != "0" && v != `"0"` && v != "{}"
}

// inside reports whether path is dir or under it.
func inside(dir, path string) bool {
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

func isSocket(p string) bool { return filepath.Base(p) == "docker.sock" }

// CheckRules checks the rendered config of the user's compose file against Tender's rules. dir is
// the service's folder. The error is a sentence that names the service in the compose file and says
// how to fix the file.
func CheckRules(r Rendered, dir string) error {
	for _, name := range SortedKeys(r.Services) {
		sv := r.Services[name]
		fail := func(format string, a ...any) error {
			return fmt.Errorf("service %s in the compose file %s", name, fmt.Sprintf(format, a...))
		}
		switch {
		case set(sv.Build):
			return fail("has build, and Tender deploys images only: build the image elsewhere, push it to a registry, and give its image instead")
		case sv.Image == "":
			return fail("has no image: give the image it runs, such as nginx:1.27")
		case len(sv.Ports) > 0:
			return fail("publishes ports, which Tender's routes do instead: remove ports from %s and add a route to its port, such as --route %s:80:tailscale", name, name)
		case sv.ContainerName != "":
			return fail("sets container_name, which Tender leaves to compose: remove container_name from %s", name)
		case sv.Privileged:
			return fail("is privileged, which gives it the whole machine: remove privileged from %s", name)
		case len(sv.Devices) > 0:
			return fail("has devices, which reach the machine's hardware: remove devices from %s", name)
		}
		for _, ns := range []struct{ key, value string }{{"network_mode", sv.NetworkMode}, {"pid", sv.Pid}, {"ipc", sv.Ipc}, {"uts", sv.Uts},
			{"userns_mode", sv.UsernsMode}} {
			if ns.value == "host" {
				return fail("sets %s: host, which shares the machine's own: remove it from %s", ns.key, name)
			}
		}
		for _, opt := range sv.SecurityOpt {
			if strings.Contains(opt, "unconfined") {
				return fail("has security_opt %s, which turns its protection off: remove it from %s", opt, name)
			}
		}
		for _, c := range sv.CapAdd {
			if cp := strings.TrimPrefix(strings.ToUpper(c), "CAP_"); slices.Contains(deniedCaps, cp) {
				return fail("adds the capability %s, which is enough to take over the machine: remove it from cap_add", cp)
			}
		}
		for _, m := range sv.Volumes {
			switch {
			case isSocket(m.Source) || isSocket(m.Target):
				return fail("mounts the Docker socket, which controls every container on the machine: remove that mount from %s", name)
			case m.Type == "bind" && !inside(dir, m.Source):
				return fail("mounts %s from the machine, outside %s: mount a path relative to the compose file, such as ./data:%s, "+
					"or a named volume", m.Source, dir, m.Target)
			}
		}
		for _, raw := range sv.EnvFile {
			p := envFilePath(raw)
			if p != "" && !inside(dir, p) {
				return fail("reads env_file %s, outside %s: use a path relative to the compose file, or the service's env", p, dir)
			}
		}
		if !set(sv.MemLimit) && (sv.Deploy == nil || !set(sv.Deploy.Resources.Limits.Memory)) {
			return fail("has no memory limit, which Tender's memory-high alert needs: add mem_limit, such as mem_limit: 512m, to %s", name)
		}
	}
	for _, name := range SortedKeys(r.Volumes) {
		v := r.Volumes[name]
		switch {
		case name == "." || name == "..":
			return fmt.Errorf("volume %q can't be a volume's name: give it another", name)
		case set(v.External):
			return fmt.Errorf("volume %s is external, and Tender keeps a service's volumes in its folder so they are backed up: remove external from it", name)
		case v.Driver != "" || set(v.DriverOpts):
			return fmt.Errorf("volume %s sets its own driver or driver_opts, and Tender binds every named volume to the service's folder so it is backed up: "+
				"remove driver and driver_opts from it", name)
		}
	}
	for _, name := range SortedKeys(r.Networks) {
		if set(r.Networks[name].External) {
			return fmt.Errorf("network %s is external, and a service keeps to its own networks: remove external from it", name)
		}
	}
	for _, refs := range []struct {
		what string
		list map[string]fileRef
	}{{"secret", r.Secrets}, {"config", r.Configs}} {
		for _, name := range SortedKeys(refs.list) {
			if f := refs.list[name].File; f != "" && !inside(dir, f) {
				return fmt.Errorf("%s %s reads %s, outside %s: use a file relative to the compose file, such as one of the service's files", refs.what, name, f, dir)
			}
		}
	}
	return nil
}

// envFilePath is the path of one env_file entry, which compose renders as a string or as
// {"path": ...}.
func envFilePath(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(raw, &o)
	return o.Path
}

// CheckRouted is an error unless each route names a service in the rendered config.
func CheckRouted(r Rendered, routes []Published) error {
	for _, p := range routes {
		if _, ok := r.Services[p.Container]; !ok {
			return fmt.Errorf("a route goes to %s, which the compose file has no service for. Its services are %s: route to one of them",
				p.Container, strings.Join(SortedKeys(r.Services), ", "))
		}
	}
	return nil
}

// Override is compose.tender.yaml, as JSON, which YAML reads too: each routed container's ports on
// 127.0.0.1 only, Tender's label with the service's name on every container, restart unless-stopped
// where the file sets no policy, and every named volume bound to volumes/<volume> in dir.
func Override(r Rendered, name, dir string, routes []Published) ([]byte, error) {
	type svc struct {
		Labels  map[string]string `json:"labels"`
		Restart string            `json:"restart,omitempty"`
		Ports   []string          `json:"ports,omitempty"`
	}
	type vol struct {
		Driver     string            `json:"driver"`
		DriverOpts map[string]string `json:"driver_opts"`
	}
	out := struct {
		Services map[string]svc `json:"services"`
		Volumes  map[string]vol `json:"volumes,omitempty"`
	}{Services: map[string]svc{}, Volumes: map[string]vol{}}
	for n, sv := range r.Services {
		s := svc{Labels: map[string]string{Label: name}}
		if sv.Restart == "" {
			s.Restart = "unless-stopped"
		}
		out.Services[n] = s
	}
	for _, p := range routes {
		s := out.Services[p.Container]
		s.Ports = append(s.Ports, fmt.Sprintf("127.0.0.1:%d:%d", p.LocalPort, p.Port))
		out.Services[p.Container] = s
	}
	for v := range r.Volumes {
		out.Volumes[v] = vol{Driver: "local", DriverOpts: map[string]string{"type": "none", "o": "bind", "device": filepath.Join(dir, "volumes", v)}}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	return append(b, '\n'), err
}

// imageHost is the registry host an image is pulled from: docker.io unless its first part names a
// host, with a dot, a port or localhost.
func imageHost(image string) string {
	first, _, ok := strings.Cut(image, "/")
	if ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first
	}
	return "docker.io"
}
