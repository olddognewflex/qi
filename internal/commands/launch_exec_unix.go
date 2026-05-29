//go:build !windows

package commands

import (
	"fmt"
	"os"
	"syscall"
)

// execReplace replaces the current process image with the harness via execve(2),
// after chdir into the vault. qi disappears; the harness inherits stdio and the
// terminal directly. Only returns on error (a successful exec never returns).
func execReplace(bin string, argv []string, env []string, dir string) error {
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("launch: chdir %q: %w", dir, err)
		}
	}
	full := append([]string{bin}, argv...)
	if err := syscall.Exec(bin, full, env); err != nil {
		return fmt.Errorf("launch: exec %q: %w", bin, err)
	}
	return nil
}
