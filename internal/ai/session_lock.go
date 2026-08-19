package ai

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

type SessionLease struct {
	file         *os.File
	verify       func() error
	remove       func() error
	localRelease func()
	once         sync.Once
	err          error
}

var processSessionLeases = struct {
	sync.Mutex
	next  uint64
	files map[uint64]os.FileInfo
}{files: make(map[uint64]os.FileInfo)}

func (s *SessionStore) AcquireLease(id SessionID) (*SessionLease, error) {
	name := id.String() + ".lock"
	var expected os.FileInfo
	var localRelease func()
	file, err := s.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		expected, err = s.root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if expected.Mode()&os.ModeSymlink != 0 || !expected.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: lease file is not regular", ErrInvalidSession)
		}
		localRelease, err = reserveProcessSessionLease(expected)
		if err != nil {
			return nil, err
		}
		file, err = s.root.OpenFile(name, os.O_RDWR, 0o600)
	}
	if err != nil {
		if localRelease != nil {
			localRelease()
		}
		return nil, fmt.Errorf("open planner session lease: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		if localRelease != nil {
			localRelease()
		}
		return nil, errors.Join(fmt.Errorf("inspect planner session lease: %w", err), file.Close())
	}
	if !info.Mode().IsRegular() || (expected != nil && !os.SameFile(expected, info)) {
		if localRelease != nil {
			localRelease()
		}
		return nil, errors.Join(fmt.Errorf("%w: lease file is not regular", ErrInvalidSession), file.Close())
	}
	if err := file.Chmod(0o600); err != nil {
		if localRelease != nil {
			localRelease()
		}
		return nil, errors.Join(fmt.Errorf("repair planner session lease mode: %w", err), file.Close())
	}
	if localRelease == nil {
		localRelease, err = reserveProcessSessionLease(info)
		if err != nil {
			return nil, errors.Join(err, file.Close())
		}
	}
	if err := tryLockSession(file); err != nil {
		localRelease()
		closeErr := file.Close()
		if errors.Is(err, ErrSessionLeaseHeld) {
			return nil, errors.Join(err, closeErr)
		}
		return nil, errors.Join(fmt.Errorf("lock planner session lease: %w", err), closeErr)
	}
	return &SessionLease{
		file:         file,
		localRelease: localRelease,
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
	l.once.Do(func() {
		defer l.localRelease()
		l.err = l.file.Close()
	})
	return l.err
}

func (l *SessionLease) Complete() error {
	l.once.Do(func() {
		defer l.localRelease()
		if err := l.verify(); err != nil {
			l.err = errors.Join(err, l.file.Close())
			return
		}
		l.err = completeSessionLease(l.file, l.remove)
	})
	return l.err
}

func reserveProcessSessionLease(info os.FileInfo) (func(), error) {
	processSessionLeases.Lock()
	defer processSessionLeases.Unlock()
	for _, held := range processSessionLeases.files {
		if os.SameFile(info, held) {
			return nil, ErrSessionLeaseHeld
		}
	}
	processSessionLeases.next++
	token := processSessionLeases.next
	processSessionLeases.files[token] = info
	var once sync.Once
	return func() {
		once.Do(func() {
			processSessionLeases.Lock()
			delete(processSessionLeases.files, token)
			processSessionLeases.Unlock()
		})
	}, nil
}
