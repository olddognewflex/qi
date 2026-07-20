//go:build windows

package commands

import "os/exec"

// detach is a no-op on Windows: there is no setsid, and a process started
// without Wait already outlives its parent.
func detach(c *exec.Cmd) {}

// processAlive is not knowable here without opening a process handle, so
// Windows keeps the socket-only stop semantics.
func processAlive(pid int) (alive, known bool) { return false, false }
