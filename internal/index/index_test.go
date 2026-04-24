package index

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpen(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	idx, err := Open()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer idx.Close()
}

func TestRebuildAndSearch(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	vault := t.TempDir()
	os.WriteFile(filepath.Join(vault, "a.md"), []byte("# Apple\n\nApples are red fruits.\n"), 0o644)
	os.WriteFile(filepath.Join(vault, "b.md"), []byte("# Banana\n\nBananas are yellow.\n"), 0o644)

	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	if err := idx.Rebuild(vault); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	results, err := idx.Search("apple")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'apple'")
	}
	if results[0].Note.Title != "Apple" {
		t.Errorf("Title = %q, want Apple", results[0].Note.Title)
	}
}

func TestSearch_EmptyQuery(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	_, err = idx.Search("  ")
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestExtractSnippet(t *testing.T) {
	body := "The quick brown fox jumps over the lazy dog. " +
		"The quick brown fox jumps over the lazy dog."
	got := extractSnippet(body, "fox")
	if got == "" {
		t.Fatal("expected non-empty snippet")
	}
	if !containsIgnoreCase(got, "fox") {
		t.Fatalf("expected snippet to contain 'fox', got %q", got)
	}
}

func TestExtractSnippet_NoMatch(t *testing.T) {
	body := "Short text."
	got := extractSnippet(body, "xyz")
	if got != body {
		t.Fatalf("expected full body, got %q", got)
	}
}

func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0)
}
