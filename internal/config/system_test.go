package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateResultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := ReadUpdateResult(dir); ok || err != nil {
		t.Fatalf("no result yet: ok %v, err %v", ok, err)
	}
	want := UpdateResult{Version: "0.3.1", Status: "failed", Output: "It rolled back."}
	if err := WriteUpdateResult(dir, want); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, UpdateResultFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the result is 0600: %v %v", fi.Mode(), err)
	}
	got, ok, err := ReadUpdateResult(dir)
	if !ok || err != nil || got != want {
		t.Fatalf("read %+v %v %v", got, ok, err)
	}
	if err := RemoveUpdateResult(dir); err != nil {
		t.Fatal(err)
	}
	if err := RemoveUpdateResult(dir); err != nil {
		t.Fatalf("removing a missing result is no error: %v", err)
	}
}

func TestReportedVersion(t *testing.T) {
	dir := t.TempDir()
	if v := ReadReportedVersion(dir); v != "" {
		t.Fatalf("no marker yet, got %q", v)
	}
	if err := WriteReportedVersion(dir, "0.3.1"); err != nil {
		t.Fatal(err)
	}
	if v := ReadReportedVersion(dir); v != "0.3.1" {
		t.Fatalf("got %q", v)
	}
}

// The update job waits for a running backup, and a backup can't start while the job holds the folder.
func TestBusyAndIdle(t *testing.T) {
	dir := t.TempDir()
	release, err := HoldBusy(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HoldBusy(dir)
	if err != nil {
		t.Fatalf("two busy holders share the lock: %v", err)
	}
	second()
	if _, err := WaitIdle(context.Background(), dir, 50*time.Millisecond, 10*time.Millisecond); err == nil {
		t.Fatal("the job got the folder while a backup ran")
	}
	go func() { time.Sleep(50 * time.Millisecond); release() }()
	idle, err := WaitIdle(context.Background(), dir, 5*time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("the job waits for the backup to end: %v", err)
	}
	if _, err := HoldBusy(dir); !errors.Is(err, ErrUpdating) {
		t.Fatalf("a backup during the update: %v", err)
	}
	idle()
	again, err := HoldBusy(dir)
	if err != nil {
		t.Fatalf("after the update a backup may run: %v", err)
	}
	again()
}
