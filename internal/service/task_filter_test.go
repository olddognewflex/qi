package service

import (
	"path/filepath"
	"testing"
	"time"
)

// seedFilterTasks builds a small vault with tasks across projects, statuses, and
// dates for the ListTasks filter tests. now is the reference "today".
func seedFilterTasks(t *testing.T, now time.Time) TaskService {
	t.Helper()
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	svc := TaskService{TaskFilePath: filepath.Join(tasksDir, "inbox.md"), TasksDir: tasksDir}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	tomorrow := today.AddDate(0, 0, 1)

	add := func(text, project string, due *time.Time) {
		t.Helper()
		if err := svc.AddTask(AddTaskInput{Text: text, Project: project, Due: due}); err != nil {
			t.Fatalf("add %q: %v", text, err)
		}
	}

	add("work today task", "work", &today)       // #work, due today
	add("work overdue task", "work", &yesterday) // #work, due yesterday
	add("home future task", "home", &tomorrow)   // #home, due tomorrow
	add("work no-date task", "work", nil)        // #work, no date

	// A completed task in #work, due today.
	add("work done task", "work", &today)
	open, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list before complete: %v", err)
	}
	for _, task := range open {
		if task.Text == "work done task #work" {
			if err := svc.CompleteTask(task); err != nil {
				t.Fatalf("complete: %v", err)
			}
		}
	}
	return svc
}

func TestListTasks_DefaultIsOpen(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.Local)
	svc := seedFilterTasks(t, now)

	got, err := svc.ListTasks(TaskFilter{Now: now})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	// 4 open tasks (the completed one is excluded by default).
	if len(got) != 4 {
		t.Fatalf("want 4 open tasks, got %d: %v", len(got), got)
	}
	for _, task := range got {
		if task.Completed {
			t.Fatalf("default filter returned a completed task: %q", task.Text)
		}
	}
}

func TestListTasks_ProjectFilter(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.Local)
	svc := seedFilterTasks(t, now)

	got, err := svc.ListTasks(TaskFilter{Project: "home", Now: now})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 1 || got[0].Project != "home" {
		t.Fatalf("want 1 #home task, got %d: %v", len(got), got)
	}

	// Case-insensitive match.
	upper, err := svc.ListTasks(TaskFilter{Project: "WORK", Now: now})
	if err != nil {
		t.Fatalf("ListTasks upper: %v", err)
	}
	if len(upper) != 3 {
		t.Fatalf("want 3 open #work tasks, got %d", len(upper))
	}
}

func TestListTasks_StatusFilter(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.Local)
	svc := seedFilterTasks(t, now)

	done, err := svc.ListTasks(TaskFilter{Status: "done", Now: now})
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	if len(done) != 1 || !done[0].Completed {
		t.Fatalf("want 1 completed task, got %d: %v", len(done), done)
	}

	all, err := svc.ListTasks(TaskFilter{Status: "all", Now: now})
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("want 5 tasks total, got %d", len(all))
	}

	if _, err := svc.ListTasks(TaskFilter{Status: "bogus", Now: now}); err == nil {
		t.Fatalf("want error for invalid status, got nil")
	}
}

func TestListTasks_DateFilters(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.Local)
	svc := seedFilterTasks(t, now)

	today, err := svc.ListTasks(TaskFilter{Date: "today", Now: now})
	if err != nil {
		t.Fatalf("today: %v", err)
	}
	if len(today) != 1 || today[0].Text != "work today task #work" {
		t.Fatalf("want 1 due-today task, got %d: %v", len(today), today)
	}

	overdue, err := svc.ListTasks(TaskFilter{Date: "overdue", Now: now})
	if err != nil {
		t.Fatalf("overdue: %v", err)
	}
	if len(overdue) != 1 || overdue[0].Text != "work overdue task #work" {
		t.Fatalf("want 1 overdue task, got %d: %v", len(overdue), overdue)
	}

	abs, err := svc.ListTasks(TaskFilter{Date: "2026-06-23", Now: now})
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if len(abs) != 1 || abs[0].Text != "home future task #home" {
		t.Fatalf("want 1 task due 2026-06-23, got %d: %v", len(abs), abs)
	}

	if _, err := svc.ListTasks(TaskFilter{Date: "nonsense", Now: now}); err == nil {
		t.Fatalf("want error for invalid date, got nil")
	}
}

func TestListTasks_BeforeAfter(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.Local)
	svc := seedFilterTasks(t, now)
	today := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)

	before, err := svc.ListTasks(TaskFilter{Before: &today, Now: now})
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if len(before) != 1 || before[0].Text != "work overdue task #work" {
		t.Fatalf("want 1 task before today, got %d: %v", len(before), before)
	}

	after, err := svc.ListTasks(TaskFilter{After: &today, Now: now})
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if len(after) != 1 || after[0].Text != "home future task #home" {
		t.Fatalf("want 1 task after today, got %d: %v", len(after), after)
	}

	// Range: After yesterday AND Before tomorrow narrows to the single task
	// dated exactly today.
	yesterday := today.AddDate(0, 0, -1)
	tomorrow := today.AddDate(0, 0, 1)
	rng, err := svc.ListTasks(TaskFilter{After: &yesterday, Before: &tomorrow, Now: now})
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if len(rng) != 1 || rng[0].Text != "work today task #work" {
		t.Fatalf("want 1 in-range task, got %d: %v", len(rng), rng)
	}
}

func TestListTasks_ComposedAND(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.Local)
	svc := seedFilterTasks(t, now)

	// #work AND due today AND status all → the open today task (the done one is
	// due today too, but project+date+open narrows to one).
	got, err := svc.ListTasks(TaskFilter{Project: "work", Date: "today", Status: "open", Now: now})
	if err != nil {
		t.Fatalf("composed: %v", err)
	}
	if len(got) != 1 || got[0].Text != "work today task #work" {
		t.Fatalf("want 1 composed match, got %d: %v", len(got), got)
	}

	// #work AND due today AND status all → both the open and done today tasks.
	all, err := svc.ListTasks(TaskFilter{Project: "work", Date: "today", Status: "all", Now: now})
	if err != nil {
		t.Fatalf("composed all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 work-today tasks (open+done), got %d: %v", len(all), all)
	}
}
