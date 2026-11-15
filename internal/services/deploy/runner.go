package deploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/g4f4r0/tender/internal/apps/expose"
	"github.com/g4f4r0/tender/internal/client"
)

// The daemon's limits on compose: how long up waits for the services to be healthy, and how much of
// compose's output an action keeps.
const (
	waitTimeoutSeconds = 300
	maxOutputBytes     = 64 << 10
)

// Runner deploys and removes the machine's managed services and applies its exposures. The
// actions domain's daemon calls Deploy and Remove; Run applies the exposures once when the daemon
// starts.
type Runner struct {
	Hub     *client.Client
	Machine string
	Log     *slog.Logger
	// Dir holds a folder per managed service: /var/lib/tender/services.
	Dir string
	// Docker is the docker program; "docker" finds it on PATH. Socket, when set, is the Docker
	// socket compose talks to.
	Docker, Socket string
	// Exposers are the exposure adapters, one per way a route is exposed.
	Exposers []expose.Exposer
	// TailnetName returns the machine's name on its tailnet, which the hub puts in a tailscale
	// route's URL; nil or "" when there is none.
	TailnetName func(context.Context) (string, error)
	// Every is how long Run waits before asking the hub again after an error.
	Every time.Duration

	mu       sync.Mutex // one deploy, remove or exposure change at a time
	reported string     // the tailnet name last sent to the hub
}

// NewRunner returns a runner for machine that keeps the managed services in dir.
func NewRunner(hub *client.Client, machine, dir string, log *slog.Logger) *Runner {
	return &Runner{Hub: hub, Machine: machine, Dir: dir, Log: log, Docker: "docker", Every: time.Minute}
}

func (d *Runner) path(rest string) string { return "/v1/machines/" + url.PathEscape(d.Machine) + rest }

// Run applies the machine's exposures as the hub has them, once, retrying until the hub answers or
// ctx ends, and tells the hub the machine's tailnet name.
func (d *Runner) Run(ctx context.Context) {
	logged := ""
	for ctx.Err() == nil {
		var ex Exposures
		err := d.Hub.Do(ctx, http.MethodGet, d.path("/exposures"), "", nil, &ex)
		if err == nil {
			d.mu.Lock()
			err = d.expose(ctx, ex)
			d.mu.Unlock()
			if err != nil {
				d.Log.Warn("could not apply the exposures of this machine's services", "error", err)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		if err.Error() != logged {
			d.Log.Warn("could not get this machine's exposures from the hub", "error", err)
			logged = err.Error()
		}
		select {
		case <-ctx.Done():
		case <-time.After(d.Every):
		}
	}
}

// expose applies ex with every adapter, each with its own routes, then tells the hub the tailnet
// name when it changed. An adapter's failure doesn't stop the others.
func (d *Runner) expose(ctx context.Context, ex Exposures) error {
	var errs []error
	known := map[string]bool{}
	for _, e := range d.Exposers {
		known[e.Name()] = true
		if t, ok := e.(interface{ SetTunnelToken(string) }); ok {
			token := ""
			if ex.CloudflareTunnelToken != nil {
				token = *ex.CloudflareTunnelToken
			}
			t.SetTunnelToken(token)
		}
		if err := e.Apply(ctx, ExposeRoutes(ex.Routes, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("the %s routes: %w", e.Name(), err))
		}
	}
	for _, r := range ex.Routes {
		if !known[r.Expose] {
			errs = append(errs, fmt.Errorf("this daemon can't expose %s routes, such as the one to %s in %s: update Tender on this machine", r.Expose, r.Container, r.Service))
			known[r.Expose] = true
		}
	}
	d.reportTailnet(ctx)
	return errors.Join(errs...)
}

// reportTailnet tells the hub the machine's tailnet name, when it differs from the last one sent.
func (d *Runner) reportTailnet(ctx context.Context) {
	if d.TailnetName == nil {
		return
	}
	name, err := d.TailnetName(ctx)
	if err != nil {
		d.Log.Warn("could not read this machine's tailnet name", "error", err)
		return
	}
	if name == "" || name == d.reported {
		return
	}
	if err := d.Hub.Do(ctx, http.MethodPut, d.path("/exposures/status"), client.NewKey(), ExposureStatus{TailscaleDNSName: &name}, nil); err != nil {
		d.Log.Warn("could not tell the hub this machine's tailnet name", "error", err)
		return
	}
	d.reported = name
}

// tail keeps the last maxOutputBytes of what is written to it.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2*maxOutputBytes {
		t.b = append([]byte{}, t.b[len(t.b)-maxOutputBytes:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	b := t.b
	if len(b) > maxOutputBytes {
		b = b[len(b)-maxOutputBytes:]
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return string(b)
}

// compose runs docker compose in dir with args, step naming its command in an error. It writes what
// compose prints to out; stdout, when given, takes compose's standard output instead.
func (d *Runner) compose(ctx context.Context, dir, config, step string, out io.Writer, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, d.Docker, append([]string{"compose"}, args...)...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	if stdout != nil {
		cmd.Stdout = stdout
	}
	// Only what compose needs: the daemon's own environment stays out of the compose file's
	// interpolation, which reads the service's .env.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + config, "DOCKER_CONFIG=" + config}
	if d.Socket != "" {
		cmd.Env = append(cmd.Env, "DOCKER_HOST=unix://"+d.Socket)
	}
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return fmt.Errorf("docker isn't installed on this machine, or not on the daemon's PATH. Install Docker with its compose plugin")
	case errors.As(err, &exit):
		return fmt.Errorf("docker compose %s exited with status %d", step, exit.ExitCode())
	}
	return err
}

// dockerConfig makes a DOCKER_CONFIG folder for one deploy, with the logins of the registries that
// the images are on. The caller deletes it.
func dockerConfig(logins []Login, images []string) (string, error) {
	dir, err := os.MkdirTemp("", "tender-docker-")
	if err != nil {
		return "", err
	}
	auths := map[string]map[string]string{}
	for _, l := range logins {
		for _, img := range images {
			if imageHost(img) == l.Host {
				key := l.Host
				if key == "docker.io" {
					key = "https://index.docker.io/v1/"
				}
				auths[key] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(l.Username + ":" + l.Password))}
			}
		}
	}
	b, _ := json.Marshal(map[string]any{"auths": auths})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		return "", errors.Join(err, os.RemoveAll(dir))
	}
	return dir, nil
}

// Folder is the folder of the managed service called name.
func (d *Runner) Folder(name string) string { return filepath.Join(d.Dir, name) }

// Deploy checks the service's compose file against Tender's rules, writes the service's folder and
// the override, pulls the images and starts the containers with docker compose, then applies the
// machine's exposures. It returns the end of what compose printed. A rule that fails is an error
// naming the service in the compose file and the fix. A failed deploy leaves the containers as
// compose left them.
func (d *Runner) Deploy(ctx context.Context, raw json.RawMessage) (string, error) {
	var dep Deployment
	if err := json.Unmarshal(raw, &dep); err != nil || dep.Name == "" {
		return "", errors.New("the hub sent no service with this deploy; it may have been removed meanwhile")
	}
	if err := CheckName(dep.Name); err != nil {
		return "", err
	}
	if err := CheckFiles(dep.Files); err != nil {
		return "", err
	}
	if err := CheckEnv(dep.Env); err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return "", err
	}
	dir := d.Folder(dep.Name)
	if err := writeFolder(dir, dep); err != nil {
		return "", err
	}
	config, err := dockerConfig(nil, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(config) }()
	out := &tail{}
	var rendered bytes.Buffer
	if err := d.compose(ctx, dir, config, "config", out, &rendered, "-p", dep.Name, "-f", "compose.yaml", "config", "--format", "json"); err != nil {
		return "", fmt.Errorf("docker compose could not read the compose file: %v:\n%s", err, out.String())
	}
	r, err := ParseRendered(rendered.Bytes())
	rendered.Reset() // it holds the env, interpolated
	if err != nil {
		return "", err
	}
	if err := CheckRules(r, dir); err != nil {
		return "", err
	}
	if err := CheckRouted(r, dep.Routes); err != nil {
		return "", err
	}
	ov, err := Override(r, dep.Name, dir, dep.Routes)
	if err != nil {
		return "", err
	}
	if err := writeOverride(dir, ov); err != nil {
		return "", err
	}
	if err := makeVolumes(dir, r); err != nil {
		return "", err
	}
	if len(dep.Registries) > 0 {
		var images []string
		for _, sv := range r.Services {
			images = append(images, sv.Image)
		}
		_ = os.RemoveAll(config)
		if config, err = dockerConfig(dep.Registries, images); err != nil {
			return "", err
		}
	}
	files := []string{"-p", dep.Name, "-f", "compose.yaml", "-f", "compose.tender.yaml"}
	if err := d.compose(ctx, dir, config, "pull", out, nil, append(files, "pull")...); err != nil {
		return out.String(), fmt.Errorf("%v: it could not pull the images. The end of its output:\n%s", err, out.String())
	}
	if err := d.compose(ctx, dir, config, "up", out, nil, append(files, "up", "-d", "--remove-orphans", "--wait", "--wait-timeout",
		strconv.Itoa(waitTimeoutSeconds))...); err != nil {
		return out.String(), fmt.Errorf("%v: the containers did not all start and turn healthy. The end of its output:\n%s", err, out.String())
	}
	if err := d.expose(ctx, dep.Exposures); err != nil {
		return out.String(), fmt.Errorf("the service runs, but its routes could not all be exposed: %w", err)
	}
	return out.String(), nil
}

func writeOverride(dir string, content []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return writeAtomic(root, "compose.tender.yaml", content, 0o600)
}

// Remove stops and removes the service's containers with docker compose down, takes its routes out
// of the exposures, and then deletes the service's folder with deleteData, or else moves it to
// .removed/.
func (d *Runner) Remove(ctx context.Context, raw json.RawMessage, deleteData bool) (string, error) {
	var dep Deployment
	if err := json.Unmarshal(raw, &dep); err != nil || dep.Name == "" {
		return "", errors.New("the hub sent no service with this remove")
	}
	if err := CheckName(dep.Name); err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	dir := d.Folder(dep.Name)
	run := dir
	if _, err := os.Stat(dir); err != nil {
		run = d.Dir
		if err := os.MkdirAll(run, 0o700); err != nil {
			return "", err
		}
	}
	config, err := dockerConfig(nil, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(config) }()
	out := &tail{}
	if err := d.compose(ctx, run, config, "down", out, nil, "-p", dep.Name, "down", "--remove-orphans"); err != nil {
		return out.String(), fmt.Errorf("%v: the service's containers may still run. The end of its output:\n%s", err, out.String())
	}
	var errs []error
	if err := d.expose(ctx, dep.Exposures); err != nil {
		errs = append(errs, fmt.Errorf("its containers are gone, but its routes could not all be taken out: %w", err))
	}
	if run == dir {
		kept, err := keptName(filepath.Join(d.Dir, ".removed"), dep.Name)
		if err != nil {
			return out.String(), errors.Join(append(errs, err)...)
		}
		if err := os.Rename(dir, kept); err != nil {
			return out.String(), errors.Join(append(errs, fmt.Errorf("could not move the service's folder aside: %w", err))...)
		}
		switch {
		case !deleteData:
			_, _ = fmt.Fprintf(out, "Kept the service's folder, data and all, in %s. Delete it when you no longer need it: sudo rm -rf %s\n", kept, kept)
		case os.RemoveAll(kept) != nil:
			_, _ = fmt.Fprintf(out, "Could not delete all of the service's folder: some files belong to its containers' users. What is left is in %s; "+
				"delete it with: sudo rm -rf %s\n", kept, kept)
		default:
			_, _ = fmt.Fprintf(out, "Deleted the service's folder and its data.\n")
		}
	}
	return out.String(), errors.Join(errs...)
}

// keptName makes removed, the folder removed services are kept in, and returns a path in it for
// the folder of the service called name: its name and when, such as notes-20261115T093000Z, with
// -2 and up when that is taken.
func keptName(removed, name string) (string, error) {
	if err := os.MkdirAll(removed, 0o700); err != nil {
		return "", err
	}
	base := filepath.Join(removed, name+"-"+time.Now().UTC().Format("20060102T150405Z"))
	kept := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(kept); errors.Is(err, os.ErrNotExist) {
			return kept, nil
		}
		kept = base + "-" + strconv.Itoa(i)
	}
}

// Managed reports whether name is a service Tender deployed on this machine: its folder has a
// compose file.
func (d *Runner) Managed(name string) bool {
	if !ValidName(name) {
		return false
	}
	_, err := os.Stat(filepath.Join(d.Folder(name), "compose.yaml"))
	return err == nil
}

// Act starts, stops or restarts every container of the managed service called name with docker
// compose, in its folder, as its compose file and override have them.
func (d *Runner) Act(ctx context.Context, typ, name string) error {
	if !d.Managed(name) {
		return fmt.Errorf("%s has no folder on this machine with a compose file in %s", name, d.Dir)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	config, err := dockerConfig(nil, nil)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(config) }()
	out := &tail{}
	if err := d.compose(ctx, d.Folder(name), config, typ, out, nil, "-p", name, "-f", "compose.yaml", "-f", "compose.tender.yaml", typ); err != nil {
		return fmt.Errorf("%v:\n%s", err, out.String())
	}
	return nil
}
