package vault

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path atomically. It writes to a temporary
// file in the same directory, fsyncs it, then renames over the destination.
// rename(2) within a filesystem is atomic, so neither a concurrent reader nor
// a crash mid-write ever observes a partially written file — the vault's
// canonical markdown stays intact even if the process dies. Replaces direct
// os.WriteFile for any full-file rewrite of a tracked vault file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename succeeds. After a
	// successful rename this is a no-op (the temp name no longer exists).
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}
