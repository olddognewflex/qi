package service

import (
	"path/filepath"
	"testing"
)

func TestNewTaskServiceDerivesTasksDir(t *testing.T) {
	path := filepath.Join("vault", "10-tasks", "inbox.md")
	svc := NewTaskService(path)
	if svc.TaskFilePath != path {
		t.Errorf("TaskFilePath = %q, want %q", svc.TaskFilePath, path)
	}
	if want := filepath.Join("vault", "10-tasks"); svc.TasksDir != want {
		t.Errorf("TasksDir = %q, want %q (derived from the file's dir)", svc.TasksDir, want)
	}
}

func TestNewNoteAndCaptureService(t *testing.T) {
	if got := NewNoteService("/v/20-notes"); got.NotesDir != "/v/20-notes" {
		t.Errorf("NoteService.NotesDir = %q", got.NotesDir)
	}
	if got := NewCaptureService("/v/00-inbox"); got.InboxPath != "/v/00-inbox" {
		t.Errorf("CaptureService.InboxPath = %q", got.InboxPath)
	}
}
