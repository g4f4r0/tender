package expose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Tailscale serves each tailscale route with Tailscale Serve: HTTPS on the machine's tailnet name,
// on the route's port, proxied to the route's local port. It keeps the ports it set in
// tailscale-ports.json in StateDir, and never touches another port.
type Tailscale struct {
	Bin      string // the tailscale program; "tailscale" finds it on PATH
	StateDir string // the daemon's state folder
}

// tailscaleState is tailscale-ports.json: each HTTPS port Tender serves, with the address it proxies to.
type tailscaleState struct {
	Ports map[string]string `json:"ports"`
}

// Name is tailscale.
func (t *Tailscale) Name() string { return "tailscale" }

func (t *Tailscale) bin() string {
	if t.Bin != "" {
		return t.Bin
	}
	return "tailscale"
}

func (t *Tailscale) statePath() string { return filepath.Join(t.StateDir, "tailscale-ports.json") }

func (t *Tailscale) load() (tailscaleState, error) {
	s := tailscaleState{Ports: map[string]string{}}
	b, err := os.ReadFile(t.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s is damaged: %w. Delete it after turning off the ports Tender served (tailscale serve status)", t.statePath(), err)
	}
	if s.Ports == nil {
		s.Ports = map[string]string{}
	}
	return s, nil
}

func (t *Tailscale) save(s tailscaleState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.statePath() + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.statePath())
}

// run runs tailscale with args and returns what it printed, or an error with its own words.
func (t *Tailscale) run(ctx context.Context, args ...string) ([]byte, error) {
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, t.bin(), args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return nil, errNoTailscale
	}
	if err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("tailscale %s failed: %s", strings.Join(args, " "), msg)
	}
	return out.Bytes(), nil
}

var errNoTailscale = errors.New("Tailscale isn't installed on this machine, so its tailscale routes can't be served. " +
	"Install it and run tailscale up, or expose the routes another way")

// serving reads which HTTPS ports Tailscale Serve uses, each with the address its / proxies to.
func (t *Tailscale) serving(ctx context.Context) (map[string]string, error) {
	b, err := t.run(ctx, "serve", "status", "--json")
	if err != nil {
		return nil, err
	}
	var cfg struct {
		TCP map[string]json.RawMessage `json:"TCP"`
		Web map[string]struct {
			Handlers map[string]struct {
				Proxy string `json:"Proxy"`
			} `json:"Handlers"`
		} `json:"Web"`
	}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("tailscale serve status --json printed something that isn't its JSON: %w", err)
		}
	}
	out := map[string]string{}
	for port := range cfg.TCP {
		out[port] = ""
	}
	for hostPort, web := range cfg.Web {
		i := strings.LastIndexByte(hostPort, ':')
		if i < 0 {
			continue
		}
		out[hostPort[i+1:]] = web.Handlers["/"].Proxy
	}
	return out, nil
}

// Apply makes Tailscale Serve serve exactly routes, of the ports Tender owns: it serves each
// route's HTTPS port and turns off the ports it served before that are no longer routes. A route
// whose port Serve already uses for something else fails with a sentence, and the others still go.
func (t *Tailscale) Apply(ctx context.Context, routes []Route) error {
	state, err := t.load()
	if err != nil {
		return err
	}
	if len(routes) == 0 && len(state.Ports) == 0 {
		return nil // nothing to serve and nothing to take back: Tailscale needn't be there at all
	}
	used, err := t.serving(ctx)
	if err != nil {
		return err
	}
	want := map[string]Route{}
	for _, r := range routes {
		want[strconv.Itoa(r.HTTPSPort)] = r
	}
	var errs []error
	for _, port := range sortedPorts(want) {
		r := want[port]
		target := "http://127.0.0.1:" + strconv.Itoa(r.LocalPort)
		_, owned := state.Ports[port]
		current, inUse := used[port]
		switch {
		case inUse && !owned:
			errs = append(errs, fmt.Errorf("tailscale serve on this machine already uses HTTPS port %s for something else, so the route to %s in %s "+
				"can't have it. Give the route another https_port, or free the port with: tailscale serve --https=%s off", port, r.Container, r.Service, port))
			continue
		case owned && inUse && current == target && state.Ports[port] == target:
			continue
		}
		if _, err := t.run(ctx, "serve", "--bg", "--https="+port, target); err != nil {
			errs = append(errs, fmt.Errorf("could not serve the route to %s in %s on HTTPS port %s: %w", r.Container, r.Service, port, err))
			continue
		}
		state.Ports[port] = target
		if err := t.save(state); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	for _, port := range sortedPorts(state.Ports) {
		if _, keep := want[port]; keep {
			continue
		}
		if _, inUse := used[port]; inUse {
			if _, err := t.run(ctx, "serve", "--https="+port, "off"); err != nil {
				errs = append(errs, fmt.Errorf("could not stop serving HTTPS port %s, which no route has any more: %w", port, err))
				continue
			}
		}
		delete(state.Ports, port)
		if err := t.save(state); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	return errors.Join(errs...)
}

// DNSName is the machine's name on its tailnet, such as apps.tail1234.ts.net, or "" when
// Tailscale isn't installed or doesn't say.
func (t *Tailscale) DNSName(ctx context.Context) (string, error) {
	b, err := t.run(ctx, "status", "--json")
	if errors.Is(err, errNoTailscale) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var st struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return "", fmt.Errorf("tailscale status --json printed something that isn't its JSON: %w", err)
	}
	return strings.TrimSuffix(st.Self.DNSName, "."), nil
}

func sortedPorts[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i])
		b, _ := strconv.Atoi(out[j])
		return a < b
	})
	return slices.Clip(out)
}
