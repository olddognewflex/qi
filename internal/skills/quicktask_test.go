package skills

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"qi/internal/service"
	"qi/internal/tools"
	"qi/internal/vault"
)

func TestQuickTaskAddsAndReturnsOpenList(t *testing.T) {
	tmp := t.TempDir()
	tasksDir := tmp
	tasks := service.TaskService{
		TaskFilePath: filepath.Join(tasksDir, "inbox.md"),
		TasksDir:     tasksDir,
	}

	// Seed one pre-existing open task so the returned list has more than the
	// freshly added one.
	if _, err := tasks.CreateTask(service.AddTaskInput{Text: "existing task"}); err != nil {
		t.Fatal(err)
	}

	out, err := addQuickTask(tasks, quickTaskInput{Text: "ship it", Project: "qi"})
	if err != nil {
		t.Fatalf("addQuickTask: %v", err)
	}

	if out.Created.Text != "ship it" {
		t.Errorf("created text = %q, want %q", out.Created.Text, "ship it")
	}
	if out.Created.Project != "qi" {
		t.Errorf("created project = %q, want qi", out.Created.Project)
	}
	if out.Created.File == "" {
		t.Error("created file path not returned")
	}
	if out.OpenCount != 2 || len(out.OpenTasks) != 2 {
		t.Fatalf("open_count = %d, open_tasks = %d, want 2", out.OpenCount, len(out.OpenTasks))
	}

	// The newly added task must appear in the open list.
	var found bool
	for _, ot := range out.OpenTasks {
		if ot.Text == "ship it #qi" || ot.Text == "ship it" {
			found = true
		}
	}
	if !found {
		t.Errorf("added task not in open list: %+v", out.OpenTasks)
	}
}

func TestQuickTaskDueScheduledRepeatRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	tasks := service.TaskService{
		TaskFilePath: filepath.Join(tmp, "inbox.md"),
		TasksDir:     tmp,
	}

	out, err := addQuickTask(tasks, quickTaskInput{
		Text:      "recurring chore",
		Due:       "2026-06-20",
		Scheduled: "2026-06-18",
		Repeat:    "every week",
	})
	if err != nil {
		t.Fatalf("addQuickTask: %v", err)
	}

	if out.Created.Due != "2026-06-20" {
		t.Errorf("created due = %q, want 2026-06-20", out.Created.Due)
	}
	if out.Created.Scheduled != "2026-06-18" {
		t.Errorf("created scheduled = %q, want 2026-06-18", out.Created.Scheduled)
	}
	if out.Created.Recurrence != "every week" {
		t.Errorf("created recurrence = %q, want every week", out.Created.Recurrence)
	}

	// Confirm the recurrence + due actually landed on disk via the canonical
	// markdown parser.
	all, err := vault.ReadTasks(out.Created.File)
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("read %d tasks, want 1", len(all))
	}
	disk := all[0]
	if disk.Recurrence != "every week" {
		t.Errorf("disk recurrence = %q, want every week", disk.Recurrence)
	}
	if disk.Due == nil || disk.Due.Format("2006-01-02") != "2026-06-20" {
		t.Errorf("disk due = %v, want 2026-06-20", disk.Due)
	}
}

func TestQuickTaskEmptyTextErrors(t *testing.T) {
	tasks := service.TaskService{TaskFilePath: filepath.Join(t.TempDir(), "inbox.md")}
	if _, err := addQuickTask(tasks, quickTaskInput{Text: "   "}); err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestQuickTaskInvalidDueErrors(t *testing.T) {
	tasks := service.TaskService{TaskFilePath: filepath.Join(t.TempDir(), "inbox.md")}
	if _, err := addQuickTask(tasks, quickTaskInput{Text: "x", Due: "20-06-2026"}); err == nil {
		t.Fatal("expected error for invalid due date")
	}
}

func TestQuickTaskProjectRouting(t *testing.T) {
	tmp := t.TempDir()
	tasks := service.TaskService{
		TaskFilePath: filepath.Join(tmp, "inbox.md"),
		TasksDir:     tmp,
	}

	out, err := addQuickTask(tasks, quickTaskInput{Text: "client work", Project: "acme"})
	if err != nil {
		t.Fatalf("addQuickTask: %v", err)
	}
	// A project task routes to its own project file.
	if filepath.Base(out.Created.File) != "acme.md" {
		t.Errorf("created file = %s, want acme.md", out.Created.File)
	}
	// And it appears in the aggregated open list.
	var found bool
	for _, ot := range out.OpenTasks {
		if ot.Project == "acme" {
			found = true
		}
	}
	if !found {
		t.Errorf("project task not in open list: %+v", out.OpenTasks)
	}
}

func TestQuickTaskRegisteredMutating(t *testing.T) {
	r := tools.NewRegistry()
	tmp := t.TempDir()
	tasks := service.TaskService{TaskFilePath: filepath.Join(tmp, "inbox.md"), TasksDir: tmp}
	if err := RegisterQuickTask(r, tasks); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := r.Lookup(QuickTaskToolName)
	if !ok {
		t.Fatal("tool not registered")
	}
	if tool.Source.Kind != tools.SourceSkill {
		t.Fatalf("source = %v, want skill", tool.Source.Kind)
	}
	if !tool.Mutating {
		t.Fatal("quick-task must be Mutating so the policy gate confirms non-cli callers")
	}

	raw, err := tools.Execute(context.Background(), r, QuickTaskToolName, json.RawMessage(`{"text":"via registry"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out quickTaskOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Created.Text != "via registry" || out.OpenCount != 1 {
		t.Fatalf("unexpected output: %+v", out)
	}
}
