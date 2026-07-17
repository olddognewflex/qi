package commands

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// datalessRemedy is the user-facing fix for evicted vault files: macOS
// "Optimize Mac Storage" purges the contents of iCloud-backed files under
// ~/Documents while keeping their metadata, and pinning the folder stops it.
const datalessRemedy = `in Finder, right-click the vault folder → "Keep Downloaded"`

// datalessScan is the outcome of walking the vault: how many markdown files
// were stat'd and how many of those had their contents evicted.
type datalessScan struct {
	scanned  int
	dataless int
}

// blocksFn reports a file's allocated 512-byte block count from an existing
// stat result. It is the seam that keeps the syscall behind a platform helper
// (fileBlocks) so the walk and the decision logic stay testable everywhere.
type blocksFn func(os.FileInfo) (int64, bool)

// isDataless reports whether a stat result describes an evicted ("dataless")
// file: macOS keeps the inode and its size but frees every block, so the file
// reads as non-empty metadata backed by no data. An empty file (size == 0) also
// has zero blocks but holds no data to evict, so it is not dataless.
//
// This is a signature, not a certainty: a sparse file has the same one. Markdown
// is never sparse in practice, and the check only ever warns, so a false positive
// is harmless — but it detects "no blocks allocated", not "iCloud evicted this".
// It is also a floor: iCloud can leave a file partly resident, and those report
// blocks > 0 while still stalling on read.
//
// Reading such a file blocks in read(2) while iCloud re-downloads it —
// unbounded latency, and an outright failure when offline. That collides with
// two invariants in CLAUDE.md: markdown is canonical and the vault must stay
// usable without qi (#1), yet macOS can silently purge that canonical store's
// contents; and `qi capture` must stay under 100ms (#2), while any vault read
// on the surrounding paths — `qi task done`, `qi agenda`'s LocalProvider,
// `qi plan` — can stall on a network fetch when the file is dataless.
func isDataless(size, blocks int64) bool {
	return size > 0 && blocks == 0
}

// datalessStatus maps a completed scan onto a check result. Evicted files are
// an environmental condition the user may knowingly accept, so this warns and
// never fails — matching doctor's existing treatment of optional/lazy
// components.
func datalessStatus(scan datalessScan) (checkStatus, string) {
	if scan.dataless == 0 {
		return statusOK, fmt.Sprintf("%d file(s) present on disk", scan.scanned)
	}
	return statusWarn, fmt.Sprintf("%d of %d file(s) evicted from disk", scan.dataless, scan.scanned)
}

// scanDataless stats every markdown file under root and counts the evicted
// ones. Like doctor's index-freshness check, it is deliberately stat-only and
// never reads file contents — here that discipline is load-bearing rather than
// merely cheap, since a read is exactly the iCloud download being warned about.
//
// Only regular files are considered, so symlinks are skipped and never followed
// out of the vault. A stat error on any single file is ignored rather than
// aborting the walk.
func scanDataless(root string, blocks blocksFn) datalessScan {
	var scan datalessScan
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		// Skips symlinks, devices, sockets — anything without ordinary blocks.
		if !d.Type().IsRegular() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		// d.Info() is an lstat on a walked file: metadata only, no open(2).
		info, err := d.Info()
		if err != nil {
			return nil
		}
		n, ok := blocks(info)
		if !ok {
			return nil
		}
		scan.scanned++
		if isDataless(info.Size(), n) {
			scan.dataless++
		}
		return nil
	})
	return scan
}
