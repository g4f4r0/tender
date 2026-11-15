package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DefaultStateDir is the daemon's state folder, which its service owns.
const DefaultStateDir = "/var/lib/tender/daemon"

// Files in the daemon's state folder that the daemon and the update job share. The job runs as root
// in its own transient unit, outside the daemon's sandbox, and outlives the daemon's restart.
const (
	// ReportedVersionFile holds the version the daemon last reported to the hub on. The update job
	// waits for it to name the new version before it calls an update healthy.
	ReportedVersionFile = "reported-version"
	// UpdateResultFile says how the last update job ended. The daemon reports it to the hub as the
	// result of the update action, then deletes it.
	UpdateResultFile = "update-result.json"
	// busyFile is locked shared while the daemon backs up or runs an action, and exclusively while
	// the update job swaps the binary and restarts the services.
	busyFile = "busy.lock"
)

// UpdateResult is how an update job ended, for the daemon to report.
type UpdateResult struct {
	Version string `json:"version"` // the version the job installed or tried to
	Status  string `json:"status"`  // completed or failed
	Output  string `json:"output"`  // what happened, in sentences
}

// WriteUpdateResult saves r in the state folder dir, whole or not at all.
func WriteUpdateResult(dir string, r UpdateResult) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, UpdateResultFile), b)
}

// ReadUpdateResult returns the saved result of an update job, and false when there is none.
func ReadUpdateResult(dir string) (UpdateResult, bool, error) {
	var r UpdateResult
	b, err := os.ReadFile(filepath.Join(dir, UpdateResultFile))
	if errors.Is(err, os.ErrNotExist) {
		return r, false, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	return r, err == nil, err
}

// RemoveUpdateResult deletes the saved result once the daemon has it.
func RemoveUpdateResult(dir string) error {
	err := os.Remove(filepath.Join(dir, UpdateResultFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// WriteReportedVersion records that the daemon reported to the hub on version.
func WriteReportedVersion(dir, version string) error {
	return writeAtomic(filepath.Join(dir, ReportedVersionFile), []byte(version+"\n"))
}

// ReadReportedVersion returns the version the daemon last reported on, or "".
func ReadReportedVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ReportedVersionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeAtomic writes path through a temporary file in its folder, readable only by its owner.
func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

// ErrUpdating means the update job holds the state folder: it is swapping Tender's binary and
// restarting its services, so a backup or an action waits.
var ErrUpdating = errors.New("Tender is updating on this machine")

// HoldBusy marks the daemon busy, with a backup or an action, until release is called, so the
// update job waits for it. It returns ErrUpdating while the update job holds the folder. The lock is
// the kernel's, on a file in dir, so it ends with the process too.
func HoldBusy(dir string) (release func(), err error) {
	f, err := openBusy(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrUpdating
		}
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

// WaitIdle waits until the daemon is idle, at most timeout, or with a negative timeout for as long as
// it takes, and then keeps it from starting a backup or an action until release is called.
func WaitIdle(ctx context.Context, dir string, timeout, every time.Duration) (release func(), err error) {
	f, err := openBusy(dir)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	if timeout < 0 {
		deadline = time.Now().Add(100 * 365 * 24 * time.Hour)
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("a backup or an action was still running after %s", timeout)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(min(every, time.Until(deadline))):
		}
	}
}

func openBusy(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, busyFile), os.O_RDWR|os.O_CREATE, 0o600)
}

// Test-only settings. Release builds read them too, so they only ever point Tender at a folder or a
// program the person running it chose; an update still needs a release signed by an embedded key.
const (
	// EnvTestRoot puts every system path the daemon and the installer use under this folder.
	EnvTestRoot = "TENDER_TEST_ROOT"
	// EnvTestSystemdRun replaces systemd-run, which starts the update and upgrade jobs.
	EnvTestSystemdRun = "TENDER_TEST_SYSTEMD_RUN"
)

// Root is the folder system paths are under: "/", or EnvTestRoot in tests.
func Root() string {
	if r := os.Getenv(EnvTestRoot); r != "" {
		return r
	}
	return "/"
}

// SystemdRun is the program that starts a transient unit: systemd-run, or EnvTestSystemdRun in tests.
func SystemdRun() string {
	if p := os.Getenv(EnvTestSystemdRun); p != "" {
		return p
	}
	return "systemd-run"
}
