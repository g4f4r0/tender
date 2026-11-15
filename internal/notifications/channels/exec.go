package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// DefaultAdaptersDir holds the exec adapters. It is root's: only root on the hub can install one.
const DefaultAdaptersDir = "/etc/tender/adapters"

// maxOutputBytes caps what the hub keeps of an exec adapter's standard output and error, each.
const maxOutputBytes = 64 << 10

var commandName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// Exec runs a program from the adapters folder and writes the event as JSON to its standard
// input. Exit status 0 means delivered. The API names the program, never a path or arguments.
type Exec struct {
	Dir string // the adapters folder; empty means DefaultAdaptersDir
	// AnyOwner lets a program belong to someone other than root, for tests with their own Dir.
	AnyOwner bool
}

// Fields: command, the program's name in the adapters folder.
func (Exec) Fields() []Field { return []Field{{Name: "command", Required: true}} }

func (e Exec) dir() string {
	if e.Dir == "" {
		return DefaultAdaptersDir
	}
	return e.Dir
}

// Check needs command to name an executable in the adapters folder that only its owner, root,
// can change.
func (e Exec) Check(cfg Config) error {
	_, err := e.path(cfg["command"])
	return err
}

// path returns the program that name names, or a sentence saying why it can't run.
func (e Exec) path(name string) (string, error) {
	if !commandName.MatchString(name) || strings.Contains(name, "..") {
		return "", errors.New("command must be the name of a program in " + e.dir() + ", such as my-adapter: letters, digits, dots, " +
			"dashes and underscores, with no slash and no \"..\".")
	}
	if err := e.checkDir(); err != nil {
		return "", err
	}
	p := filepath.Join(e.dir(), name)
	info, err := os.Stat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("there is no %s. As root on the hub, install the program there and make it executable.", p)
	case err != nil:
		return "", fmt.Errorf("the hub can't read %s: %v.", p, errors.Unwrap(err))
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("%s is not a file. Install the program itself there.", p)
	case info.Mode().Perm()&0o022 != 0:
		return "", fmt.Errorf("%s can be changed by others than its owner. Run: sudo chmod go-w %s", p, p)
	case syscall.Access(p, 1) != nil: // X_OK, for the hub's own user
		return "", fmt.Errorf("the hub may not run %s. Run: sudo chmod 755 %s", p, p)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && !e.AnyOwner {
		return "", fmt.Errorf("%s doesn't belong to root, so others than root could change it. Run: sudo chown root:root %s", p, p)
	}
	return p, nil
}

// checkDir is an error unless only root, or with AnyOwner only its owner, can change the
// adapters folder: whoever can write to it can swap a program.
func (e Exec) checkDir() error {
	d := e.dir()
	info, err := os.Stat(d)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("there is no %s. As root on the hub, run: sudo install -d -m 755 %s", d, d)
	case err != nil:
		return fmt.Errorf("the hub can't read %s: %v.", d, errors.Unwrap(err))
	case !info.IsDir():
		return fmt.Errorf("%s is not a folder. As root on the hub, make it one: sudo install -d -m 755 %s", d, d)
	case info.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s can be changed by others than its owner. Run: sudo chmod go-w %s", d, d)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && !e.AnyOwner {
		return fmt.Errorf("%s doesn't belong to root, so others than root could change it. Run: sudo chown root:root %s", d, d)
	}
	return nil
}

// Send runs the program with the event on its standard input, at most SendTimeout, in a clean
// environment: the hub's own settings, such as its encryption key, never reach it.
func (e Exec) Send(ctx context.Context, cfg Config, ev Event) error {
	p, err := e.path(cfg["command"])
	if err != nil {
		return err
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p)
	cmd.Dir = e.dir()
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(append(body, '\n'))
	var stdout, stderr capped
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Its own process group, so a timeout stops whatever it started too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	// Whatever the program left running, such as a child that holds its output open, stops now.
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Exit 0 is delivered, even when a child kept the output open past WaitDelay.
	if err == nil || (errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success()) {
		return nil
	}
	name := cfg["command"]
	if ctx.Err() != nil {
		return fmt.Errorf("%s didn't finish within %d seconds, so the hub stopped it", name, int(SendTimeout/time.Second))
	}
	out := cut(stderr.String())
	if out == "" {
		out = cut(stdout.String())
	}
	var exit *exec.ExitError
	msg := fmt.Sprintf("%s failed: %v", name, err)
	if errors.As(err, &exit) {
		msg = fmt.Sprintf("%s exited with status %d", name, exit.ExitCode())
	}
	if out != "" {
		msg += ": " + out
	}
	return errors.New(msg)
}

// capped keeps the first maxOutputBytes written to it and drops the rest.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutputBytes - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
