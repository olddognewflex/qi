package service

import (
	"path/filepath"
	"testing"

	"qi/internal/vault"
)

// TestCreateTask_ProvidedID writes the supplied id as the ^qi- block ref.
func TestCreateTask_ProvidedID(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	svc := TaskService{TaskFilePath: filepath.Join(tasksDir, "inbox.md"), TasksDir: tasksDir}

	got, err := svc.CreateTask(AddTaskInput{Text: "Remote task", ID: "qi-deadbeef"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if got.ID != "qi-deadbeef" {
		t.Fatalf("id = %q, want qi-deadbeef", got.ID)
	}

	tasks, err := vault.ReadTasks(filepath.Join(tasksDir, "inbox.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "qi-deadbeef" {
		t.Fatalf("vault tasks = %+v", tasks)
	}
}

// TestCreateTask_Idempotent: the same id+text twice writes exactly one line.
func TestCreateTask_Idempotent(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	svc := TaskService{TaskFilePath: filepath.Join(tasksDir, "inbox.md"), TasksDir: tasksDir}

	in := AddTaskInput{Text: "Drain me", ID: "qi-00010203"}
	if _, err := svc.CreateTask(in); err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := svc.CreateTask(in)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if second.ID != "qi-00010203" {
		t.Fatalf("second id = %q, want unchanged", second.ID)
	}

	all, err := svc.ListAllTasks()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("idempotency broken: want 1 task, got %d", len(all))
	}
}

// TestCreateTask_IDCollision: same id, different text → second is re-minted and
// both tasks survive as two distinct lines with two distinct ids.
func TestCreateTask_IDCollision(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	svc := TaskService{TaskFilePath: filepath.Join(tasksDir, "inbox.md"), TasksDir: tasksDir}

	first, err := svc.CreateTask(AddTaskInput{Text: "Original", ID: "qi-cafebabe"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.CreateTask(AddTaskInput{Text: "Different task", ID: "qi-cafebabe"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if second.ID == "qi-cafebabe" {
		t.Fatal("collision not detected: second kept the colliding id")
	}
	if second.ID == first.ID {
		t.Fatal("re-minted id equals the original id")
	}

	all, err := svc.ListAllTasks()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 tasks after collision re-mint, got %d", len(all))
	}
}

// TestCreateTask_NoIDMints: empty id mints a fresh qi- id.
func TestCreateTask_NoIDMints(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	svc := TaskService{TaskFilePath: filepath.Join(tasksDir, "inbox.md"), TasksDir: tasksDir}

	got, err := svc.CreateTask(AddTaskInput{Text: "Local task"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if len(got.ID) != len("qi-")+8 || got.ID[:3] != "qi-" {
		t.Fatalf("minted id %q not in qi-xxxxxxxx form", got.ID)
	}
}
