package ai

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	SessionVersion  = 1
	MaxSessionBytes = 8 << 20
)

var (
	ErrSessionLeaseHeld = errors.New("planner session is already being resumed")
	ErrInvalidSession   = errors.New("invalid planner session")
)

type SessionStore struct {
	root *os.Root
	dir  *os.File
}

func SessionDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve planner session home: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("resolve planner session directory: state home must be absolute")
	}
	return filepath.Join(base, "qi", "ai-sessions"), nil
}

func DefaultSessionStore() (*SessionStore, error) {
	root, err := SessionDir()
	if err != nil {
		return nil, err
	}
	return NewSessionStore(root)
}

func NewSessionStore(path string) (*SessionStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("open planner session store: root must be absolute")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("create planner session directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect planner session directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("open planner session store: root is not a real directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return nil, fmt.Errorf("repair planner session directory mode: %w", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open planner session root: %w", err)
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !rootInfo.IsDir() || !os.SameFile(info, rootInfo) {
		closeErr := root.Close()
		if err != nil {
			return nil, errors.Join(fmt.Errorf("verify planner session root: %w", err), closeErr)
		}
		return nil, errors.Join(errors.New("verify planner session root: root changed while opening"), closeErr)
	}
	dir, err := os.Open(path)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open planner session directory for sync: %w", err), root.Close())
	}
	dirInfo, err := dir.Stat()
	if err != nil || !dirInfo.IsDir() || !os.SameFile(info, dirInfo) {
		closeErr := errors.Join(dir.Close(), root.Close())
		if err != nil {
			return nil, errors.Join(fmt.Errorf("verify planner session directory: %w", err), closeErr)
		}
		return nil, errors.Join(errors.New("verify planner session directory: root changed while opening"), closeErr)
	}
	return &SessionStore{root: root, dir: dir}, nil
}

func (s *SessionStore) Close() error {
	rootErr := s.root.Close()
	dirErr := s.dir.Close()
	return errors.Join(rootErr, dirErr)
}

func (s *SessionStore) Save(session Session) (returnErr error) {
	if err := validateStoredSession(session); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal planner session: %w", err)
	}
	if len(data) > MaxSessionBytes {
		return fmt.Errorf("%w: encoded snapshot exceeds %d bytes", ErrInvalidSession, MaxSessionBytes)
	}
	name := sessionFileName(session.SessionID)
	if err := s.rejectExistingNonRegular(name); err != nil {
		return err
	}
	tmpName, tmp, err := s.createTemp(session.SessionID)
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			removeErr := s.root.Remove(tmpName)
			if !errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, removeErr)
			}
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return errors.Join(fmt.Errorf("chmod planner session temporary file: %w", err), tmp.Close())
	}
	if err := writeFull(tmp, data); err != nil {
		return errors.Join(fmt.Errorf("write planner session temporary file: %w", err), tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync planner session temporary file: %w", err), tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close planner session temporary file: %w", err)
	}
	if err := s.root.Rename(tmpName, name); err != nil {
		return fmt.Errorf("replace planner session: %w", err)
	}
	renamed = true
	if err := s.dir.Sync(); err != nil {
		return fmt.Errorf("sync planner session directory: %w", err)
	}
	return nil
}

func (s *SessionStore) Load(id SessionID) (Session, error) {
	name := sessionFileName(id)
	if err := s.requireRegular(name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Session{}, fmt.Errorf("no such planner session %q (it may have completed or expired): %w", id, err)
		}
		return Session{}, err
	}
	file, err := s.root.Open(name)
	if err != nil {
		return Session{}, fmt.Errorf("open planner session %s: %w", id, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Session{}, fmt.Errorf("inspect open planner session %s: %w", id, err)
	}
	if !info.Mode().IsRegular() {
		return Session{}, fmt.Errorf("%w: session file is not regular", ErrInvalidSession)
	}
	if err := file.Chmod(0o600); err != nil {
		return Session{}, fmt.Errorf("repair planner session mode: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxSessionBytes+1))
	if err != nil {
		return Session{}, fmt.Errorf("read planner session %s: %w", id, err)
	}
	if len(data) > MaxSessionBytes {
		return Session{}, fmt.Errorf("%w: snapshot exceeds %d bytes", ErrInvalidSession, MaxSessionBytes)
	}
	var session Session
	decoder := json.NewDecoder(&limitedBytesReader{data: data})
	if err := decoder.Decode(&session); err != nil {
		return Session{}, fmt.Errorf("parse planner session %s: %w", id, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("additional JSON value")
		}
		return Session{}, fmt.Errorf("parse planner session %s: trailing data: %w", id, err)
	}
	if err := validateStoredSession(session); err != nil {
		return Session{}, err
	}
	if session.SessionID != id {
		return Session{}, fmt.Errorf("%w: embedded session id does not match filename", ErrInvalidSession)
	}
	return session, nil
}

func (s *SessionStore) Delete(id SessionID) error {
	name := sessionFileName(id)
	if err := s.requireRegular(name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := s.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete planner session %s: %w", id, err)
	}
	if err := s.dir.Sync(); err != nil {
		return fmt.Errorf("sync planner session directory after delete: %w", err)
	}
	return nil
}

func (s *SessionStore) requireRegular(name string) error {
	info, err := s.root.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is a symlink or non-regular file", ErrInvalidSession, name)
	}
	return nil
}

func (s *SessionStore) rejectExistingNonRegular(name string) error {
	err := s.requireRegular(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *SessionStore) createTemp(id SessionID) (string, *os.File, error) {
	for range 10 {
		var randomBytes [16]byte
		if _, err := io.ReadFull(rand.Reader, randomBytes[:]); err != nil {
			return "", nil, fmt.Errorf("generate planner session temporary name: %w", err)
		}
		name := "." + id.String() + ".tmp-" + hex.EncodeToString(randomBytes[:])
		file, err := s.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("create planner session temporary file: %w", err)
		}
		return name, file, nil
	}
	return "", nil, errors.New("create planner session temporary file: too many name collisions")
}
