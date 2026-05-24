package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"qi/internal/domain"
)

func TestRenderAgenda_Empty(t *testing.T) {
	var s DailyService
	if got := s.RenderAgenda(nil); got != "No events." {
		t.Errorf("RenderAgenda(nil) = %q, want %q", got, "No events.")
	}
}

func TestRenderAgenda_Events(t *testing.T) {
	var s DailyService
	base := time.Date(2026, 5, 24, 9, 0, 0, 0, time.Local)
	events := []domain.Event{
		{Title: "Standup", Start: base},                                                // no end
		{Title: "Planning", Start: base.Add(time.Hour), End: base.Add(2 * time.Hour), Project: "qi"}, // range + project
	}
	got := s.RenderAgenda(events)
	want := "- 09:00 Standup\n- 10:00–11:00 Planning #qi"
	if got != want {
		t.Errorf("RenderAgenda =\n%q\nwant\n%q", got, want)
	}
}

func TestEnsureNote_ExistingFileSkipsOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	opened := false
	s := DailyService{
		PathFor:    func(time.Time) string { return path },
		OpenFunc:   func(string) error { opened = true; return nil },
		CreateWait: time.Second,
	}
	got, err := s.EnsureNote(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	if opened {
		t.Error("OpenFunc called even though note exists")
	}
}

func TestEnsureNote_TriggersAndFindsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")

	s := DailyService{
		PathFor: func(time.Time) string { return path },
		OpenFunc: func(string) error {
			// Simulate Obsidian creating the note on trigger.
			return os.WriteFile(path, []byte("# created\n"), 0o644)
		},
		CreateWait: time.Second,
	}
	got, err := s.EnsureNote(time.Now())
	if err != nil {
		t.Fatalf("EnsureNote: %v", err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
}

func TestEnsureNote_TimeoutWhenNeverCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "never.md")

	s := DailyService{
		PathFor:    func(time.Time) string { return path },
		OpenFunc:   func(string) error { return nil }, // never creates the file
		CreateWait: 150 * time.Millisecond,
	}
	start := time.Now()
	_, err := s.EnsureNote(time.Now())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("returned too fast (%v), did not poll", elapsed)
	}
}
