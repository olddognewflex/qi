package vault

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrConcurrentModification is returned by WriteGuarded when the target file's
// mtime changed between the caller's read (gather) and the guarded write. It
// signals that another writer — almost always an Obsidian Sync pull — rewrote
// the file with edits qi has not merged, so the write was refused to avoid
// clobbering them. Callers should re-read and retry.
var ErrConcurrentModification = errors.New("vault: file modified concurrently since read")

// ReadFileMtime returns the modification time of path. If the file does not
// exist it returns the zero time and no error, so an absent projection/canon
// file reads as "never modified" — a brand-new file written under that zero
// expectation will only be refused if something else created it first.
func ReadFileMtime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return info.ModTime(), nil
}

// ReadFileBytes returns the raw bytes of path, or nil for an absent file. Used
// by the sync engine to compare desired content against on-disk content and
// skip no-op writes.
func ReadFileBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// WriteGuarded atomically writes data to path, but only if the file's current
// mtime still equals expectedMtime (the value captured when the caller read the
// file). Under withFileLock it re-stats the file; on a mismatch it returns
// ErrConcurrentModification without writing. This is the sole defense against
// Obsidian Sync racing qi's read->write: withFileLock only serializes qi-vs-qi,
// so the mtime re-check is what catches an external rewrite.
func WriteGuarded(path string, data []byte, expectedMtime time.Time) error {
	return withFileLock(path, func() error {
		current, err := ReadFileMtime(path)
		if err != nil {
			return err
		}
		// time.Time carries a monotonic clock reading on some platforms; mtimes
		// from Stat never do. Equal compares wall-clock instants correctly here
		// since both come from Stat. An absent file (zero) matching a zero
		// expectation means "still absent" -> safe to create.
		if !current.Equal(expectedMtime) {
			return ErrConcurrentModification
		}
		return writeFileAtomic(path, data, 0o644)
	})
}
