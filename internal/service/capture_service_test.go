package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureService_Capture(t *testing.T) {
	inbox := t.TempDir()
	svc := CaptureService{InboxPath: inbox}

	if err := svc.Capture("Remember to call mom"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}

	data, err := os.ReadFile(filepath.Join(inbox, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Remember to call mom") {
		t.Fatalf("capture not found in file: %q", string(data))
	}
}
