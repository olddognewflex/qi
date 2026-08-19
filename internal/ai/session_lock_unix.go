//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package ai

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLockSession(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrSessionLeaseHeld
	}
	return err
}

func completeSessionLease(file *os.File, remove func() error) error {
	return errors.Join(remove(), file.Close())
}
