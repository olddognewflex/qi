package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCapture(t *testing.T) {
	inbox := t.TempDir()
	path, err := WriteCapture(inbox, "Buy oat milk")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Dir(path) != inbox {
		t.Fatalf("returned path %q not in inbox %q", path, inbox)
	}

	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}

	name := entries[0].Name()
	if !strings.HasSuffix(name, ".md") {
		t.Fatalf("expected .md suffix, got %q", name)
	}
	if filepath.Base(path) != name {
		t.Fatalf("returned path basename %q != listed entry %q", filepath.Base(path), name)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "Buy oat milk") {
		t.Fatalf("capture content missing: %q", content)
	}
	if !strings.Contains(content, "2026-") {
		t.Fatalf("expected timestamp prefix: %q", content)
	}
}

func TestWriteCapture_EmptyText(t *testing.T) {
	inbox := t.TempDir()
	if _, err := WriteCapture(inbox, "   "); err == nil {
		t.Fatal("expected error for empty capture text")
	}
}

func TestWriteCapture_Mkdir(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "deep", "00-inbox")
	if _, err := WriteCapture(inbox, "Nested inbox test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(inbox); err != nil {
		t.Fatalf("inbox dir not created: %v", err)
	}
}

func TestWriteCapture_NoCollision(t *testing.T) {
	inbox := t.TempDir()
	p1, err := WriteCapture(inbox, "first")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := WriteCapture(inbox, "second")
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatalf("two captures returned the same path %q", p1)
	}
	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 capture files, got %d", len(entries))
	}
	// Both payloads must survive — second-granularity names would otherwise
	// let the second capture overwrite the first.
	for _, want := range []struct{ path, text string }{{p1, "first"}, {p2, "second"}} {
		data, err := os.ReadFile(want.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), want.text) {
			t.Fatalf("file %q missing %q", want.path, want.text)
		}
	}
}
