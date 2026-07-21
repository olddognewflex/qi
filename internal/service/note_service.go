package service

import (
	"fmt"

	"qi/internal/domain"
	"qi/internal/vault"
)

type NoteService struct {
	NotesDir string
}

// NewNoteService builds a NoteService writing into notesDir. A constructor
// (alongside NewTaskService / NewCaptureService) so the command layer has one
// consistent construction idiom per service.
func NewNoteService(notesDir string) NoteService {
	return NoteService{NotesDir: notesDir}
}

func (s NoteService) AddNote(title, body string) (domain.Note, error) {
	path, err := vault.WriteNote(s.NotesDir, title, body)
	if err != nil {
		return domain.Note{}, err
	}
	note, _, err := vault.ReadNote(path)
	if err != nil {
		return domain.Note{}, fmt.Errorf("read back note: %w", err)
	}
	return note, nil
}

func (s NoteService) GetNote(path string) (domain.Note, string, error) {
	return vault.ReadNote(path)
}

func (s NoteService) ListNotes() ([]domain.Note, error) {
	return vault.ListNotes(s.NotesDir)
}
