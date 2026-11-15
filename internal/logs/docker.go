package logs

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/g4f4r0/tender/internal/logs/sinks"
)

// dockerSource follows the logs of the running Docker containers through the Engine API. It
// looks for new and re-created containers every dockerPoll.
type dockerSource struct {
	socket string
	filter ServicesSource
	log    *slog.Logger
	poll   time.Duration
}

// dockerPoll is how often the daemon looks for containers to follow.
const dockerPoll = 5 * time.Second

func dockerClient(socket string) *http.Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	return &http.Client{Transport: &http.Transport{DialContext: dial, DisableCompression: true}}
}

type container struct {
	ID    string `json:"Id"`
	Names []string
}

func (c container) name() string {
	if len(c.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

func (d dockerSource) getJSON(ctx context.Context, hc *http.Client, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Docker answered %d to %s", resp.StatusCode, path)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(v)
}

func (d dockerSource) run(ctx context.Context, start cursors, out chan<- item) {
	hc := dockerClient(d.socket)
	var mu sync.Mutex
	last := map[string]time.Time{} // by container name: the time of its last record sent
	for name, t := range start.Services {
		last[name] = t
	}
	active := map[string]bool{} // container ids being followed
	var wg sync.WaitGroup
	defer wg.Wait()
	failing := false
	for {
		var list []container
		listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := d.getJSON(listCtx, hc, "/containers/json", &list)
		cancel()
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil && !failing:
			d.log.Warn("can't list the Docker containers to send their logs; trying again", "error", err)
			failing = true
		case err == nil && failing:
			d.log.Info("listing the Docker containers again, to send their logs")
			failing = false
		}
		for _, c := range list {
			name := c.name()
			if name == "" || (len(d.filter.Names) > 0 && !slices.Contains(d.filter.Names, name)) {
				continue
			}
			mu.Lock()
			following := active[c.ID]
			active[c.ID] = true
			if _, seen := last[name]; !seen {
				last[name] = start.Since
			}
			since := last[name]
			mu.Unlock()
			if following {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := d.follow(ctx, hc, c.ID, name, since, func(t time.Time) bool {
					mu.Lock()
					defer mu.Unlock()
					if !t.After(last[name]) {
						return false
					}
					last[name] = t
					return true
				}, out)
				if err != nil && ctx.Err() == nil {
					d.log.Warn("stopped following a container's logs", "service", name, "error", err)
				}
				mu.Lock()
				delete(active, c.ID)
				mu.Unlock()
			}()
		}
		if !wait(ctx, d.poll) {
			return
		}
	}
}

// follow streams one container's logs from after since until the container stops or ctx ends.
// keep says whether a line at its time is new; it is false for a line already sent.
func (d dockerSource) follow(ctx context.Context, hc *http.Client, id, name string, since time.Time, keep func(time.Time) bool, out chan<- item) error {
	var info struct {
		Config struct{ Tty bool }
	}
	if err := d.getJSON(ctx, hc, "/containers/"+url.PathEscape(id)+"/json", &info); err != nil {
		return err
	}
	q := url.Values{"follow": {"1"}, "stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}}
	if !since.IsZero() {
		q.Set("since", strconv.FormatInt(since.Unix(), 10)+"."+fmt.Sprintf("%09d", since.Nanosecond()))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+url.PathEscape(id)+"/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Docker answered %d", resp.StatusCode)
	}
	emit := func(line string, stderr bool) bool {
		ts, text, ok := strings.Cut(line, " ")
		t, err := time.Parse(time.RFC3339Nano, ts)
		if !ok || err != nil || !keep(t) {
			return true
		}
		priority := "info"
		if stderr {
			priority = "err"
		}
		return send(ctx, out, item{rec: sinks.Record{Time: t.UTC(), Source: sinks.SourceService, Service: name, Priority: priority, Message: message(text)}})
	}
	if info.Config.Tty { // a TTY's output comes raw, without frames
		return lines(resp.Body, func(l string) bool { return emit(l, false) })
	}
	return frames(resp.Body, emit)
}

// lines calls fn with each line read from r, until fn returns false or r ends.
func lines(r io.Reader, fn func(string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		if !fn(sc.Text()) {
			return nil
		}
	}
	return sc.Err()
}

// frames reads Docker's multiplexed stream: each frame is an 8-byte header, whose first byte is 1
// for stdout and 2 for stderr and whose last four are the payload's size, big-endian, then the
// payload. It calls fn with each whole line, until fn returns false or r ends.
func frames(r io.Reader, fn func(line string, stderr bool) bool) error {
	br := bufio.NewReader(r)
	partial := map[byte]string{}
	var header [8]byte
	for {
		if _, err := io.ReadFull(br, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		size := binary.BigEndian.Uint32(header[4:])
		if size > 16<<20 {
			return fmt.Errorf("a log frame of %d bytes is too big", size)
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(br, payload); err != nil {
			return err
		}
		stream := header[0]
		text := partial[stream] + string(payload)
		for {
			line, rest, found := strings.Cut(text, "\n")
			if !found {
				break
			}
			if !fn(line, stream == 2) {
				return nil
			}
			text = rest
		}
		if len(text) > maxMessageBytes*2 { // a line with no end in sight goes as it is
			if !fn(text, stream == 2) {
				return nil
			}
			text = ""
		}
		partial[stream] = text
	}
}
