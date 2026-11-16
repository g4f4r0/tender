package expose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTailscale is a tailscale program that logs each call and prints the file status for
// "serve status --json" and the file self for "status --json".
type fakeTailscale struct {
	t                     *testing.T
	bin, log, status, dns string
}

func newFakeTailscale(t *testing.T) *fakeTailscale {
	t.Helper()
	dir := t.TempDir()
	f := &fakeTailscale{t: t, bin: filepath.Join(dir, "tailscale"), log: filepath.Join(dir, "calls.log"),
		status: filepath.Join(dir, "status.json"), dns: filepath.Join(dir, "self.json")}
	script := `#!/bin/sh
case "$*" in
  "serve status --json") cat ` + f.status + ` ;;
  "status --json") cat ` + f.dns + ` ;;
  *) echo "$*" >> ` + f.log + ` ;;
esac
`
	if err := os.WriteFile(f.bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	f.serving(`{}`)
	return f
}

// serving sets what tailscale serve status --json prints.
func (f *fakeTailscale) serving(json string) {
	f.t.Helper()
	if err := os.WriteFile(f.status, []byte(json), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// calls returns the calls that changed something since the last time, and forgets them.
func (f *fakeTailscale) calls() []string {
	f.t.Helper()
	b, _ := os.ReadFile(f.log)
	_ = os.Remove(f.log)
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestTailscale(t *testing.T) {
	f := newFakeTailscale(t)
	ts := &Tailscale{Bin: f.bin, StateDir: t.TempDir()}
	ctx := context.Background()
	web := Route{Service: "notes", Container: "web", LocalPort: 20000, HTTPSPort: 443}
	api := Route{Service: "notes", Container: "api", LocalPort: 20001, HTTPSPort: 8443}

	if err := ts.Apply(ctx, []Route{web, api}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(); strings.Join(got, ";") != "serve --bg --https=443 http://127.0.0.1:20000;serve --bg --https=8443 http://127.0.0.1:20001" {
		t.Errorf("first apply = %q", got)
	}

	// Again, with Serve as Tender left it: nothing changes.
	f.serving(`{"TCP":{"443":{"HTTPS":true},"8443":{"HTTPS":true}},"Web":{
		"apps.tail1234.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:20000"}}},
		"apps.tail1234.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:20001"}}}}}`)
	if err := ts.Apply(ctx, []Route{web, api}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(); got != nil {
		t.Errorf("an unchanged apply ran %q", got)
	}

	// api's route goes: Tender turns off only its own port.
	if err := ts.Apply(ctx, []Route{web}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(); strings.Join(got, ";") != "serve --https=8443 off" {
		t.Errorf("after a route went = %q", got)
	}
	f.serving(`{"TCP":{"443":{"HTTPS":true},"10000":{"HTTPS":true}},"Web":{
		"apps.tail1234.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:20000"}}},
		"apps.tail1234.ts.net:10000":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}}}`)

	// A port someone else serves on is refused with a sentence, and the other routes still go.
	other := Route{Service: "blog", Container: "web", LocalPort: 20002, HTTPSPort: 10000}
	moved := Route{Service: "notes", Container: "web", LocalPort: 20005, HTTPSPort: 443}
	err := ts.Apply(ctx, []Route{moved, other})
	if err == nil || err.Error() != "tailscale serve on this machine already uses HTTPS port 10000 for something else, so the route to web in blog can't have it. "+
		"Give the route another https_port, or free the port with: tailscale serve --https=10000 off" {
		t.Errorf("a port in use: %v", err)
	}
	if mine, others := For(err, "blog", true); mine == nil || others != nil {
		t.Errorf("the port in use isn't blog's route error: mine %v, others %v", mine, others)
	}
	if mine, others := For(err, "notes", true); mine != nil || others == nil {
		t.Errorf("blog's port in use is notes': mine %v, others %v", mine, others)
	}
	if got := f.calls(); strings.Join(got, ";") != "serve --bg --https=443 http://127.0.0.1:20005" {
		t.Errorf("with a port in use = %q", got)
	}

	// No routes: Tender takes back all of its own, and leaves the other port alone.
	if err := ts.Apply(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(); strings.Join(got, ";") != "serve --https=443 off" {
		t.Errorf("no routes = %q", got)
	}
	b, err := os.ReadFile(filepath.Join(ts.StateDir, "tailscale-ports.json"))
	if err != nil || strings.TrimSpace(string(b)) != "{\n  \"ports\": {}\n}" {
		t.Errorf("state = %s, %v", b, err)
	}
}

func TestTailscaleNotInstalled(t *testing.T) {
	ts := &Tailscale{Bin: filepath.Join(t.TempDir(), "tailscale"), StateDir: t.TempDir()}
	ctx := context.Background()
	if err := ts.Apply(ctx, nil); err != nil {
		t.Errorf("no routes without Tailscale: %v", err)
	}
	if err := ts.Apply(ctx, []Route{{Service: "notes", Container: "web", LocalPort: 20000, HTTPSPort: 443}}); err == nil ||
		!strings.HasPrefix(err.Error(), "Tailscale isn't installed on this machine") {
		t.Errorf("a route without Tailscale: %v", err)
	}
	if name, err := ts.DNSName(ctx); name != "" || err != nil {
		t.Errorf("DNSName without Tailscale = %q, %v", name, err)
	}
}

func TestTailscaleDNSName(t *testing.T) {
	f := newFakeTailscale(t)
	if err := os.WriteFile(f.dns, []byte(`{"Self":{"DNSName":"apps.tail1234.ts.net."}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := &Tailscale{Bin: f.bin, StateDir: t.TempDir()}
	if name, err := ts.DNSName(context.Background()); name != "apps.tail1234.ts.net" || err != nil {
		t.Errorf("DNSName = %q, %v", name, err)
	}
}
