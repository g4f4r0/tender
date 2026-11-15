//go:build !linux

package backups

import "os/exec"

// startGently starts cmd; lowering its priority is only done on Linux.
func startGently(cmd *exec.Cmd) error { return cmd.Start() }
