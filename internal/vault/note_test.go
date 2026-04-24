package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Hello World", "hello-world"},
		{"  spaced  ", "spaced"},
		{"A&B*C", "a-b-c"},
		{"UPPERCASE", "uppercase"},
		{"", "untitled"},
		{"---", "untitled"},
		{"file/123", "file-123"},
	}
	for _, c := range cases {
		got := sanitizeFilename(c.input)
		if got != c.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestWriteNote(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteNote(dir, "Test Note", "This is the body.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, "test-note.md") {
		t.Fatalf("expected path to end with test-note.md, got %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "# Test Note") {
		t.Fatalf("expected heading, got:\n%s", content)
	}
	if !strings.Contains(content, "This is the body.") {
		t.Fatalf("expected body, got:\n%s", content)
	}
}

func TestWriteNote_DuplicateTitle(t *testing.T) {
	dir := t.TempDir()
	_, err := WriteNote(dir, "Dup", "first")
	if err != nil {
		t.Fatal(err)
	}
	path2, err := WriteNote(dir, "Dup", "second")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path2) == "dup.md" {
		t.Fatal("expected deduplicated filename")
	}
}

func TestWriteNote_EmptyTitle(t *testing.T) {
	_, err := WriteNote(t.TempDir(), "", "body")
	if err == nil {
		t.Fatal("expected error for empty title")
	}
}

func TestReadNote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.md")
	os.WriteFile(path, []byte("# Hello\n\nworld #tag1 #tag2\n"), 0o644)

	note, content, err := ReadNote(path)
	if err != nil {
		t.Fatal(err)
	}
	if note.Title != "Hello" {
		t.Errorf("Title = %q, want Hello", note.Title)
	}
	if len(note.Tags) != 2 {
		t.Errorf("Tags = %v, want 2", note.Tags)
	}
	if !strings.Contains(content, "world") {
		t.Error("expected body content")
	}
}

func TestListNotes(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("# A\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("# B\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "skip.txt"), []byte("text"), 0o644)

	notes, err := ListNotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(notes))
	}
}

func TestListNotes_MissingDir(t *testing.T) {
	notes, err := ListNotes(filepath.Join(t.TempDir(), "nonexistent"))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("expected 0 notes, got %d", len(notes))
	}
}

func TestWalkVault(t *testing.T) {
	vault := t.TempDir()
	os.WriteFile(filepath.Join(vault, "readme.md"), []byte("# Readme\n"), 0o644)
	os.MkdirAll(filepath.Join(vault, ".git"), 0o755)
	os.WriteFile(filepath.Join(vault, ".git", "config"), []byte("git"), 0o644)
	os.MkdirAll(filepath.Join(vault, "subdir"), 0o755)
	os.WriteFile(filepath.Join(vault, "subdir", "note.md"), []byte("# Sub\n"), 0o644)

	var found []string
	err := WalkVault(vault, func(path, content string) error {
		found = append(found, filepath.Base(path))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("expected 2 markdown files, got %d: %v", len(found), found)
	}
}


