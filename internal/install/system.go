package install

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// localTypes are the file systems that hold a machine's own data. Network, memory and
// container file systems stay out of the backup paths.
var localTypes = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "vfat": true, "f2fs": true}

// mountPaths lists the mount points of the local file systems, from findmnt.
func (in *Installer) mountPaths(ctx context.Context) ([]string, error) {
	out, err := in.run(ctx, "findmnt", "--real", "--list", "--json", "--output", "TARGET,FSTYPE")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Filesystems []struct{ Target, Fstype string }
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("read findmnt output: %w", err)
	}
	seen := map[string]bool{}
	var paths []string
	for _, f := range doc.Filesystems {
		if localTypes[f.Fstype] && !seen[f.Target] {
			seen[f.Target] = true
			paths = append(paths, f.Target)
		}
	}
	if len(paths) == 0 {
		return []string{"/"}, nil
	}
	sort.Strings(paths)
	return paths, nil
}

// serve publishes the hub on this node's Tailscale HTTPS port 443, unless something else already uses it.
func (in *Installer) serve(ctx context.Context, target string) {
	command := "tailscale serve --bg --https=443 " + target
	out, err := in.Run(ctx, "tailscale", "serve", "status", "--json")
	var status struct {
		TCP map[string]json.RawMessage
		Web map[string]struct {
			Handlers map[string]struct{ Proxy string }
		}
	}
	if err == nil {
		err = json.Unmarshal(out, &status)
	}
	if err != nil {
		in.say("\nTailscale did not answer (%v). When it is up, run: %s", err, command)
		return
	}
	switch {
	case status.TCP["443"] == nil:
		if _, err := in.run(ctx, "tailscale", "serve", "--bg", "--https=443", target); err != nil {
			in.say("\nTailscale Serve failed (%v). Run: %s", err, command)
			return
		}
	case servesTarget(status.Web, target):
		in.say("\nTailscale Serve already publishes %s on 443.", target)
	default:
		in.say("\nTailscale port 443 on this node already serves something else, so tender left it alone.\nTo publish the hub there, run: %s", command)
		return
	}
	out, err = in.Run(ctx, "tailscale", "status", "--json")
	var self struct{ Self struct{ DNSName string } }
	if err == nil {
		err = json.Unmarshal(out, &self)
	}
	if err != nil || self.Self.DNSName == "" {
		in.say("\nThe hub is published, but its name is unknown: see tailscale status.")
		return
	}
	in.say("\nHub URL: https://%s", strings.TrimSuffix(self.Self.DNSName, "."))
}

func servesTarget(web map[string]struct {
	Handlers map[string]struct{ Proxy string }
}, target string) bool {
	for host, site := range web {
		if !strings.HasSuffix(host, ":443") {
			continue
		}
		for _, h := range site.Handlers {
			if h.Proxy == target {
				return true
			}
		}
	}
	return false
}
