package ai

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

type SessionLease struct {
	file *os.File
	once sync.Once
	err  error
}

func (s *SessionStore) AcquireLease(id SessionID) (*SessionLease, error) {
	name := id.String() + ".lock"
	if err := s.rejectExistingNonRegular(name); err != nil {
		return nil, err
	}
	file, err := s.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		if err := s.requireRegular(name); err != nil {
			return nil, err
		}
		file, err = s.root.OpenFile(name, os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("open planner session lease: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect planner session lease: %w", err), file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("%w: lease file is not regular", ErrInvalidSession), file.Close())
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("repair planner session lease mode: %w", err), file.Close())
	}
	if err := tryLockSession(file); err != nil {
		closeErr := file.Close()
		if errors.Is(err, ErrSessionLeaseHeld) {
			return nil, errors.Join(err, closeErr)
		}
		return nil, errors.Join(fmt.Errorf("lock planner session lease: %w", err), closeErr)
	}
	return &SessionLease{file: file}, nil
}

func (l *SessionLease) Release() error {
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}
