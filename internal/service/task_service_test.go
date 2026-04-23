package service

import (
	"path/filepath"
	"testing"
	"time"

	"qi/internal/domain"
	"qi/internal/vault"
)

func TestFuzzyMatch(t *testing.T) {
	tasks := []domain.Task{
		{Text: "Fix CDK bootstrap #builderhq"},
		{Text: "Fix qi parser #qi"},
		{Text: "Write docs"},
	}

	got := FuzzyMatch("fix", tasks)
	if len(got) != 2 {
		t.Fatalf("got %d tasks, want 2", len(got))
	}

	got = FuzzyMatch("FIX", tasks)
	if len(got) != 2 {
		t.Fatalf("case-insensitive: got %d tasks, want 2", len(got))
	}

	got = FuzzyMatch("qi", tasks)
	if len(got) != 1 || got[0].Text != "Fix qi parser #qi" {
		t.Fatalf("single match: got %v", got)
	}

	got = FuzzyMatch("", tasks)
	if len(got) != 3 {
		t.Fatalf("empty query should return all, got %d", len(got))
	}

	got = FuzzyMatch("xyzzy", tasks)
	if len(got) != 0 {
		t.Fatalf("no match expected, got %d", len(got))
	}
}

func TestCompleteTask(t *testing.T) {
	taskFile := filepath.Join(t.TempDir(), "tasks.md")
	svc := TaskService{TaskFilePath: taskFile}

	if err := svc.AddTask(AddTaskInput{Text: "Fix qi parser", Project: "qi"}); err != nil {
		t.Fatalf("add task: %v", err)
	}

	open, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 open task, got %d", len(open))
	}

	if err := svc.CompleteTask(open[0]); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	open, err = svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list after complete: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("expected 0 open tasks after complete, got %d", len(open))
	}

	all, err := vault.ReadTasks(taskFile)
	if err != nil {
		t.Fatalf("read all tasks: %v", err)
	}
	if len(all) != 1 || !all[0].Completed {
		t.Fatalf("task not marked completed in vault")
	}
}

func TestAddAndListOpenTasks(t *testing.T) {
	taskFile := filepath.Join(t.TempDir(), "10-tasks", "inbox.md")
	svc := TaskService{TaskFilePath: taskFile}

	due := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	if err := svc.AddTask(AddTaskInput{
		Text:    "Fix qi parser",
		Project: "qi",
		Due:     &due,
	}); err != nil {
		t.Fatalf("add task: %v", err)
	}

	tasks, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Text != "Fix qi parser #qi" {
		t.Fatalf("unexpected text: %q", tasks[0].Text)
	}
}
