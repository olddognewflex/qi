package ai

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

type SessionLease struct {
	file   *os.File
	verify func() error
	remove func() error
	once   sync.Once
	err    error
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
	return &SessionLease{
		file: file,
		verify: func() error {
			held, err := file.Stat()
			if err != nil {
				return fmt.Errorf("inspect held planner session lease: %w", err)
			}
			current, err := s.root.Lstat(name)
			if err != nil {
				return fmt.Errorf("inspect planner session lease path: %w", err)
			}
			if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(held, current) {
				return fmt.Errorf("%w: planner session lease path changed", ErrInvalidSession)
			}
			return nil
		},
		remove: func() error {
			if err := s.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("delete planner session lease: %w", err)
			}
			if err := s.dir.Sync(); err != nil {
				return fmt.Errorf("sync planner session directory after lease delete: %w", err)
			}
			return nil
		},
	}, nil
}

func (l *SessionLease) Release() error {
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}

func (l *SessionLease) Complete() error {
	l.once.Do(func() {
		if err := l.verify(); err != nil {
			l.err = errors.Join(err, l.file.Close())
			return
		}
		l.err = completeSessionLease(l.file, l.remove)
	})
	return l.err
}
