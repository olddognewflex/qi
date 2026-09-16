//go:build aix

package ai

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func tryLockSession(file *os.File) error {
	lock := unix.Flock_t{
		Type:   unix.F_WRLCK,
		Whence: io.SeekStart,
		Len:    1,
	}
	err := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
	if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EAGAIN) {
		return ErrSessionLeaseHeld
	}
	return err
}

func completeSessionLease(file *os.File, remove func() error) error {
	return errors.Join(remove(), file.Close())
}
