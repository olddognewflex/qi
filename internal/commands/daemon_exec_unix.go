//go:build !windows

package commands

import (
	"errors"
	"os/exec"
	"syscall"
)

// detach puts the spawned qid in its own session so it survives the qi process
// exiting and never receives the terminal's job-control signals.
func detach(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// processAlive reports whether pid is still running, and whether the answer is
// knowable at all. Signal 0 runs the existence and permission checks without
// delivering anything — EPERM means the process exists but belongs to another
// user, which still counts as alive. It never signals for real: `qi daemon
// stop` reports a stuck daemon, it does not kill one.
func processAlive(pid int) (alive, known bool) {
	if pid <= 0 {
		return false, false
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, true
	case errors.Is(err, syscall.ESRCH):
		return false, true
	default:
		return false, false
	}
}
