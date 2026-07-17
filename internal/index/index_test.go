package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qi/internal/domain"
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

func TestClassifyKind(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/vault/00-inbox/2026-06-11-1200.md", domain.SearchKindInbox},
		{"/vault/10-tasks/inbox.md", domain.SearchKindTask},
		{"/vault/20-notes/x.md", domain.SearchKindNote},
		{"/vault/20-notes/sub/x.md", domain.SearchKindNote},
		{"/vault/30-daily/2026-06-11.md", domain.SearchKindDaily},
		{"20-notes/x.md", domain.SearchKindNote},
		{"/vault/README.md", domain.SearchKindOther},
		{"/vault/40-other/x.md", domain.SearchKindOther},
	}
	for _, c := range cases {
		if got := classifyKind(c.path); got != c.want {
			t.Errorf("classifyKind(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestSearchWith_KindFilter(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	vault := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(vault, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("10-tasks/inbox.md", "# Tasks\n\n- [ ] do the alpha thing\n")
	mustWrite("20-notes/n.md", "# Note\n\nThis note mentions alpha.\n")
	mustWrite("30-daily/2026-06-11.md", "# Daily\n\nReviewed alpha today.\n")

	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.Rebuild(vault); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	noteOnly, err := idx.SearchWith("alpha", SearchOptions{Kinds: []string{domain.SearchKindNote}})
	if err != nil {
		t.Fatalf("search note-only: %v", err)
	}
	if len(noteOnly) != 1 {
		t.Fatalf("note-only: got %d results, want 1", len(noteOnly))
	}
	for _, r := range noteOnly {
		if r.Kind != domain.SearchKindNote {
			t.Errorf("note-only result kind = %q, want note", r.Kind)
		}
	}

	all, err := idx.SearchWith("alpha", SearchOptions{})
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all: got %d results, want 3", len(all))
	}
	seen := map[string]bool{}
	for _, r := range all {
		seen[r.Kind] = true
	}
	for _, k := range []string{domain.SearchKindNote, domain.SearchKindTask, domain.SearchKindDaily} {
		if !seen[k] {
			t.Errorf("all: missing kind %q (got %v)", k, seen)
		}
	}
}

func TestSearch_PopulatesKind(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	vault := t.TempDir()
	os.WriteFile(filepath.Join(vault, "a.md"), []byte("# Apple\n\nApples are red.\n"), 0o644)

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
		t.Fatal("expected results")
	}
	for _, r := range results {
		if r.Kind == "" {
			t.Errorf("result %q has empty Kind", r.Note.Path)
		}
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

func TestUpsertFile_InsertUpdateAndStats(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	vault := t.TempDir()
	path := filepath.Join(vault, "note.md")
	os.WriteFile(path, []byte("# Cherry\n\nCherries are small.\n"), 0o644)

	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Insert.
	if err := idx.UpsertFile(path); err != nil {
		t.Fatalf("upsert (insert): %v", err)
	}
	results, err := idx.Search("cherry")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Note.Title != "Cherry" {
		t.Fatalf("after insert: results = %+v, want one Cherry", results)
	}

	// Update: same path must REPLACE the row, not add a second one.
	os.WriteFile(path, []byte("# Cherry\n\nCherries are sweet and small.\n"), 0o644)
	if err := idx.UpsertFile(path); err != nil {
		t.Fatalf("upsert (update): %v", err)
	}
	results, err = idx.Search("cherry")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("after update: %d rows for one path, want 1", len(results))
	}
	if want := "sweet"; !strings.Contains(results[0].Match, want) {
		t.Errorf("snippet %q does not reflect updated body (want %q)", results[0].Match, want)
	}

	// Stats: marker set, one row.
	last, rows, err := Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if last.IsZero() {
		t.Error("last_indexed marker not stamped by UpsertFile")
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1", rows)
	}
}

func TestDeleteFile_RemovesRowAndEmbedding(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	vault := t.TempDir()
	path := filepath.Join(vault, "gone.md")
	os.WriteFile(path, []byte("# Durian\n\nPungent.\n"), 0o644)

	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	if err := idx.UpsertFile(path); err != nil {
		t.Fatal(err)
	}
	if err := idx.UpsertEmbedding(path, "test-model", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}

	if err := idx.DeleteFile(path); err != nil {
		t.Fatalf("delete: %v", err)
	}

	results, err := idx.Search("durian")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("deleted note still in FTS: %+v", results)
	}
	embs, err := idx.EmbeddingsFor("test-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(embs) != 0 {
		t.Errorf("deleted note still has embedding rows: %d", len(embs))
	}
}

func TestStats_LegacyDBWithoutMeta(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	idx.Close()

	// Fresh schema but no marker ever stamped: zero time, zero rows, no error.
	last, rows, err := Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if !last.IsZero() {
		t.Errorf("last = %v, want zero when never stamped", last)
	}
	if rows != 0 {
		t.Errorf("rows = %d, want 0", rows)
	}
}

func TestRebuild_StampsLastIndexed(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	vault := t.TempDir()
	os.WriteFile(filepath.Join(vault, "x.md"), []byte("# X\n"), 0o644)

	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.Rebuild(vault); err != nil {
		t.Fatal(err)
	}

	last, rows, err := Stats()
	if err != nil {
		t.Fatal(err)
	}
	if last.IsZero() {
		t.Error("Rebuild did not stamp last_indexed")
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1", rows)
	}
}
