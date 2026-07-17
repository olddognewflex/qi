//go:build darwin

package commands

import (
	"os"
	"syscall"
)

// datalessSupported gates the dataless check to macOS. Elsewhere a file with
// size > 0 and blocks == 0 is an ordinary sparse file, not an evicted one, so
// the same test would be a false positive — see dataless_other.go.
const datalessSupported = true

// fileBlocks returns the number of 512-byte blocks allocated to a file
// (st_blocks) from an already-taken stat result. It never opens the file:
// reading is precisely what forces iCloud to materialize it, which is the cost
// this check exists to warn about.
func fileBlocks(fi os.FileInfo) (int64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Blocks, true
}
