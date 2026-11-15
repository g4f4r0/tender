package deploy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Rendered is the part of "docker compose config --format json" that Tender's rules and override
// read. Compose has resolved it: interpolated, merged, with relative paths made absolute, and each
// $ written as $$. CheckRules checks every key of it against the keys Tender allows.
type Rendered struct {
	Name     string                     `json:"name"`
	Services map[string]renderedService `json:"services"`
	Volumes  map[string]renderedVolume  `json:"volumes"`
	Networks map[string]struct {
		External json.RawMessage `json:"external"`
		Name     string          `json:"name"`
		Driver   string          `json:"driver"`
	} `json:"networks"`
	Secrets map[string]fileRef `json:"secrets"`
	Configs map[string]fileRef `json:"configs"`

	raw []byte // the whole of it, for the keys
}

type renderedService struct {
	Image         string            `json:"image"`
	Build         json.RawMessage   `json:"build"`
	Ports         []json.RawMessage `json:"ports"`
	NetworkMode   string            `json:"network_mode"`
	Pid           string            `json:"pid"`
	Ipc           string            `json:"ipc"`
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
			Reservations struct {
				Devices []json.RawMessage `json:"devices"`
			} `json:"reservations"`
		} `json:"resources"`
	} `json:"deploy"`
	Restart string            `json:"restart"`
	EnvFile []json.RawMessage `json:"env_file"`
	Sysctls json.RawMessage   `json:"sysctls"`
	Logging *struct {
		Driver string `json:"driver"`
	} `json:"logging"`
}

type mount struct {
	Type   string `json:"type"`
	Source string `json:"source"`
	Target string `json:"target"`
	Bind   *struct {
		Propagation string `json:"propagation"`
	} `json:"bind"`
}

type renderedVolume struct {
	External   json.RawMessage `json:"external"`
	Driver     string          `json:"driver"`
	DriverOpts json.RawMessage `json:"driver_opts"`
	Name       string          `json:"name"`
}

type fileRef struct {
	File     string          `json:"file"`
	External json.RawMessage `json:"external"`
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
	r.raw = b
	return r, nil
}

// The capabilities a service may not add: each is enough to take over the machine.
var deniedCaps = []string{"ALL", "SYS_ADMIN", "SYS_MODULE", "SYS_PTRACE", "SYS_RAWIO", "DAC_READ_SEARCH", "NET_ADMIN", "BPF", "PERFMON",
	"MAC_ADMIN", "MAC_OVERRIDE", "SYS_BOOT", "SYS_TIME"}

// The keys Tender allows, at each level of the rendered config. Keys starting x- are allowed
// everywhere: compose ignores them. A key that isn't here is refused, so a key compose learns later
// is refused until Tender knows it is safe.
var (
	topKeys     = keys("name", "services", "networks", "volumes", "secrets", "configs")
	serviceKeys = keys("image", "command", "entrypoint", "environment", "env_file", "healthcheck", "depends_on", "restart", "labels",
		"logging", "user", "working_dir", "volumes", "networks", "expose", "mem_limit", "mem_reservation", "memswap_limit", "cpus",
		"cpu_shares", "pids_limit", "deploy", "ulimits", "stop_signal", "stop_grace_period", "init", "read_only", "tmpfs", "shm_size",
		"cap_drop", "cap_add", "security_opt", "profiles", "hostname", "domainname", "dns", "dns_search", "dns_opt", "extra_hosts",
		"sysctls", "tty", "stdin_open", "platform", "pull_policy", "scale", "network_mode", "pid", "ipc", "secrets", "configs")
	deployKeys       = keys("resources", "replicas", "restart_policy", "labels")
	resourcesKeys    = keys("limits", "reservations")
	limitsKeys       = keys("cpus", "memory", "pids")
	reservationsKeys = keys("cpus", "memory")
	mountKeys        = keys("type", "source", "target", "read_only", "bind", "volume", "tmpfs", "consistency")
	bindKeys         = keys("create_host_path", "selinux", "propagation")
	volumeMountKeys  = keys("nocopy")
	tmpfsMountKeys   = keys("size", "mode")
	serviceNetKeys   = keys("aliases", "ipv4_address", "ipv6_address", "priority", "gw_priority")
	volumeKeys       = keys("name", "labels", "external", "driver", "driver_opts")
	networkKeys      = keys("name", "driver", "ipam", "internal", "attachable", "enable_ipv4", "enable_ipv6", "labels", "external")
	fileRefKeys      = keys("name", "file", "environment", "content", "external")
	logDrivers       = keys("", "json-file", "local", "journald", "none")
)

func keys(list ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range list {
		m[k] = true
	}
	return m
}

// object reads raw as a JSON object; null is an empty one.
func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	err := json.Unmarshal(raw, &m)
	return m, err
}

// unknownKey returns the first key of raw, an object, that allowed doesn't have, in order.
func unknownKey(raw json.RawMessage, allowed map[string]bool) (string, error) {
	m, err := object(raw)
	if err != nil {
		return "", err
	}
	for _, k := range SortedKeys(m) {
		if !allowed[k] && !strings.HasPrefix(k, "x-") {
			return k, nil
		}
	}
	return "", nil
}

// set reports whether a value compose rendered is there and not false, empty or zero.
func set(raw json.RawMessage) bool {
	v := string(bytes.TrimSpace(raw))
	return v != "" && v != "null" && v != "false" && v != `""` && v != "0" && v != `"0"` && v != "{}"
}

// memBytes reads a memory size as compose renders it, a number of bytes as a string or a number,
// or with a unit such as 512m. ok is false when it can't be read.
func memBytes(raw json.RawMessage) (n int64, ok bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(bytes.TrimSpace(raw))
	}
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "b")
	mult := int64(1)
	if s != "" {
		switch s[len(s)-1] {
		case 'k':
			mult = 1 << 10
		case 'm':
			mult = 1 << 20
		case 'g':
			mult = 1 << 30
		}
		if mult > 1 {
			s = s[:len(s)-1]
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return 0, false
		}
		return int64(f * float64(mult)), true
	}
	return v * mult, true
}

// inside reports whether path is dir or under it.
func inside(dir, path string) bool {
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// owned reports whether path, inside dir, is the folder itself or something in it that Tender
// writes: the compose file, the override, .env, the list of files, its temporary files, and
// volumes/, where the named volumes keep their data. A container that could write one of them
// could change what the next deploy runs, or bind a volume to anywhere.
func owned(dir, path string) bool {
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	if err != nil {
		return true
	}
	first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return rel == "." || slices.Contains(reserved, first) || first == "volumes" || strings.HasPrefix(first, ".tender-")
}

func isSocket(p string) bool { return filepath.Base(p) == "docker.sock" }

// namespaceOK reports whether value, a service's network_mode, pid or ipc, keeps the service off
// the machine's namespaces and other containers': one of allowed, or service:<name> of a service
// in the same file. container:<name> could join any container's, such as one on the host network.
func namespaceOK(value string, allowed []string, services map[string]renderedService) bool {
	if other, ok := strings.CutPrefix(value, "service:"); ok {
		_, in := services[other]
		return in
	}
	return slices.Contains(allowed, value)
}

// CheckRules checks the rendered config of the user's compose file against Tender's rules. dir is
// the service's folder, whose name is the compose project's. The error is a sentence that names the
// service in the compose file and says how to fix the file.
func CheckRules(r Rendered, dir string) error {
	project := filepath.Base(dir)
	if k, err := unknownKey(r.raw, topKeys); err != nil || k != "" {
		return errors.Join(err, refusedTop(k))
	}
	for _, name := range SortedKeys(r.Services) {
		if err := checkService(r, name, dir); err != nil {
			return err
		}
	}
	top, _ := object(r.raw)
	vols, _ := object(top["volumes"])
	for _, name := range SortedKeys(r.Volumes) {
		v := r.Volumes[name]
		switch {
		case name == "." || name == ".." || !segmentPattern.MatchString(name):
			return fmt.Errorf("volume %q can't be a volume's name: use letters, digits, dots, dashes and underscores", name)
		case set(v.External):
			return fmt.Errorf("volume %s is external, and Tender keeps a service's volumes in its folder so they are backed up: remove external from it", name)
		case v.Driver != "" || set(v.DriverOpts):
			return fmt.Errorf("volume %s sets its own driver or driver_opts, and Tender binds every named volume to the service's folder so it is backed up: "+
				"remove driver and driver_opts from it", name)
		case v.Name != "" && v.Name != project+"_"+name:
			return fmt.Errorf("volume %s sets name %s, which could be another service's volume: remove name from it", name, v.Name)
		}
		if k, err := unknownKey(vols[name], volumeKeys); err != nil || k != "" {
			return errors.Join(err, fmt.Errorf("volume %s sets %s, which Tender's rules don't allow: remove %s from it", name, k, k))
		}
	}
	nets, _ := object(top["networks"])
	for _, name := range SortedKeys(r.Networks) {
		n := r.Networks[name]
		switch {
		case set(n.External):
			return fmt.Errorf("network %s is external, and a service keeps to its own networks: remove external from it", name)
		case n.Driver != "" && n.Driver != "bridge":
			return fmt.Errorf("network %s has driver %s, and a service's networks are bridge networks of its own: remove driver from it", name, n.Driver)
		case n.Name != "" && n.Name != project+"_"+name:
			return fmt.Errorf("network %s sets name %s, which could be another network on the machine: remove name from it", name, n.Name)
		}
		if k, err := unknownKey(nets[name], networkKeys); err != nil || k != "" {
			return errors.Join(err, fmt.Errorf("network %s sets %s, which Tender's rules don't allow: remove %s from it", name, k, k))
		}
	}
	for _, refs := range []struct {
		what string
		list map[string]fileRef
	}{{"secret", r.Secrets}, {"config", r.Configs}} {
		raws, _ := object(top[refs.what+"s"])
		for _, name := range SortedKeys(refs.list) {
			ref := refs.list[name]
			switch {
			case set(ref.External):
				return fmt.Errorf("%s %s is external: give it a file relative to the compose file, its content or an environment variable instead", refs.what, name)
			case ref.File != "" && !inside(dir, ref.File):
				return fmt.Errorf("%s %s reads %s, outside %s: use a file relative to the compose file, such as one of the service's files", refs.what, name, ref.File, dir)
			}
			if k, err := unknownKey(raws[name], fileRefKeys); err != nil || k != "" {
				return errors.Join(err, fmt.Errorf("%s %s sets %s, which Tender's rules don't allow: remove %s from it", refs.what, name, k, k))
			}
		}
	}
	return nil
}

func refusedTop(k string) error {
	if k == "" {
		return nil
	}
	return fmt.Errorf("the compose file has %s at its top, which Tender's rules don't allow: remove %s from it", k, k)
}

// checkService checks the service called name in r.
func checkService(r Rendered, name, dir string) error {
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
	case len(sv.Devices) > 0 || sv.Deploy != nil && len(sv.Deploy.Resources.Reservations.Devices) > 0:
		return fail("has devices, which reach the machine's hardware: remove devices from %s", name)
	}
	top, _ := object(r.raw)
	services, _ := object(top["services"])
	raw := services[name]
	if k, err := unknownKey(raw, serviceKeys); err != nil || k != "" {
		return errors.Join(err, fail("sets %s, which Tender's rules don't allow: remove %s from %s", k, k, name))
	}
	for _, ns := range []struct {
		key, value string
		allowed    []string
	}{
		{"network_mode", sv.NetworkMode, []string{"", "none", "bridge", "default"}},
		{"pid", sv.Pid, []string{""}},
		{"ipc", sv.Ipc, []string{"", "none", "private", "shareable"}},
	} {
		switch {
		case ns.value == "host":
			return fail("sets %s: host, which shares the machine's own: remove it from %s", ns.key, name)
		case strings.HasPrefix(ns.value, "container:"):
			return fail("sets %s: %s, which joins another container's, such as one on the machine's own network: "+
				"use service:<name> of a service in the same compose file, or remove it from %s", ns.key, ns.value, name)
		case !namespaceOK(ns.value, ns.allowed, r.Services):
			return fail("sets %s: %s, which Tender's rules don't allow: remove it from %s, or use service:<name> of a service in the same compose file",
				ns.key, ns.value, name)
		}
	}
	for _, opt := range sv.SecurityOpt {
		o := strings.ReplaceAll(strings.ToLower(opt), "=", ":")
		if strings.Contains(o, "unconfined") || o == "label:disable" {
			return fail("has security_opt %s, which turns its protection off: remove it from %s", opt, name)
		}
	}
	for _, c := range sv.CapAdd {
		if cp := strings.TrimPrefix(strings.ToUpper(c), "CAP_"); slices.Contains(deniedCaps, cp) {
			return fail("adds the capability %s, which is enough to take over the machine: remove it from cap_add", cp)
		}
	}
	if err := checkMounts(sv, raw, name, dir, fail); err != nil {
		return err
	}
	for _, e := range sv.EnvFile {
		p := envFilePath(e)
		if p != "" && !inside(dir, p) {
			return fail("reads env_file %s, outside %s: use a path relative to the compose file, or the service's env", p, dir)
		}
	}
	if err := checkSysctls(sv.Sysctls, fail); err != nil {
		return err
	}
	if sv.Logging != nil && !logDrivers[sv.Logging.Driver] {
		return fail("logs with driver %s, and Tender reads a container's logs from Docker: use json-file, local or journald", sv.Logging.Driver)
	}
	if err := checkDeploy(raw, fail); err != nil {
		return err
	}
	if err := checkNetworks(raw, fail); err != nil {
		return err
	}
	return checkMemory(sv, name, fail)
}

// checkMounts checks the service's volumes: the kinds of mount, their options, and that a bind
// stays inside dir and off what Tender owns there.
func checkMounts(sv renderedService, raw json.RawMessage, name, dir string, fail func(string, ...any) error) error {
	svc, _ := object(raw)
	var raws []json.RawMessage
	_ = json.Unmarshal(svc["volumes"], &raws)
	for i, m := range sv.Volumes {
		switch {
		case isSocket(m.Source) || isSocket(m.Target):
			return fail("mounts the Docker socket, which controls every container on the machine: remove that mount from %s", name)
		case m.Type != "bind" && m.Type != "volume" && m.Type != "tmpfs":
			return fail("has a mount of type %s, and Tender allows bind, volume and tmpfs: remove it from %s", m.Type, name)
		case m.Type == "bind" && !inside(dir, m.Source):
			return fail("mounts %s from the machine, outside %s: mount a path relative to the compose file, such as ./data:%s, "+
				"or a named volume", m.Source, dir, m.Target)
		case m.Type == "bind" && owned(dir, m.Source):
			return fail("mounts %s, which is the service's folder or what Tender keeps in it: mount a folder of its own, such as ./data:%s, "+
				"or a named volume", m.Source, m.Target)
		case m.Bind != nil && m.Bind.Propagation != "" && m.Bind.Propagation != "private" && m.Bind.Propagation != "rprivate":
			return fail("mounts %s with propagation %s, which shares mounts with the machine: remove propagation from %s", m.Source, m.Bind.Propagation, name)
		}
		if i >= len(raws) {
			continue
		}
		if k, err := unknownKey(raws[i], mountKeys); err != nil || k != "" {
			return errors.Join(err, fail("mounts %s with %s, which Tender's rules don't allow: remove %s from that mount", m.Target, k, k))
		}
		opts, _ := object(raws[i])
		for _, sub := range []struct {
			key     string
			allowed map[string]bool
		}{{"bind", bindKeys}, {"volume", volumeMountKeys}, {"tmpfs", tmpfsMountKeys}} {
			if o, ok := opts[sub.key]; ok && set(o) {
				if k, err := unknownKey(o, sub.allowed); err != nil || k != "" {
					return errors.Join(err, fail("mounts %s with %s.%s, which Tender's rules don't allow: remove it from that mount", m.Target, sub.key, k))
				}
			}
		}
	}
	return nil
}

// checkSysctls allows only net.* sysctls, which are the container's own.
func checkSysctls(raw json.RawMessage, fail func(string, ...any) error) error {
	if !set(raw) {
		return nil
	}
	var names []string
	var m map[string]json.RawMessage
	var list []string
	switch {
	case json.Unmarshal(raw, &m) == nil:
		names = SortedKeys(m)
	case json.Unmarshal(raw, &list) == nil:
		for _, l := range list {
			k, _, _ := strings.Cut(l, "=")
			names = append(names, k)
		}
	default:
		return fail("has sysctls Tender can't read")
	}
	for _, k := range names {
		if !strings.HasPrefix(k, "net.") {
			return fail("sets sysctl %s, and Tender allows only net.* sysctls, which are the container's own: remove it", k)
		}
	}
	return nil
}

// checkDeploy allows the deploy keys that limit and reserve resources, without devices.
func checkDeploy(raw json.RawMessage, fail func(string, ...any) error) error {
	svc, _ := object(raw)
	d, ok := svc["deploy"]
	if !ok || !set(d) {
		return nil
	}
	if k, err := unknownKey(d, deployKeys); err != nil || k != "" {
		return errors.Join(err, fail("sets deploy.%s, which Tender's rules don't allow: remove it", k))
	}
	dm, _ := object(d)
	if !set(dm["resources"]) {
		return nil
	}
	if k, err := unknownKey(dm["resources"], resourcesKeys); err != nil || k != "" {
		return errors.Join(err, fail("sets deploy.resources.%s, which Tender's rules don't allow: remove it", k))
	}
	rm, _ := object(dm["resources"])
	for _, sub := range []struct {
		key     string
		allowed map[string]bool
	}{{"limits", limitsKeys}, {"reservations", reservationsKeys}} {
		if o, ok := rm[sub.key]; ok && set(o) {
			if k, err := unknownKey(o, sub.allowed); err != nil || k != "" {
				return errors.Join(err, fail("sets deploy.resources.%s.%s, which Tender's rules don't allow: remove it", sub.key, k))
			}
		}
	}
	return nil
}

// checkNetworks allows a service's networks with their addresses and aliases only.
func checkNetworks(raw json.RawMessage, fail func(string, ...any) error) error {
	svc, _ := object(raw)
	n, ok := svc["networks"]
	if !ok || !set(n) {
		return nil
	}
	m, err := object(n)
	if err != nil {
		return nil // a list of names
	}
	for _, net := range SortedKeys(m) {
		if !set(m[net]) {
			continue
		}
		if k, err := unknownKey(m[net], serviceNetKeys); err != nil || k != "" {
			return errors.Join(err, fail("joins network %s with %s, which Tender's rules don't allow: remove it", net, k))
		}
	}
	return nil
}

// checkMemory needs a memory limit above zero: 0 and -1 mean none.
func checkMemory(sv renderedService, name string, fail func(string, ...any) error) error {
	limits := 0
	for _, l := range []struct {
		key string
		raw json.RawMessage
	}{{"mem_limit", sv.MemLimit}, {"deploy.resources.limits.memory", deployMemory(sv)}} {
		if len(bytes.TrimSpace(l.raw)) == 0 || string(bytes.TrimSpace(l.raw)) == "null" {
			continue
		}
		n, ok := memBytes(l.raw)
		if !ok || n <= 0 {
			return fail("sets %s to %s, which is no memory limit: give %s a size, such as %s: 512m", l.key, bytes.TrimSpace(l.raw), name, l.key)
		}
		limits++
	}
	if limits == 0 {
		return fail("has no memory limit, which Tender's memory-high alert needs: add mem_limit, such as mem_limit: 512m, to %s", name)
	}
	return nil
}

func deployMemory(sv renderedService) json.RawMessage {
	if sv.Deploy == nil {
		return nil
	}
	return sv.Deploy.Resources.Limits.Memory
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

// CheckLinks walks each path in dir that compose will read or bind for r, a part at a time, and
// refuses one that goes through a symbolic link or isn't a plain folder or file: a container that
// can write in a bound folder could have made one point anywhere on the machine. A path that isn't
// there yet is fine: compose makes it. The daemon calls it right before up; a running container
// can still race it, so the main guard is that a bind stays off what Tender owns.
func CheckLinks(r Rendered, dir string) error {
	type use struct{ what, path string }
	var uses []use
	for _, name := range SortedKeys(r.Services) {
		sv := r.Services[name]
		for _, m := range sv.Volumes {
			if m.Type == "bind" {
				uses = append(uses, use{"service " + name + " in the compose file mounts", m.Source})
			}
		}
		for _, e := range sv.EnvFile {
			if p := envFilePath(e); p != "" {
				uses = append(uses, use{"service " + name + " in the compose file reads env_file", p})
			}
		}
	}
	for _, name := range SortedKeys(r.Secrets) {
		uses = append(uses, use{"secret " + name + " reads", r.Secrets[name].File})
	}
	for _, name := range SortedKeys(r.Configs) {
		uses = append(uses, use{"config " + name + " reads", r.Configs[name].File})
	}
	for _, u := range uses {
		p := strings.ReplaceAll(u.path, "$$", "$")
		if p == "" || !inside(dir, p) {
			continue
		}
		rel, _ := filepath.Rel(dir, filepath.Clean(p))
		cur := dir
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if part == "." {
				continue
			}
			cur = filepath.Join(cur, part)
			fi, err := os.Lstat(cur)
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if err != nil {
				return fmt.Errorf("%s %s, and Tender could not check %s: %w", u.what, p, cur, err)
			}
			if fi.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("%s %s, and %s in its folder is a symbolic link, which could reach outside the folder: delete the link, "+
					"or use another path", u.what, p, cur)
			}
			if !fi.Mode().IsDir() && !fi.Mode().IsRegular() {
				return fmt.Errorf("%s %s, and %s in its folder isn't a plain folder or file: delete it, or use another path", u.what, p, cur)
			}
		}
	}
	return nil
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
