package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qi/internal/service"
	"qi/internal/tools"
)

func TestProcessInboxProposesPerItem(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "00-inbox")
	now := time.Now()

	writeCapture(t, inbox, "01-task.md", "- [ ] Email the client back", now)
	writeCapture(t, inbox, "02-short.md", "call dentist", now)
	writeCapture(t, inbox, "03-note.md", "Long thought about the architecture.\nSecond paragraph with more detail here.", now)
	// Empty body: only the header, no content lines.
	emptyPath := filepath.Join(inbox, "04-empty.md")
	if err := os.WriteFile(emptyPath, []byte("2026-05-18 10:00:00\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := tools.NewRegistry()
	if err := RegisterProcessInbox(r, inbox); err != nil {
		t.Fatalf("register: %v", err)
	}

	regTool, ok := r.Lookup(ProcessInboxToolName)
	if !ok {
		t.Fatal("tool not registered")
	}
	if regTool.Source.Kind != tools.SourceSkill {
		t.Fatalf("source = %v, want skill", regTool.Source.Kind)
	}
	if regTool.Mutating {
		t.Fatal("process-inbox must be read-only")
	}

	raw, err := tools.Execute(context.Background(), r, ProcessInboxToolName, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out processInboxOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Count != 4 || len(out.Items) != 4 {
		t.Fatalf("count = %d, items = %d, want 4", out.Count, len(out.Items))
	}

	// Items are sorted by path, so order matches the filename prefixes.
	want := []struct {
		base   string
		action string
	}{
		{"01-task.md", actionTask},
		{"02-short.md", actionTask},
		{"03-note.md", actionNote},
		{"04-empty.md", actionArchive},
	}
	for i, w := range want {
		got := out.Items[i]
		if filepath.Base(got.Path) != w.base {
			t.Errorf("item %d path = %s, want %s", i, got.Path, w.base)
		}
		if got.Action != w.action {
			t.Errorf("item %d (%s) action = %s, want %s", i, w.base, got.Action, w.action)
		}
		if got.Reason == "" {
			t.Errorf("item %d (%s) missing reason", i, w.base)
		}
	}
	if out.Items[0].Summary != "- [ ] Email the client back" {
		t.Errorf("task summary = %q", out.Items[0].Summary)
	}
}

func TestProcessInboxMissingDirIsNotFatal(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterProcessInbox(r, filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatalf("register: %v", err)
	}
	raw, err := tools.Execute(context.Background(), r, ProcessInboxToolName, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out processInboxOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 0 {
		t.Fatalf("count = %d, want 0", out.Count)
	}
}

func TestProcessInboxApplyIsMutating(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterProcessInboxApply(r, t.TempDir(), t.TempDir(), service.TaskService{}, service.NoteService{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := r.Lookup(ProcessInboxApplyToolName)
	if !ok {
		t.Fatal("apply tool not registered")
	}
	if !tool.Mutating {
		t.Fatal("process-inbox-apply must be Mutating so the policy gate confirms non-cli callers")
	}
}

func TestProcessInboxApplyTask(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "00-inbox")
	archive := filepath.Join(inbox, "archive")
	taskFile := filepath.Join(tmp, "inbox.md")

	src := writeCapture(t, inbox, "cap.md", "Follow up with Bob", time.Now())

	tasks := service.TaskService{TaskFilePath: taskFile}
	notes := service.NoteService{NotesDir: filepath.Join(tmp, "20-notes")}

	out := mustApply(t, inbox, archive, tasks, notes, inboxApplyInput{
		Path:    src,
		Action:  actionTask,
		Project: "ops",
	})

	if out.Action != actionTask {
		t.Errorf("action = %s", out.Action)
	}
	// Original capture moved out of the inbox into archive.
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present: %v", err)
	}
	if filepath.Dir(out.Archived) != archive {
		t.Errorf("archived to %s, want under %s", out.Archived, archive)
	}
	body, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Follow up with Bob") || !strings.Contains(string(body), "#ops") {
		t.Errorf("task file missing task: %s", body)
	}
}

func TestProcessInboxApplyNote(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "00-inbox")
	archive := filepath.Join(inbox, "archive")
	notesDir := filepath.Join(tmp, "20-notes")

	src := writeCapture(t, inbox, "cap.md", "Architecture idea\nUse a queue between the stages.", time.Now())

	notes := service.NoteService{NotesDir: notesDir}
	out := mustApply(t, inbox, archive, service.TaskService{}, notes, inboxApplyInput{
		Path:   src,
		Action: actionNote,
	})

	if out.Created == "" {
		t.Fatal("note path not returned")
	}
	content, err := os.ReadFile(out.Created)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if !strings.Contains(string(content), "Use a queue between the stages.") {
		t.Errorf("note missing body: %s", content)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present after note apply")
	}
}

func TestProcessInboxApplyArchiveOnly(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "00-inbox")
	archive := filepath.Join(inbox, "archive")
	src := writeCapture(t, inbox, "junk.md", "never mind", time.Now())

	out := mustApply(t, inbox, archive, service.TaskService{}, service.NoteService{}, inboxApplyInput{
		Path:   src,
		Action: actionArchive,
	})
	if out.Created != "" {
		t.Errorf("archive should create nothing, got %q", out.Created)
	}
	if _, err := os.Stat(out.Archived); err != nil {
		t.Errorf("archived file missing: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present")
	}
}

func TestProcessInboxApplyRejectsPathOutsideInbox(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "00-inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(tmp, "secret.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := applyInbox(inbox, filepath.Join(inbox, "archive"), service.TaskService{}, service.NoteService{}, inboxApplyInput{
		Path:   outside,
		Action: actionArchive,
	})
	if err == nil {
		t.Fatal("expected rejection for path outside inbox")
	}
	// File must be untouched.
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Errorf("outside file was disturbed: %v", statErr)
	}
}

func TestProcessInboxApplyArchiveCollision(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "00-inbox")
	archive := filepath.Join(inbox, "archive")

	src1 := writeCapture(t, inbox, "dup.md", "first", time.Now())
	out1 := mustApply(t, inbox, archive, service.TaskService{}, service.NoteService{}, inboxApplyInput{Path: src1, Action: actionArchive})

	src2 := writeCapture(t, inbox, "dup.md", "second", time.Now())
	out2 := mustApply(t, inbox, archive, service.TaskService{}, service.NoteService{}, inboxApplyInput{Path: src2, Action: actionArchive})

	if out1.Archived == out2.Archived {
		t.Fatalf("collision not avoided: both archived to %s", out1.Archived)
	}
}

func mustApply(t *testing.T, inbox, archive string, tasks service.TaskService, notes service.NoteService, in inboxApplyInput) inboxApplyOutput {
	t.Helper()
	out, err := applyInbox(inbox, archive, tasks, notes, in)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return out
}
