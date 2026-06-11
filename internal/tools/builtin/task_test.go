package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"qi/internal/service"
	"qi/internal/tools"
)

func newTaskSvc(t *testing.T) service.TaskService {
	t.Helper()
	dir := t.TempDir()
	return service.TaskService{
		TaskFilePath: filepath.Join(dir, "inbox.md"),
		TasksDir:     dir,
	}
}

func TestTaskAddRoundtrip(t *testing.T) {
	svc := newTaskSvc(t)
	r := tools.NewRegistry()
	if err := RegisterTaskAdd(r, svc); err != nil {
		t.Fatalf("register: %v", err)
	}

	got, ok := r.Lookup(TaskAddToolName)
	if !ok {
		t.Fatalf("lookup miss for %s", TaskAddToolName)
	}
	if !got.Mutating {
		t.Fatal("task.add must be marked mutating")
	}

	params, _ := json.Marshal(taskAddParams{Text: "Write the report", Project: "work", Due: "2026-06-20"})
	out, err := tools.Execute(context.Background(), r, TaskAddToolName, params)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res taskResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.ID == "" {
		t.Fatal("expected minted task id")
	}
	if res.Project != "work" || res.Due != "2026-06-20" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// The task is now readable back through the service.
	open, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 task, got %d", len(open))
	}
}

func TestTaskAddRejectsEmptyText(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterTaskAdd(r, newTaskSvc(t)); err != nil {
		t.Fatalf("register: %v", err)
	}
	params, _ := json.Marshal(taskAddParams{Text: "   "})
	if _, err := tools.Execute(context.Background(), r, TaskAddToolName, params); err == nil {
		t.Fatal("expected empty-text error")
	}
}

func TestTaskAddRejectsBadDate(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterTaskAdd(r, newTaskSvc(t)); err != nil {
		t.Fatalf("register: %v", err)
	}
	params, _ := json.Marshal(taskAddParams{Text: "x", Due: "20-06-2026"})
	if _, err := tools.Execute(context.Background(), r, TaskAddToolName, params); err == nil {
		t.Fatal("expected bad-date error")
	}
}

func TestTaskListFiltersAndIsReadOnly(t *testing.T) {
	svc := newTaskSvc(t)
	for _, in := range []service.AddTaskInput{
		{Text: "Buy milk", Project: "home"},
		{Text: "Ship release", Project: "work"},
		{Text: "Write milk memo", Project: "work"},
	} {
		if _, err := svc.CreateTask(in); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	r := tools.NewRegistry()
	if err := RegisterTaskList(r, svc); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, _ := r.Lookup(TaskListToolName)
	if tool.Mutating {
		t.Fatal("task.list must be read-only")
	}

	// Project filter.
	params, _ := json.Marshal(taskListParams{Project: "work"})
	out, err := tools.Execute(context.Background(), r, TaskListToolName, params)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res taskListResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Tasks) != 2 {
		t.Fatalf("project filter: expected 2, got %d", len(res.Tasks))
	}

	// Project + query filter.
	params, _ = json.Marshal(taskListParams{Project: "work", Query: "milk"})
	out, _ = tools.Execute(context.Background(), r, TaskListToolName, params)
	_ = json.Unmarshal(out, &res)
	if len(res.Tasks) != 1 || res.Tasks[0].Text != "Write milk memo #work" {
		t.Fatalf("query filter: unexpected %+v", res.Tasks)
	}
}

func TestTaskListEmptyParams(t *testing.T) {
	svc := newTaskSvc(t)
	if _, err := svc.CreateTask(service.AddTaskInput{Text: "solo"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := tools.NewRegistry()
	if err := RegisterTaskList(r, svc); err != nil {
		t.Fatalf("register: %v", err)
	}
	// No params at all should default to open tasks.
	out, err := tools.Execute(context.Background(), r, TaskListToolName, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res taskListResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(res.Tasks))
	}
}
