//go:build windows

package commands

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// execReplace has no execve(2) equivalent on Windows, so it runs the harness as
// a child inheriting stdio and exits with the harness's status code once it
// returns — the closest behavior to replacing the process.
func execReplace(bin string, argv []string, env []string, dir string) error {
	c := exec.Command(bin, argv...)
	c.Dir = dir
	c.Env = env
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("launch: running %q: %w", bin, err)
	}
	os.Exit(0)
	return nil
}
