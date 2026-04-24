package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCapture(t *testing.T) {
	inbox := t.TempDir()
	if err := WriteCapture(inbox, "Buy oat milk"); err != nil {
		t.Fatalf("unexpected error: %v", err)
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

	data, err := os.ReadFile(filepath.Join(inbox, name))
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
	if err := WriteCapture(inbox, "   "); err == nil {
		t.Fatal("expected error for empty capture text")
	}
}

func TestWriteCapture_Mkdir(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "deep", "00-inbox")
	if err := WriteCapture(inbox, "Nested inbox test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(inbox); err != nil {
		t.Fatalf("inbox dir not created: %v", err)
	}
}
