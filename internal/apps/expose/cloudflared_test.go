package expose

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const tunnelToken = "eyJhIjoiMDEyMzQ1Njc4OWFiY2RlZiIsInQiOiJ0dW5uZWwiLCJzIjoic2VjcmV0In0marker"

// The first cloudflare route starts cloudflared in project tender with host networking and the
// pinned image, with the token in its environment only: never in an argument, the compose file or
// an error.
func TestCloudflaredRunsWithTheTokenInItsEnvironment(t *testing.T) {
	bin := fakes(t)
	dir := filepath.Join(t.TempDir(), "cloudflared")
	c := &Cloudflared{Dir: dir, Token: tunnelToken}
	if err := c.Apply(context.Background(), []Route{web, api}); err != nil {
		t.Fatal(err)
	}
	compose, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"image: " + CloudflaredImage, "network_mode: host", "restart: unless-stopped",
		`command: ["tunnel", "--metrics", "127.0.0.1:0", "run"]`, "      - TUNNEL_TOKEN\n"} {
		if !strings.Contains(string(compose), w) {
			t.Errorf("compose.yaml lacks %q:\n%s", w, compose)
		}
	}
	if strings.Contains(string(compose), tunnelToken) {
		t.Error("the token is in the compose file")
	}
	log := lines(t, filepath.Join(bin, "docker.log"))
	file := filepath.Join(dir, "compose.yaml")
	if !slices.Equal(log, []string{"compose -p tender -f " + file + " up --detach cloudflared|TUNNEL_TOKEN=" + tunnelToken}) {
		t.Errorf("docker calls %q", log)
	}

	// A new token reaches compose, which recreates the container.
	reset(t, bin)
	c.Token = tunnelToken + "2"
	if err := c.Apply(context.Background(), []Route{web}); err != nil {
		t.Fatal(err)
	}
	if log := lines(t, filepath.Join(bin, "docker.log")); len(log) != 1 || !strings.HasSuffix(log[0], "|TUNNEL_TOKEN="+tunnelToken+"2") {
		t.Errorf("docker calls %q", log)
	}

	// A failing start doesn't show the token, even when Docker prints it.
	touch(t, filepath.Join(bin, "fail-up"))
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(strings.Replace(fakeDocker, "is broken", "is broken with $TUNNEL_TOKEN", 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	err = c.Apply(context.Background(), []Route{web})
	if err == nil || !strings.Contains(err.Error(), "start cloudflared") || strings.Contains(err.Error(), tunnelToken) || !strings.Contains(err.Error(), "***") {
		t.Errorf("failing start: %v", err)
	}
}

// After the last route cloudflared is removed; with routes but no token, nothing starts.
func TestCloudflaredStopsAfterTheLastRoute(t *testing.T) {
	bin := fakes(t)
	dir := filepath.Join(t.TempDir(), "cloudflared")
	c := &Cloudflared{Dir: dir, Token: tunnelToken}
	ctx := context.Background()
	if err := c.Apply(ctx, []Route{web}); err != nil {
		t.Fatal(err)
	}
	reset(t, bin)
	if err := c.Apply(ctx, nil); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "compose.yaml")
	if got := dockerCalls(t, bin); !slices.Equal(got, []string{"compose -p tender -f " + file + " rm --stop --force cloudflared"}) {
		t.Errorf("docker calls %q", got)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("compose.yaml stays: %v", err)
	}
	reset(t, bin)
	if err := c.Apply(ctx, nil); err != nil || len(dockerCalls(t, bin)) != 0 {
		t.Errorf("empty apply: %v %q", err, dockerCalls(t, bin))
	}
	c.Token = ""
	if err := c.Apply(ctx, []Route{web}); err == nil || !strings.Contains(err.Error(), "no Cloudflare tunnel token") || len(dockerCalls(t, bin)) != 0 {
		t.Errorf("without a token: %v %q", err, dockerCalls(t, bin))
	}
}
