package backups

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	maxErrorBytes = 1000 // cap on the error text stored with a run
	maxErrorItems = 20   // unreadable files named in a partial backup's error
)

// Outcome is how one restic command ended. Status is StatusCompleted or StatusFailed and comes
// from the exit code alone. A partial backup is completed with Error set.
type Outcome struct {
	Status   string
	Error    string
	ResticID string // the snapshot a backup made
}

// Restic runs the restic binary with fixed argument lists, never through a shell.
type Restic struct {
	Bin          string // default "restic" on PATH
	Repository   string
	PasswordFile string
	Host         string   // the machine name; scopes every command to this machine's snapshots
	Env          []string // extra KEY=VALUE for the child only, such as the S3 keys
	CacheDir     string
	StateDir     string // holds the exclude file and TMPDIR
}

// Backup runs restic backup, creating the repository first if restic says it doesn't exist.
func (r *Restic) Backup(ctx context.Context, paths, exclude []string) Outcome {
	file := filepath.Join(r.StateDir, "exclude")
	// Never back up restic's own cache and temp files.
	exclude = append(slices.Clone(exclude), r.CacheDir, filepath.Join(r.StateDir, "tmp"))
	if err := os.MkdirAll(r.StateDir, 0o700); err != nil {
		return failed(err)
	}
	if err := os.WriteFile(file, []byte(strings.Join(exclude, "\n")+"\n"), 0o600); err != nil {
		return failed(fmt.Errorf("write exclude file: %w", err))
	}
	args := append([]string{
		"backup", "--json", "-q", "--host", r.Host, "--tag", "tender", "--exclude-caches",
		"--one-file-system", "--exclude-file", file, "--retry-lock", "5m", "--",
	}, paths...)

	for initialized := false; ; initialized = true {
		var stdout bytes.Buffer
		p, code, err := r.run(ctx, &stdout, args...)
		if err != nil {
			return failed(err)
		}
		if code == 10 && !initialized { // repository doesn't exist yet
			if o := r.init(ctx); o.Status != StatusCompleted {
				return o
			}
			continue
		}
		o := r.outcome(code, p, true)
		if o.Status == StatusCompleted {
			o.ResticID = summarySnapshotID(stdout.Bytes())
		}
		return o
	}
}

func (r *Restic) init(ctx context.Context) Outcome {
	p, code, err := r.run(ctx, io.Discard, "init")
	if err != nil {
		return failed(err)
	}
	return r.outcome(code, p, false)
}

// Cleanup forgets this machine's snapshots older than retentionDays and prunes.
func (r *Restic) Cleanup(ctx context.Context, retentionDays int) Outcome {
	if retentionDays < 1 {
		return failed(errors.New("retention_days must be at least 1"))
	}
	p, code, err := r.run(ctx, io.Discard, "forget", "--json", "--host", r.Host, "--tag", "tender",
		"--keep-within", strconv.Itoa(retentionDays)+"d", "--prune", "--retry-lock", "5m")
	if err != nil {
		return failed(err)
	}
	return r.outcome(code, p, false)
}

// Check reads a 5% sample of the repository's data. A plain check misses pack corruption.
func (r *Restic) Check(ctx context.Context) Outcome {
	p, code, err := r.run(ctx, io.Discard, "check", "--json", "--retry-lock", "5m", "--read-data-subset=5%")
	if err != nil {
		return failed(err)
	}
	return r.outcome(code, p, false)
}

// Snapshots lists this machine's tender snapshots.
func (r *Restic) Snapshots(ctx context.Context) ([]SnapshotInput, error) {
	var stdout bytes.Buffer
	p, code, err := r.run(ctx, &stdout, "snapshots", "--json", "--host", r.Host, "--tag", "tender")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, errors.New(r.outcome(code, p, false).Error)
	}
	var raw []struct {
		ID      string    `json:"id"`
		Time    time.Time `json:"time"`
		Paths   []string  `json:"paths"`
		Summary *struct {
			TotalBytesProcessed int64 `json:"total_bytes_processed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, fmt.Errorf("parse restic snapshots: %w", err)
	}
	list := make([]SnapshotInput, 0, len(raw))
	for _, s := range raw {
		in := SnapshotInput{ResticID: s.ID, CreatedAt: s.Time.UTC(), Paths: s.Paths}
		if in.Paths == nil {
			in.Paths = []string{}
		}
		if s.Summary != nil { // snapshots from old restic versions have no summary
			size := s.Summary.TotalBytesProcessed
			in.SizeBytes = &size
		}
		list = append(list, in)
	}
	return list, nil
}

func failed(err error) Outcome {
	return Outcome{Status: StatusFailed, Error: truncate(err.Error())}
}

// outcome maps an exit code to a status: 0 completed, 3 completed with an error when partialOK
// (a backup that saved a snapshot but couldn't read some files), anything else failed. That
// includes 11 (locked after --retry-lock), 12 (wrong password), 130 (cancelled) and 1.
func (r *Restic) outcome(code int, p *stderrParser, partialOK bool) Outcome {
	switch {
	case code == 0:
		return Outcome{Status: StatusCompleted}
	case code == 3 && partialOK:
		return Outcome{Status: StatusCompleted, Error: truncate(r.scrub(p.partialMessage()))}
	}
	// Scrub before truncating: a secret cut in half by the limit would no longer match.
	return Outcome{Status: StatusFailed, Error: truncate(r.scrub(p.failureMessage(code)))}
}

// scrub hides the child's secret values in text that came from restic.
func (r *Restic) scrub(s string) string {
	for _, kv := range r.Env {
		if _, v, ok := strings.Cut(kv, "="); ok && len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// run starts restic and returns its exit code and parsed stderr. A non-nil error means restic
// couldn't be started. SIGINT cancels it so it releases its lock; SIGKILL follows after 30 s.
func (r *Restic) run(ctx context.Context, stdout io.Writer, args ...string) (*stderrParser, int, error) {
	tmp := filepath.Join(r.StateDir, "tmp")
	for _, dir := range []string{tmp, r.CacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, 0, err
		}
	}
	bin := r.Bin
	if bin == "" {
		bin = "restic"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + r.StateDir,
		"TMPDIR=" + tmp,
		"RESTIC_REPOSITORY=" + r.Repository,
		"RESTIC_PASSWORD_FILE=" + r.PasswordFile,
		"RESTIC_HOST=" + r.Host,
		"RESTIC_CACHE_DIR=" + r.CacheDir,
	}, r.Env...)
	cmd.Stdout = stdout
	p := &stderrParser{}
	cmd.Stderr = p
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGINT) }
	cmd.WaitDelay = 30 * time.Second

	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return p, 0, nil
	case errors.As(err, &exit):
		return p, exit.ExitCode(), nil
	}
	return p, 0, fmt.Errorf("run restic %s: %w", args[0], err)
}

// summarySnapshotID finds the snapshot id in restic backup's JSON lines.
func summarySnapshotID(stdout []byte) string {
	id := ""
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		var m struct {
			Type       string `json:"message_type"`
			SnapshotID string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &m) == nil && m.Type == "summary" && m.SnapshotID != "" {
			id = m.SnapshotID
		}
	}
	return id
}

// stderrParser reads restic's stderr line by line and keeps only what an error message needs:
// the last exit_error, the unreadable files (a backup can emit a million) and the last plain lines.
type stderrParser struct {
	buf       []byte
	exitError string
	items     []string
	seen      map[string]bool
	tail      []string
}

func (p *stderrParser) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		p.line(string(p.buf[:i]))
		p.buf = p.buf[i+1:]
	}
	if len(p.buf) > 1<<20 { // a line this long is a stack trace; drop it
		p.buf = nil
	}
	return len(b), nil
}

func (p *stderrParser) line(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	var m struct {
		Type    string `json:"message_type"`
		Message string `json:"message"`
		Item    string `json:"item"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if !strings.HasPrefix(s, "{") || json.Unmarshal([]byte(s), &m) != nil {
		p.tail = append(p.tail, s)
		if len(p.tail) > 5 {
			p.tail = p.tail[1:]
		}
		return
	}
	switch m.Type {
	case "exit_error":
		p.exitError = m.Message
	case "error":
		if m.Item != "" && len(p.seen) < 10000 && !p.seen[m.Item] {
			if p.seen == nil {
				p.seen = map[string]bool{}
			}
			p.seen[m.Item] = true
			if len(p.items) < maxErrorItems {
				p.items = append(p.items, m.Item)
			}
		}
	}
}

func (p *stderrParser) partialMessage() string {
	if len(p.seen) == 0 {
		return p.failureMessage(3)
	}
	msg := fmt.Sprintf("%d files could not be read: %s", len(p.seen), strings.Join(p.items, ", "))
	if len(p.seen) > len(p.items) {
		msg += fmt.Sprintf(" and %d more", len(p.seen)-len(p.items))
	}
	return msg
}

func (p *stderrParser) failureMessage(code int) string {
	msg := p.exitError
	for _, marker := range []string{"\ngoroutine ", "\ngithub.com/", "\nmain."} { // cut a stack trace
		if i := strings.Index(msg, marker); i >= 0 {
			msg = msg[:i]
		}
	}
	if msg == "" {
		msg = strings.Join(p.tail, " | ")
	}
	if msg == "" {
		msg = "restic exited with code " + strconv.Itoa(code)
	}
	return strings.TrimSpace(msg)
}

func truncate(s string) string {
	if len(s) <= maxErrorBytes {
		return s
	}
	s = s[:maxErrorBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "..."
}
