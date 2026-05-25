package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// lockPathFor returns the machine-local advisory-lock file for a vault file.
// The lock lives in os.TempDir() (never synced, shared across processes of the
// same user on one machine), keyed by a hash of the file's absolute path so
// every qi process agrees on the same lock for the same target. It is
// deliberately NOT placed next to the file inside the vault, which would be
// picked up by Obsidian/iCloud sync.
func lockPathFor(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(os.TempDir(), "qi-lock-"+hex.EncodeToString(sum[:8])+".lock")
}
