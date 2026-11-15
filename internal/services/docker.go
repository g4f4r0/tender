package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// DefaultSocket is where Docker's Engine API listens.
const DefaultSocket = "/var/run/docker.sock"

const composeProject = "com.docker.compose.project"

// Docker talks to the Docker Engine API over its Unix socket, with unversioned paths, so it uses
// the version the engine runs.
type Docker struct {
	http *http.Client
}

// NewDocker returns a client for the Engine API at socket.
func NewDocker(socket string) *Docker {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	return &Docker{http: &http.Client{Transport: &http.Transport{DialContext: dial, DisableCompression: true}}}
}

// errNoDocker means there is no socket: Docker isn't installed.
var errNoDocker = errors.New("this machine has no Docker")

// errUnreachable means the socket is there but refuses connections or doesn't answer: the engine
// is stopped or stuck.
var errUnreachable = errors.New("Docker is not answering on this machine")

// errDenied means the socket is there but the daemon may not use it.
var errDenied = errors.New("Tender may not use Docker on this machine")

// classify turns a failure to reach the socket into errNoDocker, errUnreachable or errDenied,
// keeping the cause. A timeout isn't classified here: only Ping judges that Docker doesn't answer,
// so a slow request isn't taken for an outage.
func classify(err error) error {
	switch {
	case errors.Is(err, syscall.ENOENT):
		return fmt.Errorf("%w: %w", errNoDocker, err)
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("%w: %w", errUnreachable, err)
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return fmt.Errorf("%w: %w", errDenied, err)
	}
	return err
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// apiError is an answer from Docker that isn't a success.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string { return e.message }

// do sends one request and returns the answer's body, which the caller closes. Statuses in ok
// are successes too, such as 304 for starting a running container.
func (d *Docker) do(ctx context.Context, method, path string, ok ...int) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, classify(err)
	}
	if resp.StatusCode/100 == 2 || slices.Contains(ok, resp.StatusCode) {
		return resp.Body, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct{ Message string }
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(raw, &body) != nil || body.Message == "" {
		body.Message = strings.TrimSpace(string(raw))
	}
	if body.Message == "" {
		body.Message = http.StatusText(resp.StatusCode)
	}
	return nil, &apiError{status: resp.StatusCode, message: "Docker answered " + strconv.Itoa(resp.StatusCode) + ": " + body.Message}
}

// pingTimeout is how long Docker has to answer GET /_ping before it counts as not answering.
var pingTimeout = 5 * time.Second

// Ping asks whether Docker answers, with its own short timeout. Any HTTP answer counts. It returns
// errNoDocker without a socket, errDenied when the daemon may not use it, and errUnreachable when
// it refuses connections or doesn't answer within pingTimeout; ctx ending first is none of them.
func (d *Docker) Ping(ctx context.Context) error {
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	body, err := d.do(pctx, http.MethodGet, "/_ping")
	var api *apiError
	switch {
	case err == nil:
		return body.Close()
	case errors.As(err, &api):
		return nil
	case ctx.Err() == nil && (errors.Is(err, context.DeadlineExceeded) || isTimeout(err)):
		return fmt.Errorf("%w: %w", errUnreachable, err)
	}
	return err
}

func (d *Docker) getJSON(ctx context.Context, path string, v any) error {
	body, err := d.do(ctx, http.MethodGet, path)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	if err := json.NewDecoder(body).Decode(v); err != nil {
		return classify(fmt.Errorf("read Docker's answer to %s: %w", path, err))
	}
	return nil
}

type listed struct {
	ID     string `json:"Id"`
	Names  []string
	Image  string
	State  string
	Labels map[string]string
}

type inspected struct {
	ID           string `json:"Id"`
	Name         string
	RestartCount int64
	State        struct {
		Status    string
		ExitCode  int64
		StartedAt string
		Health    *struct{ Status string }
	}
	Config struct {
		Tty bool
	}
	HostConfig struct {
		RestartPolicy struct{ Name string }
		Memory        int64 // the memory limit in bytes, 0 for none
	}
}

// Containers lists every container, running or not, with the health, restart count and start
// time from inspecting each. A container that is removed meanwhile is left out, and so is one in a
// state Tender doesn't know.
func (d *Docker) Containers(ctx context.Context) ([]ServiceInput, error) {
	var list []listed
	if err := d.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return nil, err
	}
	out := []ServiceInput{}
	for _, c := range list {
		var in inspected
		if err := d.getJSON(ctx, "/containers/"+url.PathEscape(c.ID)+"/json", &in); err != nil {
			var api *apiError
			if errors.As(err, &api) && api.status == http.StatusNotFound {
				continue
			}
			return nil, err
		}
		state := in.State.Status
		if state == "" {
			state = c.State
		}
		if !states[state] {
			continue
		}
		name := strings.TrimPrefix(in.Name, "/")
		if name == "" && len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		sv := ServiceInput{Name: name, ContainerID: c.ID, Image: c.Image, State: state, RestartCount: in.RestartCount,
			RestartPolicy: in.HostConfig.RestartPolicy.Name}
		if !policies[sv.RestartPolicy] {
			sv.RestartPolicy = PolicyNo
		}
		if p := c.Labels[composeProject]; p != "" {
			sv.Project = &p
		}
		if h := in.State.Health; h != nil && healths[h.Status] {
			health := h.Status
			sv.Health = &health
		}
		if state == StateExited || state == StateDead {
			code := in.State.ExitCode
			sv.ExitCode = &code
		}
		if state == StateRunning && in.HostConfig.Memory > 0 {
			limit := in.HostConfig.Memory
			sv.MemoryLimitBytes = &limit
		}
		if t, err := time.Parse(time.RFC3339Nano, in.State.StartedAt); err == nil && t.Year() > 1 {
			t = t.UTC().Truncate(time.Second)
			sv.StartedAt = &t
		}
		out = append(out, sv)
	}
	return out, nil
}

// sample is one reading of a container's usage. cpu and system are Docker's cumulative CPU times,
// in nanoseconds, of the container and of the whole machine.
type sample struct {
	memory      int64
	cpu, system uint64
	cpus        uint32
}

// Stats reads a container's memory use and CPU times once, without Docker's second sample.
// Memory is what the container uses less the page cache it can drop, as "docker stats" shows it.
func (d *Docker) Stats(ctx context.Context, id string) (sample, error) {
	var st struct {
		MemoryStats struct {
			Usage uint64            `json:"usage"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
		CPUStats struct {
			CPUUsage struct {
				TotalUsage  uint64   `json:"total_usage"`
				PercpuUsage []uint64 `json:"percpu_usage"`
			} `json:"cpu_usage"`
			SystemCPUUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs     uint32 `json:"online_cpus"`
		} `json:"cpu_stats"`
	}
	if err := d.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/stats?stream=false&one-shot=true", &st); err != nil {
		return sample{}, err
	}
	m := st.MemoryStats
	if m.Usage == 0 {
		return sample{}, errors.New("Docker sent no memory use")
	}
	cache := m.Stats["inactive_file"]                // cgroup v2
	if v, ok := m.Stats["total_inactive_file"]; ok { // cgroup v1
		cache = v
	}
	used := int64(m.Usage)
	if cache < m.Usage {
		used = int64(m.Usage - cache)
	}
	cpus := st.CPUStats.OnlineCPUs
	if cpus == 0 {
		cpus = uint32(len(st.CPUStats.CPUUsage.PercpuUsage))
	}
	return sample{memory: used, cpu: st.CPUStats.CPUUsage.TotalUsage, system: st.CPUStats.SystemCPUUsage, cpus: cpus}, nil
}

// cpuPercent is the container's share of the machine's CPU between two samples, where 100 is one
// whole CPU, as "docker stats" shows it. It is false when the samples can't tell.
func cpuPercent(prev, now sample) (float64, bool) {
	if now.system <= prev.system || now.cpu < prev.cpu || now.cpus == 0 {
		return 0, false
	}
	p := float64(now.cpu-prev.cpu) / float64(now.system-prev.system) * float64(now.cpus) * 100
	return math.Round(p*10) / 10, true
}

// Act starts, stops or restarts a container. A stop waits as long as the container's own stop
// timeout. Starting a running container or stopping a stopped one is not an error.
func (d *Docker) Act(ctx context.Context, typ, name string) error {
	body, err := d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/"+typ, http.StatusNotModified)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, body)
	return body.Close()
}

// Logs returns a container's last lines, stdout and stderr together, with Docker's timestamps:
// at most MaxLines lines and MaxOutputBytes, the newest kept, and only from since ago when since
// isn't 0.
func (d *Docker) Logs(ctx context.Context, name string, lines int, since time.Duration) (string, error) {
	lines = max(1, min(lines, MaxLines))
	var in inspected
	if err := d.getJSON(ctx, "/containers/"+url.PathEscape(name)+"/json", &in); err != nil {
		return "", err
	}
	body, err := d.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/logs?stdout=1&stderr=1&timestamps=1&tail="+strconv.Itoa(lines)+sinceParam(since))
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	var t tail
	if in.Config.Tty { // a TTY's output comes raw
		_, err = io.Copy(&t, body)
	} else {
		err = demux(body, &t)
	}
	if err != nil {
		return "", fmt.Errorf("read the logs from Docker: %w", err)
	}
	return t.text(lines), nil
}

// sinceParam is Docker's since parameter, in Unix seconds, for logs from since ago.
func sinceParam(since time.Duration) string {
	if since <= 0 {
		return ""
	}
	return "&since=" + strconv.FormatInt(time.Now().Add(-since).Unix(), 10)
}

// demux copies the payloads of Docker's multiplexed stream to w: each frame is an 8-byte header,
// whose last four bytes are the payload's size, big-endian, then the payload.
func demux(r io.Reader, w io.Writer) error {
	var header [8]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if _, err := io.CopyN(w, r, size); err != nil {
			return err
		}
	}
}

// tail keeps the last MaxOutputBytes written to it, and a little more, so its memory stays small
// however long the logs are.
type tail struct {
	buf []byte
	cut bool // bytes were dropped from the front
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*MaxOutputBytes {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-MaxOutputBytes:]...)
		t.cut = true
	}
	return len(p), nil
}

// text is the last lines lines, at most MaxOutputBytes, as valid UTF-8. A line cut by the byte
// limit is left out.
func (t *tail) text(lines int) string {
	b := t.buf
	if len(b) > MaxOutputBytes {
		b, t.cut = b[len(b)-MaxOutputBytes:], true
	}
	if t.cut {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		} else {
			b = nil
		}
	}
	all := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	s := strings.Join(all, "\n")
	if s != "" {
		s += "\n"
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return s
}
