package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeInboxCapture writes a capture file in WriteCapture's shape: a header
// line the body parser skips, a blank line, then the content.
func writeInboxCapture(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	body := "2026-06-11 09:00:00\n\n" + content + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newInboxService(t *testing.T, inbox string) InboxService {
	t.Helper()
	tmp := filepath.Dir(inbox)
	return InboxService{
		InboxDir:   inbox,
		ArchiveDir: filepath.Join(inbox, "archive"),
		Tasks:      TaskService{TaskFilePath: filepath.Join(tmp, "inbox.md")},
		Notes:      NoteService{NotesDir: filepath.Join(tmp, "20-notes")},
	}
}

func TestInboxListProposesPerItem(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	writeInboxCapture(t, inbox, "01-task.md", "- [ ] Email the client back")
	writeInboxCapture(t, inbox, "02-short.md", "call dentist")
	writeInboxCapture(t, inbox, "03-note.md", "Long thought about the architecture.\nSecond paragraph with more detail.")
	writeInboxCapture(t, inbox, "04-empty.md", "")

	items, err := newInboxService(t, inbox).List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4", len(items))
	}

	want := []struct{ base, action string }{
		{"01-task.md", InboxActionTask},
		{"02-short.md", InboxActionTask},
		{"03-note.md", InboxActionNote},
		{"04-empty.md", InboxActionArchive},
	}
	for i, w := range want {
		if base := filepath.Base(items[i].Path); base != w.base {
			t.Errorf("item %d path = %s, want %s", i, base, w.base)
		}
		if items[i].Action != w.action {
			t.Errorf("item %d action = %s, want %s", i, items[i].Action, w.action)
		}
		if items[i].Reason == "" {
			t.Errorf("item %d missing reason", i)
		}
	}
	if got := items[2].Body; len(got) != 2 {
		t.Errorf("note body should expose both lines, got %v", got)
	}
}

func TestInboxListMissingDirIsNotFatal(t *testing.T) {
	svc := InboxService{InboxDir: filepath.Join(t.TempDir(), "nope")}
	items, err := svc.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
}

func TestInboxApplyTask(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	src := writeInboxCapture(t, inbox, "cap.md", "Follow up with Bob")

	out, err := svc.Apply(InboxApplyInput{Path: src, Action: InboxActionTask, Project: "ops"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present after apply")
	}
	if filepath.Dir(out.Archived) != svc.ArchiveDir {
		t.Errorf("archived to %s, want under %s", out.Archived, svc.ArchiveDir)
	}
	body, err := os.ReadFile(svc.Tasks.TaskFilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Follow up with Bob") || !strings.Contains(string(body), "#ops") {
		t.Errorf("task file missing task: %s", body)
	}
}

func TestInboxApplyNote(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	src := writeInboxCapture(t, inbox, "cap.md", "Architecture idea\nUse a queue between stages.")

	out, err := svc.Apply(InboxApplyInput{Path: src, Action: InboxActionNote})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Created == "" {
		t.Fatal("note path not returned")
	}
	content, err := os.ReadFile(out.Created)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if !strings.Contains(string(content), "Use a queue between stages.") {
		t.Errorf("note missing body: %s", content)
	}
}

func TestInboxApplyDeleteRemovesWithoutArchiving(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	src := writeInboxCapture(t, inbox, "junk.md", "never mind")

	out, err := svc.Apply(InboxApplyInput{Path: src, Action: InboxActionDelete})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Archived != "" {
		t.Errorf("delete should not archive, got %q", out.Archived)
	}
	if out.Created != "" {
		t.Errorf("delete should create nothing, got %q", out.Created)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present after delete")
	}
	if _, err := os.Stat(svc.ArchiveDir); !os.IsNotExist(err) {
		t.Errorf("delete should not create an archive dir")
	}
}

func TestInboxApplyRejectsPathOutsideInbox(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "00-inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(tmp, "secret.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := newInboxService(t, inbox)
	if _, err := svc.Apply(InboxApplyInput{Path: outside, Action: InboxActionDelete}); err == nil {
		t.Fatal("expected rejection for path outside inbox")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("outside file was disturbed: %v", err)
	}
}

func TestInboxApplyArchiveCollision(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)

	src1 := writeInboxCapture(t, inbox, "dup.md", "first")
	out1, err := svc.Apply(InboxApplyInput{Path: src1, Action: InboxActionArchive})
	if err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	src2 := writeInboxCapture(t, inbox, "dup.md", "second")
	out2, err := svc.Apply(InboxApplyInput{Path: src2, Action: InboxActionArchive})
	if err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	if out1.Archived == out2.Archived {
		t.Fatalf("collision not avoided: both archived to %s", out1.Archived)
	}
}

func TestInboxApplyUnknownAction(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	src := writeInboxCapture(t, inbox, "cap.md", "x")
	if _, err := svc.Apply(InboxApplyInput{Path: src, Action: "frobnicate"}); err == nil {
		t.Fatal("expected error for unknown action")
	}
}
