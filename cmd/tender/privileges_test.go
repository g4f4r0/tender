package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDropTarget(t *testing.T) {
	dir := t.TempDir() // owned by whoever runs the tests, never root in CI
	me, mygid := os.Getuid(), os.Getgid()
	if me == 0 {
		t.Skip("the test folder is root-owned when the tests run as root")
	}

	uid, gid, ok := dropTarget(0, dir)
	if !ok || uid != me || gid != mygid {
		t.Errorf("root with a folder owned by %d:%d = %d:%d %v", me, mygid, uid, gid, ok)
	}
	if _, _, ok := dropTarget(me, dir); ok {
		t.Error("a user who is not root must not switch")
	}
	if _, _, ok := dropTarget(0, filepath.Join(dir, "missing")); ok {
		t.Error("a missing folder must not switch")
	}
	if _, _, ok := dropTarget(0, "/"); ok {
		t.Error("a root-owned folder must not switch")
	}
}
