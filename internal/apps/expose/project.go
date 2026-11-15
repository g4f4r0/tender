package expose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Tender's own containers, Caddy and cloudflared, run in the compose project tender, each from its
// own compose file. They are not services people deploy and don't follow those rules: they use host networking.
const project = "tender"

// The images of Tender's own containers, pinned by digest so every machine runs the same build.
// The digest is the multi-platform index, so amd64 and arm64 machines both pull their own image.
const (
	CaddyImage       = "caddy:2.11.9@sha256:4e2425f6009577e7c651010f16679c28f7d12df3e7b1914f941a7a3370746b3c"
	CloudflaredImage = "cloudflare/cloudflared:2026.11.0@sha256:b94b96edf2c9c4360f8879c8580cfaa99077f19c849ec595ca2f711b27d4f344"
)

// keptOutput is how much of a command's output its error keeps: the end, where the reason is.
const keptOutput = 4 << 10

// tail keeps the last n bytes written to it.
type tail struct {
	n   int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.n; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

// run runs name with args, never through a shell, with env added to this process's environment,
// and returns its stdout. Its error names the command and keeps the end of its output.
func run(ctx context.Context, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout bytes.Buffer
	both := &tail{n: keptOutput}
	cmd.Stdout = io.MultiWriter(&stdout, both)
	cmd.Stderr = both
	err := cmd.Run()
	if err != nil {
		out := strings.TrimSpace(string(both.buf))
		if out != "" {
			out = ": " + strings.Join(strings.Fields(out), " ")
		}
		return stdout.String(), fmt.Errorf("%s %s failed (%w)%s", name, strings.Join(args, " "), err, out)
	}
	return stdout.String(), nil
}

// service is one of Tender's own containers: one service in its own compose file of project tender.
type service struct {
	docker string // the docker program
	file   string // its compose.yaml
	name   string // caddy or cloudflared
}

func (s service) compose(ctx context.Context, env []string, args ...string) (string, error) {
	return run(ctx, env, s.docker, append([]string{"compose", "-p", project, "-f", s.file}, args...)...)
}

// running says whether the container runs now.
func (s service) running(ctx context.Context) (bool, error) {
	out, err := s.compose(ctx, nil, "ps", "--status", "running", "--quiet", s.name)
	return strings.TrimSpace(out) != "", err
}

// up creates or starts the container, and recreates it when its compose file or env changed.
func (s service) up(ctx context.Context, env []string) error {
	_, err := s.compose(ctx, env, "up", "--detach", s.name)
	return err
}

// remove stops and removes the container, then its compose file, which says it was started. Without
// the file there is nothing of Tender's to remove, so it runs nothing.
func (s service) remove(ctx context.Context) error {
	if _, err := os.Stat(s.file); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := s.compose(ctx, nil, "rm", "--stop", "--force", s.name); err != nil {
		return err
	}
	return os.Remove(s.file)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// writeFile replaces path with data, atomically, and says whether it changed.
func writeFile(path string, data []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() == mode {
			return false, nil
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(mode); err != nil {
		return false, errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(data); err != nil {
		return false, errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return false, errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}

var hostPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// CheckHost is an error unless host is a full DNS name in lowercase, such as app.example.com: at
// least two labels of letters, digits and inner dashes, and a top-level label that starts with a
// letter, so an IP address is not a host.
func CheckHost(host string) error {
	if len(host) > 253 || !hostPattern.MatchString(host) {
		return fmt.Errorf("%q is not a full hostname in lowercase, such as app.example.com", host)
	}
	return nil
}

// hostRoutes checks the routes of an adapter that serves hosts, and returns them sorted by host.
func hostRoutes(adapter string, routes []Route) ([]Route, error) {
	out := slices.Clone(routes)
	slices.SortFunc(out, func(a, b Route) int { return strings.Compare(a.Host, b.Host) })
	for i, r := range out {
		if err := CheckHost(r.Host); err != nil {
			return nil, fmt.Errorf("the %s route of %s/%s: %w", adapter, r.Service, r.Container, err)
		}
		if r.LocalPort < 1 || r.LocalPort > 65535 {
			return nil, fmt.Errorf("the %s route of %s/%s has local port %d, which is not a TCP port", adapter, r.Service, r.Container, r.LocalPort)
		}
		if i > 0 && out[i-1].Host == r.Host {
			return nil, fmt.Errorf("two %s routes have the host %s: a host takes one route", adapter, r.Host)
		}
	}
	return out, nil
}
