package backups

import (
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	ioprioWhoProcess = 1
	ioprioClassIdle  = 3
	ioprioClassShift = 13
	schedIdle        = 5
)

// startGently starts cmd at the lowest CPU and IO priority, so a backup only uses what the
// machine's other work leaves. The daemon itself keeps its normal priority. On Linux these are per
// thread and a child takes them from the thread that forks it: so one locked thread lowers itself,
// starts cmd and is never unlocked, which ends that thread with its goroutine. The main thread
// can't end, so it is never the one: when the goroutine lands there, it holds it and starts another.
// If the machine doesn't allow lowering a priority, cmd starts as it is.
func startGently(cmd *exec.Cmd) error {
	var start func(done chan<- error)
	start = func(done chan<- error) {
		runtime.LockOSThread()
		if syscall.Gettid() == syscall.Getpid() {
			other := make(chan error, 1)
			go start(other) // can't run on this thread while it is locked here
			err := <-other
			runtime.UnlockOSThread()
			done <- err
			return
		}
		lowerThisThread() // never unlocked: the lowered thread goes away with this goroutine
		done <- cmd.Start()
	}
	done := make(chan error, 1)
	go start(done)
	return <-done
}

func lowerThisThread() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19) // who 0 is this thread
	param := struct{ priority int32 }{}
	_, _, _ = syscall.RawSyscall(syscall.SYS_SCHED_SETSCHEDULER, 0, schedIdle, uintptr(unsafe.Pointer(&param)))
	_, _, _ = syscall.RawSyscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcess, 0, ioprioClassIdle<<ioprioClassShift)
}
