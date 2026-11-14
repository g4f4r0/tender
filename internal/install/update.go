package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	latestURL   = "https://api.github.com/repos/g4f4r0/tender/releases/latest"
	downloadURL = "https://github.com/g4f4r0/tender/releases/download"

	releaseAsset    = "tender_linux_amd64"
	checksumsAsset  = "SHA256SUMS"
	maxReleaseBytes = 128 << 20
	maxSumsBytes    = 1 << 20
)

// releaseTag is the shape of a tender release tag. A tag goes into a URL, so nothing else passes.
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// latestTag asks GitHub for the newest release.
func (in *Installer) latestTag(ctx context.Context) (string, error) {
	body, err := get(ctx, in.LatestURL, maxSumsBytes)
	if err != nil {
		return "", fmt.Errorf("could not look up the latest tender release (%w): check this machine's internet access, or pick a release with --version", err)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &release); err != nil || !releaseTag.MatchString(release.TagName) {
		return "", errors.New("GitHub did not name a tender release: pick one with --version, such as v0.1.0")
	}
	return release.TagName, nil
}

// fetchRelease downloads the tender binary of a release and checks it against the release's SHA256SUMS.
func (in *Installer) fetchRelease(ctx context.Context, tag string) ([]byte, error) {
	base := in.DownloadURL + "/" + tag + "/"
	sums, err := get(ctx, base+checksumsAsset, maxSumsBytes)
	if err != nil {
		return nil, fmt.Errorf("could not download the checksums of tender %s (%w): nothing was changed; check the version and this machine's internet access", tag, err)
	}
	bin, err := get(ctx, base+releaseAsset, maxReleaseBytes)
	if err != nil {
		return nil, fmt.Errorf("could not download tender %s (%w): nothing was changed; check the version and this machine's internet access", tag, err)
	}
	want := checksumFor(sums, releaseAsset)
	if want == "" {
		return nil, fmt.Errorf("the checksums of tender %s do not list %s: nothing was changed; report this at github.com/g4f4r0/tender/issues", tag, releaseAsset)
	}
	if got := sum(bin); got != want {
		return nil, fmt.Errorf("the download of tender %s does not match its checksum (got %s, want %s): nothing was changed; try again, and report this at github.com/g4f4r0/tender/issues if it repeats", tag, got, want)
	}
	return bin, nil
}

// checksumFor finds the SHA-256 of name in sha256sum output, or returns "".
func checksumFor(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

func (in *Installer) installed(unitPath string) bool {
	_, err := os.Stat(in.path(unitPath))
	return err == nil
}

// Update replaces the tender binary with a release (the latest when version is empty), then has the
// new binary refresh the installed services. The old binary stays until the download is verified.
func (in *Installer) Update(ctx context.Context, version string) error {
	if err := in.preflight(); err != nil {
		return err
	}
	if !in.installed(agentUnitPath) && !in.installed(hubUnitPath) {
		return errors.New("tender is not installed on this machine yet: run tender install agent or tender install hub first")
	}
	tag := version
	if tag != "" && !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if tag != "" && !releaseTag.MatchString(tag) {
		return fmt.Errorf("%q is not a tender version: use a release such as v0.1.0", version)
	}
	if tag == "" {
		var err error
		if tag, err = in.latestTag(ctx); err != nil {
			return err
		}
	}
	if strings.TrimPrefix(tag, "v") == in.Version {
		in.say("tender %s is already installed. Nothing to do.", in.Version)
		return nil
	}

	in.say("Updating tender %s to %s.", in.Version, tag)
	bin, err := in.fetchRelease(ctx, tag)
	if err != nil {
		return err
	}
	if _, err := in.write(ctx, binaryPath, bin, 0o755, ""); err != nil {
		return err
	}
	out, err := in.run(ctx, in.path(binaryPath), "install", "refresh")
	_, _ = in.Out.Write(out)
	if err != nil {
		return fmt.Errorf("tender %s is installed, but refreshing the services failed: %w\nFix the problem above, then run: sudo tender install refresh", tag, err)
	}
	in.say("\nUpdated to tender %s.", tag)
	return nil
}

// Refresh brings every installed service up to date with this binary: it re-renders the units,
// re-checks the pinned restic, keeps the config and credentials, and restarts the services.
func (in *Installer) Refresh(ctx context.Context) error {
	agent, hub := in.installed(agentUnitPath), in.installed(hubUnitPath)
	if !agent && !hub {
		return errors.New("tender is not installed on this machine yet: run tender install agent or tender install hub first")
	}
	in.Refreshing = true
	if agent {
		in.say("Refreshing the agent.")
		if err := in.Agent(ctx, AgentOptions{}); err != nil {
			return err
		}
	}
	if hub {
		in.say("Refreshing the hub.")
		return in.Hub(ctx, HubOptions{})
	}
	return nil
}
