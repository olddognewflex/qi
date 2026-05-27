package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadFileMtime_Absent(t *testing.T) {
	mt, err := ReadFileMtime(filepath.Join(t.TempDir(), "nope.md"))
	if err != nil {
		t.Fatalf("absent file should not error: %v", err)
	}
	if !mt.IsZero() {
		t.Errorf("absent file should yield zero time, got %v", mt)
	}
}

func TestWriteGuarded_HappyPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt, _ := ReadFileMtime(path)

	if err := WriteGuarded(path, []byte("new\n"), mt); err != nil {
		t.Fatalf("guarded write should succeed when mtime matches: %v", err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "new\n" {
		t.Errorf("content = %q, want %q", b, "new\n")
	}
}

func TestWriteGuarded_CreateAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.md")
	// expected zero mtime == absent; create succeeds.
	if err := WriteGuarded(path, []byte("hi\n"), time.Time{}); err != nil {
		t.Fatalf("create under zero expectation should succeed: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "hi\n" {
		t.Errorf("content = %q", b)
	}
}

func TestWriteGuarded_ConcurrentModification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, _ := ReadFileMtime(path)

	// Simulate an external (Obsidian Sync) write bumping the mtime.
	future := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	err := WriteGuarded(path, []byte("clobber\n"), stale)
	if err != ErrConcurrentModification {
		t.Fatalf("expected ErrConcurrentModification, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "old\n" {
		t.Errorf("file must be untouched on guard failure, got %q", b)
	}
}
