package service

import (
	"testing"
)

func TestNoteService_AddNote(t *testing.T) {
	dir := t.TempDir()
	svc := NoteService{NotesDir: dir}

	note, err := svc.AddNote("Test Note", "body content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if note.Title != "Test Note" {
		t.Errorf("Title = %q, want Test Note", note.Title)
	}
}

func TestNoteService_ListNotes(t *testing.T) {
	dir := t.TempDir()
	svc := NoteService{NotesDir: dir}

	if _, err := svc.AddNote("A", "body"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddNote("B", "body"); err != nil {
		t.Fatal(err)
	}

	notes, err := svc.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(notes))
	}
}

func TestNoteService_GetNote(t *testing.T) {
	dir := t.TempDir()
	svc := NoteService{NotesDir: dir}

	note, err := svc.AddNote("Readable", "hello world")
	if err != nil {
		t.Fatal(err)
	}

	readNote, content, err := svc.GetNote(note.Path)
	if err != nil {
		t.Fatal(err)
	}
	if readNote.Title != "Readable" {
		t.Errorf("Title = %q, want Readable", readNote.Title)
	}
	if content == "" {
		t.Error("expected non-empty content")
	}
}
