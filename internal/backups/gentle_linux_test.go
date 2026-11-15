package backups

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// statFields returns the fields of /proc/<path>/stat after the command name.
func statFields(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("/proc", path, "stat"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+2:]))
}

// restic runs at the lowest CPU and IO priority, and the daemon that starts it doesn't: none of
// its threads is lowered, so its pings and reports aren't starved while a backup runs.
func TestResticRunsGently(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := startGently(cmd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := cmd.Process.Pid
	f := statFields(t, strconv.Itoa(pid))
	// After the name, the fields start at 3 (state): nice is field 19, policy field 41.
	if nice, policy := f[19-3], f[41-3]; nice != "19" || policy != "5" {
		t.Errorf("restic has nice %s and policy %s; want 19 and 5 (SCHED_IDLE)", nice, policy)
	}
	prio, _, errno := syscall.Syscall(syscall.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(pid), 0)
	if errno != 0 || prio>>ioprioClassShift != ioprioClassIdle {
		t.Errorf("restic's IO priority is %#x (%v); want the idle class", prio, errno)
	}

	time.Sleep(100 * time.Millisecond) // the thread that started it has exited
	tasks, _ := filepath.Glob("/proc/self/task/*")
	for _, task := range tasks {
		f := statFields(t, strings.TrimPrefix(task, "/proc/"))
		if f[19-3] != "0" || f[41-3] != "0" {
			t.Errorf("thread %s of the daemon has nice %s and policy %s", filepath.Base(task), f[19-3], f[41-3])
		}
		tid, _ := strconv.Atoi(filepath.Base(task))
		if prio, _, _ := syscall.Syscall(syscall.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(tid), 0); prio>>ioprioClassShift == ioprioClassIdle {
			t.Errorf("thread %d of the daemon has idle IO", tid)
		}
	}
}
