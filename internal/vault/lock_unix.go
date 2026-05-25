//go:build unix

package vault

import (
	"fmt"
	"os"
	"syscall"
)

// withFileLock runs fn while holding an exclusive advisory lock (flock) tied to
// path. It serializes vault mutators across processes so a read-modify-write
// critical section cannot interleave and lose an update — in particular it
// prevents AppendTask (which appends to the current inode) from racing
// UpdateTaskLine (which atomically renames a new inode into place).
//
// The lock is advisory: it only excludes other callers that also use
// withFileLock, which every qi vault mutator does. External editors (Obsidian,
// sync clients) do not participate; the per-write content guard and atomic
// rename remain the defense against their edits.
func withFileLock(path string, fn func() error) error {
	lockPath := lockPathFor(path)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	return fn()
}
