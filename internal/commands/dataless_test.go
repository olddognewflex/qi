package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsDataless(t *testing.T) {
	tests := []struct {
		name   string
		size   int64
		blocks int64
		want   bool
	}{
		{"evicted: metadata kept, blocks freed", 4096, 0, true},
		{"evicted: single byte of metadata", 1, 0, true},
		{"empty file has no data to evict", 0, 0, false},
		{"resident file", 4096, 8, false},
		{"resident: small file, one block", 12, 1, false},
		{"empty file with an allocated block", 0, 8, false},
		{"large resident file", 1 << 20, 2048, false},
		{"negative blocks treated as resident", 4096, -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDataless(tt.size, tt.blocks); got != tt.want {
				t.Errorf("isDataless(%d, %d) = %v, want %v", tt.size, tt.blocks, got, tt.want)
			}
		})
	}
}

func TestDatalessStatus(t *testing.T) {
	tests := []struct {
		name       string
		scan       datalessScan
		wantStatus checkStatus
		wantParts  []string
	}{
		{
			name:       "no files scanned",
			scan:       datalessScan{scanned: 0, dataless: 0},
			wantStatus: statusOK,
			wantParts:  []string{"0"},
		},
		{
			name:       "all files resident",
			scan:       datalessScan{scanned: 142, dataless: 0},
			wantStatus: statusOK,
			wantParts:  []string{"142"},
		},
		{
			name:       "some files evicted warns, never fails",
			scan:       datalessScan{scanned: 142, dataless: 43},
			wantStatus: statusWarn,
			wantParts:  []string{"43", "142"},
		},
		{
			name:       "every file evicted still only warns",
			scan:       datalessScan{scanned: 142, dataless: 142},
			wantStatus: statusWarn,
			wantParts:  []string{"142"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, summary := datalessStatus(tt.scan)
			if status != tt.wantStatus {
				t.Errorf("status = %v, want %v (summary %q)", status, tt.wantStatus, summary)
			}
			if status == statusFail {
				t.Error("dataless files are environmental; the check must never fail")
			}
			for _, part := range tt.wantParts {
				if !strings.Contains(summary, part) {
					t.Errorf("summary %q missing %q", summary, part)
				}
			}
		})
	}
}

// fakeBlocks reports block counts by file size, standing in for the real
// syscall so the walk is testable: a dataless file cannot be created on demand.
func fakeBlocks(evictedSizes map[int64]bool) blocksFn {
	return func(fi os.FileInfo) (int64, bool) {
		if evictedSizes[fi.Size()] {
			return 0, true
		}
		return (fi.Size() + 511) / 512, true
	}
}

func TestScanDataless_RealDirAllResident(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "20-notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := []string{"a.md", "20-notes/b.md", "20-notes/c.MD"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(root, f), []byte("# note\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Non-markdown and dot-dir contents must be ignored.
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obsidian", "cache.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	scan := scanDataless(root, fakeBlocks(nil))
	if scan.scanned != len(files) {
		t.Errorf("scanned = %d, want %d", scan.scanned, len(files))
	}
	if scan.dataless != 0 {
		t.Errorf("dataless = %d, want 0 for a freshly written dir", scan.dataless)
	}
	if status, _ := datalessStatus(scan); status != statusOK {
		t.Errorf("status = %v, want statusOK", status)
	}
}

func TestScanDataless_CountsEvicted(t *testing.T) {
	root := t.TempDir()
	// Distinct sizes let fakeBlocks single out which file is "evicted".
	if err := os.WriteFile(filepath.Join(root, "resident.md"), []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "evicted.md"), []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "empty.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	scan := scanDataless(root, fakeBlocks(map[int64]bool{4: true}))
	if scan.scanned != 3 {
		t.Errorf("scanned = %d, want 3", scan.scanned)
	}
	// empty.md also has zero blocks but zero size: not dataless.
	if scan.dataless != 1 {
		t.Errorf("dataless = %d, want 1", scan.dataless)
	}
	status, summary := datalessStatus(scan)
	if status != statusWarn {
		t.Errorf("status = %v, want statusWarn (%q)", status, summary)
	}
}

func TestScanDataless_SkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real.md"), []byte("here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	scan := scanDataless(root, fakeBlocks(nil))
	if scan.scanned != 1 {
		t.Errorf("scanned = %d, want 1 (the symlink must not be followed)", scan.scanned)
	}
}

// blocksUnavailable stands in for a platform or filesystem that cannot report
// block counts; such files must be skipped rather than counted as evicted.
func TestScanDataless_SkipsWhenBlocksUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	scan := scanDataless(root, func(os.FileInfo) (int64, bool) { return 0, false })
	if scan.scanned != 0 || scan.dataless != 0 {
		t.Errorf("scan = %+v, want zero values when blocks are unavailable", scan)
	}
}

func TestScanDataless_MissingRoot(t *testing.T) {
	scan := scanDataless(filepath.Join(t.TempDir(), "nope"), fakeBlocks(nil))
	if scan.scanned != 0 || scan.dataless != 0 {
		t.Errorf("scan = %+v, want zero values for a missing root", scan)
	}
}

func TestCheckDataless_HealthyVaultReportsOK(t *testing.T) {
	cfg := healthyVault(t)
	if err := os.WriteFile(filepath.Join(cfg.NotesPath, "n.md"), []byte("# n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	if !datalessSupported {
		if strings.Contains(out, "vault data") {
			t.Errorf("check must be skipped off darwin, not reported:\n%s", out)
		}
		return
	}
	if !strings.Contains(out, "[ok  ] vault data") {
		t.Errorf("expected an ok vault data line:\n%s", out)
	}
}
