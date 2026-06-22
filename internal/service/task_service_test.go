package service

import (
	"os"
	"path/filepath"
	"regexp"
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
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	taskFile := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: taskFile, TasksDir: tasksDir}

	if err := svc.AddTask(AddTaskInput{Text: "Fix qi parser"}); err != nil {
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

// TestCompleteTask_RecurringSpawnsNext verifies that completing a recurring,
// dated task marks the original done (keeping its 🔁) and appends a fresh open
// instance with the date advanced by the rule and a new id.
func TestCompleteTask_RecurringSpawnsNext(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	taskFile := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: taskFile, TasksDir: tasksDir}

	due := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	if err := svc.AddTask(AddTaskInput{Text: "water plants", Due: &due, Recurrence: "every week"}); err != nil {
		t.Fatalf("add task: %v", err)
	}

	open, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 open task, got %d", len(open))
	}
	original := open[0]

	if err := svc.CompleteTask(original); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	all, err := vault.ReadTasks(taskFile)
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 tasks after completing a recurring one, got %d", len(all))
	}

	var done, spawned *domain.Task
	for i := range all {
		if all[i].Completed {
			done = &all[i]
		} else {
			spawned = &all[i]
		}
	}
	if done == nil || spawned == nil {
		t.Fatalf("expected one completed + one open task, got %+v", all)
	}

	// The completed line keeps its recurrence marker (Layer A round-trip).
	if done.Recurrence != "every week" {
		t.Errorf("completed task lost recurrence: %q", done.Recurrence)
	}

	// The spawned instance: same text/recurrence, due advanced +1 week, new id.
	if spawned.Text != "water plants" {
		t.Errorf("spawned text = %q, want %q", spawned.Text, "water plants")
	}
	if spawned.Recurrence != "every week" {
		t.Errorf("spawned recurrence = %q, want %q", spawned.Recurrence, "every week")
	}
	if spawned.Due == nil || spawned.Due.Format("2006-01-02") != "2026-06-19" {
		t.Errorf("spawned due = %v, want 2026-06-19", spawned.Due)
	}
	if spawned.ID == "" || spawned.ID == original.ID {
		t.Errorf("spawned id = %q, want a fresh id (original %q)", spawned.ID, original.ID)
	}
}

// TestCompleteTask_NonRecurringNoSpawn guards that a plain task does not spawn a
// follow-up when completed.
func TestCompleteTask_NonRecurringNoSpawn(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	taskFile := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: taskFile, TasksDir: tasksDir}

	due := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	if err := svc.AddTask(AddTaskInput{Text: "one-off", Due: &due}); err != nil {
		t.Fatalf("add task: %v", err)
	}
	open, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := svc.CompleteTask(open[0]); err != nil {
		t.Fatalf("complete: %v", err)
	}
	all, err := vault.ReadTasks(taskFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 task (no spawn), got %d", len(all))
	}
}

// TestRescheduleTask sets a task's scheduled date and confirms it round-trips
// to the vault while leaving the due date untouched.
func TestRescheduleTask(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	taskFile := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: taskFile, TasksDir: tasksDir}

	due := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	if err := svc.AddTask(AddTaskInput{Text: "ship it", Due: &due}); err != nil {
		t.Fatalf("add task: %v", err)
	}

	open, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("want 1 open task, got %d", len(open))
	}

	when := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	if err := svc.RescheduleTask(open[0], &when); err != nil {
		t.Fatalf("reschedule: %v", err)
	}

	all, err := vault.ReadTasks(taskFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 task, got %d", len(all))
	}
	if all[0].Scheduled == nil || all[0].Scheduled.Format("2006-01-02") != "2026-06-25" {
		t.Fatalf("scheduled = %v, want 2026-06-25", all[0].Scheduled)
	}
	if all[0].Due == nil || all[0].Due.Format("2006-01-02") != "2026-06-30" {
		t.Fatalf("due should be untouched, got %v", all[0].Due)
	}
}

// TestRescheduleTask_Clear: a nil date clears the scheduled marker.
func TestRescheduleTask_Clear(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	taskFile := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: taskFile, TasksDir: tasksDir}

	sched := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	if err := svc.AddTask(AddTaskInput{Text: "later", Scheduled: &sched}); err != nil {
		t.Fatalf("add: %v", err)
	}
	open, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := svc.RescheduleTask(open[0], nil); err != nil {
		t.Fatalf("reschedule clear: %v", err)
	}
	all, err := vault.ReadTasks(taskFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if all[0].Scheduled != nil {
		t.Fatalf("scheduled should be cleared, got %v", all[0].Scheduled)
	}
}

func TestAddAndListOpenTasks(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	taskFile := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: taskFile, TasksDir: tasksDir}

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

// TestAddTask_StampsID: AddTask must persist a well-formed ^qi- block-ref ID
// on every new task so it is addressable by ID from first write.
func TestAddTask_StampsID(t *testing.T) {
	idRe := regexp.MustCompile(`^qi-[0-9a-f]{8}$`)
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	taskFile := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: taskFile, TasksDir: tasksDir}

	if err := svc.AddTask(AddTaskInput{Text: "New addressable task", Project: "qi"}); err != nil {
		t.Fatalf("add task: %v", err)
	}

	// Task with project "qi" goes to tasksDir/qi.md, not inbox.
	tasks, err := vault.ReadTasks(filepath.Join(tasksDir, "qi.md"))
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	id := tasks[0].ID
	if id == "" {
		t.Fatal("AddTask did not stamp an ID on the task")
	}
	if !idRe.MatchString(id) {
		t.Fatalf("ID %q does not match qi-[0-9a-f]{8}", id)
	}
}

// TestAddTask_RoutesToProjectFile: tagged tasks go to <tasksDir>/<project>.md;
// untagged tasks go to the inbox (TaskFilePath).
func TestAddTask_RoutesToProjectFile(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	inbox := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: inbox, TasksDir: tasksDir}

	if err := svc.AddTask(AddTaskInput{Text: "Tagged task", Project: "foo"}); err != nil {
		t.Fatalf("add tagged: %v", err)
	}
	if err := svc.AddTask(AddTaskInput{Text: "Untagged task"}); err != nil {
		t.Fatalf("add untagged: %v", err)
	}

	// Tagged task must be in foo.md, not inbox.
	fooTasks, err := vault.ReadTasks(filepath.Join(tasksDir, "foo.md"))
	if err != nil {
		t.Fatalf("read foo.md: %v", err)
	}
	if len(fooTasks) != 1 {
		t.Fatalf("foo.md: want 1 task, got %d", len(fooTasks))
	}
	if fooTasks[0].Text != "Tagged task #foo" {
		t.Fatalf("foo.md text: %q", fooTasks[0].Text)
	}

	// Untagged task must be in inbox.md only.
	inboxTasks, err := vault.ReadTasks(inbox)
	if err != nil {
		t.Fatalf("read inbox.md: %v", err)
	}
	if len(inboxTasks) != 1 {
		t.Fatalf("inbox.md: want 1 task, got %d", len(inboxTasks))
	}
	if inboxTasks[0].Text != "Untagged task" {
		t.Fatalf("inbox.md text: %q", inboxTasks[0].Text)
	}
}

// TestAddTask_NestedProjectFlattened: "work/clientA" maps to "work-clientA.md".
func TestAddTask_NestedProjectFlattened(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	inbox := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: inbox, TasksDir: tasksDir}

	if err := svc.AddTask(AddTaskInput{Text: "Client work", Project: "work/clientA"}); err != nil {
		t.Fatalf("add task: %v", err)
	}

	expected := filepath.Join(tasksDir, "work-clientA.md")
	tasks, err := vault.ReadTasks(expected)
	if err != nil {
		t.Fatalf("read work-clientA.md: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(tasks))
	}
}

// TestListAllTasks_AggregatesFiles: ListAllTasks spans multiple project files.
func TestListAllTasks_AggregatesFiles(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	inbox := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: inbox, TasksDir: tasksDir}

	if err := svc.AddTask(AddTaskInput{Text: "Task alpha", Project: "alpha"}); err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	if err := svc.AddTask(AddTaskInput{Text: "Task beta", Project: "beta"}); err != nil {
		t.Fatalf("add beta: %v", err)
	}
	if err := svc.AddTask(AddTaskInput{Text: "Inbox task"}); err != nil {
		t.Fatalf("add inbox: %v", err)
	}

	all, err := svc.ListAllTasks()
	if err != nil {
		t.Fatalf("ListAllTasks: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 tasks across files, got %d", len(all))
	}

	open, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("ListOpenTasks: %v", err)
	}
	if len(open) != 3 {
		t.Fatalf("want 3 open tasks, got %d", len(open))
	}
}

// TestDone_HitsCorrectFile: completing a task from the aggregate updates the
// file it lives in, not the inbox.
func TestDone_HitsCorrectFile(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	inbox := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: inbox, TasksDir: tasksDir}

	if err := svc.AddTask(AddTaskInput{Text: "Do the thing", Project: "foo"}); err != nil {
		t.Fatalf("add task: %v", err)
	}

	open, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list open: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("want 1 open task, got %d", len(open))
	}

	// Confirm the task's FilePath is foo.md, not inbox.md.
	task := open[0]
	if task.FilePath != filepath.Join(tasksDir, "foo.md") {
		t.Fatalf("FilePath: got %q, want foo.md", task.FilePath)
	}

	if err := svc.CompleteTask(task); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	// inbox.md must be empty (or nonexistent).
	inboxTasks, err := vault.ReadTasks(inbox)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(inboxTasks) != 0 {
		t.Fatalf("inbox should be empty, got %d tasks", len(inboxTasks))
	}

	// foo.md must show the task as completed.
	fooTasks, err := vault.ReadTasks(filepath.Join(tasksDir, "foo.md"))
	if err != nil {
		t.Fatalf("read foo.md: %v", err)
	}
	if len(fooTasks) != 1 || !fooTasks[0].Completed {
		t.Fatalf("foo.md: task not completed, tasks=%v", fooTasks)
	}
}

// TestListAllTasks_SkipsSyncConflicts: *.sync-conflict-* files are ignored.
func TestListAllTasks_SkipsSyncConflicts(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	inbox := filepath.Join(tasksDir, "inbox.md")
	svc := TaskService{TaskFilePath: inbox, TasksDir: tasksDir}

	// Write a real task to inbox.md.
	if err := svc.AddTask(AddTaskInput{Text: "Real task"}); err != nil {
		t.Fatalf("add task: %v", err)
	}

	// Write a conflict file directly (bypassing AddTask).
	conflictPath := filepath.Join(tasksDir, "foo.sync-conflict-20260101-120000.md")
	if err := os.WriteFile(conflictPath, []byte("- [ ] Conflict task\n"), 0o644); err != nil {
		t.Fatalf("write conflict file: %v", err)
	}

	all, err := svc.ListAllTasks()
	if err != nil {
		t.Fatalf("ListAllTasks: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 task (conflict file skipped), got %d", len(all))
	}
	if all[0].Text != "Real task" {
		t.Fatalf("unexpected task: %q", all[0].Text)
	}
}
