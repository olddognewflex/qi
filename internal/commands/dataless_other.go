//go:build !darwin

package commands

import "os"

// datalessSupported is false off darwin. Eviction of a file's contents while
// its metadata survives ("dataless" files) is a macOS/iCloud phenomenon; on
// Linux, size > 0 with blocks == 0 legitimately describes a sparse file, and
// syscall.Stat_t has no Blocks field on Windows at all. The check is skipped
// rather than reported, so no spurious warning appears.
const datalessSupported = false

// fileBlocks reports that block counts are unavailable on this platform.
func fileBlocks(os.FileInfo) (int64, bool) { return 0, false }
