package install

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/config"
)

const (
	newBinaryPath      = binaryPath + ".new"      // a downloaded release, while it is checked
	previousBinaryPath = binaryPath + ".previous" // the binary before the last update, to roll back to

	// Update results, as the daemon reports them on its update action.
	resultCompleted = "completed"
	resultFailed    = "failed"
)

// The waits of an update, unless a test sets others on the Installer.
const (
	defaultBusyWait    = 30 * time.Minute
	defaultHealthWait  = 2 * time.Minute
	defaultHealthEvery = 2 * time.Second
	busyEvery          = 5 * time.Second
)

// fetchRelease downloads the tender binary of a release and checks it: the release's SHA256SUMS
// must carry a valid signature by one of the release keys, and the binary must match it.
func (in *Installer) fetchRelease(ctx context.Context, tag string) ([]byte, error) {
	base := in.DownloadURL + "/" + tag + "/"
	sums, err := get(ctx, base+checksumsAsset, maxSumsBytes)
	if err != nil {
		return nil, fmt.Errorf("cannot download the checksums of Tender %s, so nothing was changed. Check the version and this machine's internet access: %w", tag, err)
	}
	sig, err := get(ctx, base+SignatureAsset, maxSumsBytes)
	if err != nil {
		return nil, fmt.Errorf("cannot download the signature of Tender %s (%s), so nothing was changed. Tender installs only signed releases, "+
			"which every release from v0.3.0 on is: %w", tag, SignatureAsset, err)
	}
	if !verifySums(in.ReleaseKeys, sums, sig) {
		return nil, fmt.Errorf("the signature of Tender %s does not match its checksums, so nothing was changed. The release may have been "+
			"tampered with: report it at %s", tag, issuesURL)
	}
	bin, err := get(ctx, base+releaseAsset, maxReleaseBytes)
	if err != nil {
		return nil, fmt.Errorf("cannot download Tender %s, so nothing was changed. Check the version and this machine's internet access: %w", tag, err)
	}
	want := checksumFor(sums, releaseAsset)
	if want == "" {
		return nil, fmt.Errorf("the checksums of Tender %s do not list %s, so nothing was changed. Report it at %s", tag, releaseAsset, issuesURL)
	}
	if got := sum(bin); got != want {
		return nil, fmt.Errorf("the download of Tender %s does not match its checksum (got %s, want %s), so nothing was changed. Try again, and report it at %s if it repeats", tag, got, want, issuesURL)
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

// installedServices reports which services are installed, under their current or their old names.
func (in *Installer) installedServices() (daemon, hub bool) {
	return in.installed(daemonUnitPath) || in.installed(oldUnitPath(oldDaemonUnit)),
		in.installed(hubUnitPath) || in.installed(oldUnitPath(oldHubUnit))
}

// Update runs "sudo tender update": it installs a release on this machine, the latest when version
// is empty, with the same checks, wait, health check and rollback as the update job. Naming a
// version by hand is the one way to downgrade.
func (in *Installer) Update(ctx context.Context, version string) error {
	tag, err := in.pick(ctx, version, version != "")
	if err != nil || tag == "" {
		return err
	}
	return in.swap(ctx, tag)
}

// Apply runs "tender update --apply VERSION", the job the daemon starts as the tenderupdate unit
// when the hub rolls out a release. It refuses a downgrade, and leaves how it ended in the daemon's
// state folder, which the daemon reports to the hub.
func (in *Installer) Apply(ctx context.Context, version string) error {
	tag, err := "", error(nil)
	if DevBuild(in.Version) {
		err = fmt.Errorf("this machine runs a development build of Tender, %s, which may be newer than %s, so nothing was changed. "+
			"Root can update it by hand with: sudo tender update", in.Version, version)
	} else {
		tag, err = in.pick(ctx, version, false)
	}
	if err == nil && tag != "" {
		err = in.swap(ctx, tag)
	}
	result := config.UpdateResult{Version: strings.TrimPrefix(version, "v"), Status: resultCompleted}
	switch {
	case err != nil:
		result.Status, result.Output = resultFailed, sentence(err.Error())
	case tag == "":
		result.Output = "Tender " + result.Version + " was already installed."
	default:
		result.Output = fmt.Sprintf("Updated Tender %s to %s.", in.Version, tag)
	}
	if daemon, _ := in.installedServices(); daemon {
		if werr := config.WriteUpdateResult(in.path(config.DefaultStateDir), result); werr != nil {
			in.say("Could not save how the update ended for the daemon to report: %v", werr)
		}
	}
	return err
}

// sentence ends s with a period.
func sentence(s string) string {
	if s == "" || strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

// pick checks the release to install and returns its tag, or "" when there is nothing to do. It
// refuses drafts, prereleases and, unless downgrade, a release older than this binary.
func (in *Installer) pick(ctx context.Context, version string, downgrade bool) (string, error) {
	if err := in.preflight(); err != nil {
		return "", err
	}
	if daemon, hub := in.installedServices(); !daemon && !hub {
		return "", errors.New("Tender is not installed on this machine yet. Run \"sudo tender install daemon\" or \"sudo tender install hub\" first")
	}
	tag := ""
	if version != "" {
		var err error
		if tag, err = Tag(version); err != nil {
			return "", err
		}
	}
	rel, err := GetRelease(ctx, in.ReleasesURL, tag)
	switch {
	case err != nil && tag == "":
		return "", fmt.Errorf("cannot look up the latest Tender release. Check this machine's internet access, or pick a release with --version: %w", err)
	case err != nil:
		return "", fmt.Errorf("cannot find Tender %s on GitHub, so nothing was changed. Check the version: %w", tag, err)
	}
	if err := rel.Official(); err != nil {
		return "", fmt.Errorf("%w, so nothing was changed", err)
	}
	switch c := CompareVersions(rel.Tag, in.Version); {
	case c == 0:
		in.say("Tender %s is already installed. Nothing to update.", in.Version)
		return "", nil
	case c < 0 && version == "":
		in.say("This machine runs Tender %s, newer than the latest release %s. Nothing to update.", in.Version, rel.Tag)
		return "", nil
	case c < 0 && !downgrade:
		return "", fmt.Errorf("Tender %s is older than %s, the version on this machine, and an update never downgrades, so nothing was changed. "+
			"Root can downgrade this machine by hand with: sudo tender update --version %s", rel.Tag, in.Version, rel.Tag)
	}
	return rel.Tag, nil
}

// swap installs release tag: it downloads and verifies it, checks that the new binary runs and
// reads this machine's configs, keeps the running binary as tender.previous, waits for a running
// backup or action, swaps the binary and restarts the services. If they aren't healthy on the new
// version within HealthWait, it puts the previous binary back and restarts them again.
func (in *Installer) swap(ctx context.Context, tag string) error {
	daemon, hub := in.installedServices()
	want := strings.TrimPrefix(tag, "v")
	in.say("Updating Tender %s to %s", in.Version, tag)
	bin, err := in.fetchRelease(ctx, tag)
	if err != nil {
		return err
	}
	in.say("Downloaded Tender %s and checked its signature and checksums", tag)

	if _, err := in.write(ctx, newBinaryPath, bin, 0o755, ""); err != nil {
		return err
	}
	defer func() { _ = os.Remove(in.path(newBinaryPath)) }()
	out, err := in.run(ctx, in.path(newBinaryPath), "version")
	if err != nil || strings.TrimSpace(string(out)) != "Tender "+want {
		return fmt.Errorf("the downloaded Tender %s does not run as Tender %s (it printed %q), so nothing was changed. Report it at %s",
			tag, want, strings.TrimSpace(string(out)), issuesURL)
	}
	if _, err := in.run(ctx, in.path(newBinaryPath), "install", "check"); err != nil {
		return fmt.Errorf("Tender %s can't read this machine's configs, so nothing was changed: %w", tag, err)
	}
	previous, err := os.ReadFile(in.path(binaryPath))
	if err != nil {
		return err
	}
	if _, err := in.write(ctx, previousBinaryPath, previous, 0o755, ""); err != nil {
		return err
	}
	if daemon {
		release, err := in.waitIdle(ctx)
		if err != nil {
			return fmt.Errorf("%w, so nothing was changed. Try the update again later", err)
		}
		defer release()
		if err := os.Remove(in.path(filepath.Join(config.DefaultStateDir, config.ReportedVersionFile))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(in.path(newBinaryPath), in.path(binaryPath)); err != nil {
		return err
	}
	in.say("Installed Tender %s at %s", tag, in.path(binaryPath))
	out, err = in.run(ctx, in.path(binaryPath), "install", "refresh")
	_, _ = in.Out.Write(out)
	if err == nil {
		err = in.healthy(ctx, want, daemon, hub)
	}
	if err == nil {
		in.say("\nUpdated to Tender %s.", tag)
		return nil
	}
	in.say("Tender %s is not healthy. Rolling back to Tender %s.", tag, in.Version)
	if rerr := in.rollback(ctx, previous); rerr != nil {
		return fmt.Errorf("Tender %s failed its health check (%v), and rolling back to Tender %s failed too: %w. "+
			"Put %s back at %s and run \"sudo tender install refresh\"", tag, err, in.Version, rerr, in.path(previousBinaryPath), in.path(binaryPath))
	}
	return fmt.Errorf("Tender %s failed its health check, so it rolled back to Tender %s: %w", tag, in.Version, err)
}

// waitIdle waits for a running backup or action to finish, up to BusyWait, and keeps new ones from
// starting until release.
func (in *Installer) waitIdle(ctx context.Context) (func(), error) {
	dir := in.path(config.DefaultStateDir)
	if release, err := config.WaitIdle(ctx, dir, 0, busyEvery); err == nil {
		return release, nil
	}
	in.say("Waiting up to %s for the running backup or action to finish", in.BusyWait)
	return config.WaitIdle(ctx, dir, in.BusyWait, min(busyEvery, in.BusyWait))
}

// rollback puts the previous binary back and restarts the services on it.
func (in *Installer) rollback(ctx context.Context, previous []byte) error {
	if _, err := in.write(ctx, binaryPath, previous, 0o755, ""); err != nil {
		return err
	}
	out, err := in.run(ctx, in.path(binaryPath), "install", "refresh")
	_, _ = in.Out.Write(out)
	return err
}

// healthy waits up to HealthWait for the installed services to be healthy on version, and says
// what isn't when they aren't.
func (in *Installer) healthy(ctx context.Context, version string, daemon, hub bool) error {
	deadline := time.Now().Add(in.HealthWait)
	for {
		err := in.health(ctx, version, daemon, hub)
		if err == nil || !time.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("after %s, %w", in.HealthWait, err)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(in.HealthEvery, time.Until(deadline))):
		}
	}
}

// health is nil when the units run, the daemon reported to the hub on version and the hub answers.
func (in *Installer) health(ctx context.Context, version string, daemon, hub bool) error {
	if daemon {
		if _, err := in.Run(ctx, "systemctl", "is-active", "--quiet", "tenderd"); err != nil {
			return errors.New("tenderd is not running")
		}
		if got := config.ReadReportedVersion(in.path(config.DefaultStateDir)); got != version {
			return fmt.Errorf("the daemon has not reported to the hub on Tender %s", version)
		}
	}
	if hub {
		if _, err := in.Run(ctx, "systemctl", "is-active", "--quiet", "tenderhub"); err != nil {
			return errors.New("tenderhub is not running")
		}
		if err := in.hubHealth(ctx); err != nil {
			return err
		}
	}
	return nil
}

// hubHealth asks this machine's hub for /v1/health.
func (in *Installer) hubHealth(ctx context.Context) error {
	env, err := os.ReadFile(in.path(hubEnvPath))
	if err != nil {
		return err
	}
	listen := envValue(env, "TENDER_LISTEN")
	if listen == "" {
		listen = defaultListen
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/v1/health", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("the hub does not answer on /v1/health")
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the hub's /v1/health answered %d", resp.StatusCode)
	}
	return nil
}

// Check loads the installed daemon config and hub settings as this binary reads them, so an update
// can stop before it swaps in a binary that won't start.
func (in *Installer) Check() error {
	daemon, hub := in.installedServices()
	if daemon {
		if _, err := config.LoadDaemon(in.path(daemonConfigPath)); err != nil {
			return err
		}
	}
	if hub {
		b, err := os.ReadFile(in.path(hubEnvPath))
		if err != nil {
			return err
		}
		vars := envVars(b)
		if _, err := config.LoadHubEnv(func(k string) string { return vars[k] }); err != nil {
			return err
		}
	}
	return nil
}

// Refresh brings every installed service up to date with this binary: it re-renders the units,
// re-checks the pinned restic, keeps the config and credentials, and restarts the services. On a
// daemon installed before v0.3.0 it also moves the daemon to root, as the unit now says.
func (in *Installer) Refresh(ctx context.Context) error {
	daemon, hub := in.installedServices()
	if !daemon && !hub {
		return errors.New("Tender is not installed on this machine yet. Run \"sudo tender install daemon\" or \"sudo tender install hub\" first")
	}
	in.Refreshing = true
	if daemon {
		if err := in.Daemon(ctx, DaemonOptions{}); err != nil {
			return err
		}
	}
	if hub {
		return in.Hub(ctx, HubOptions{})
	}
	return nil
}
