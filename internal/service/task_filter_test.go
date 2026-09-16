package service

import (
	"os"
	"path/filepath"
	"strings"
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

func TestListTasks_ActionableOn_keeps_only_tasks_without_future_dates(t *testing.T) {
	// Given
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.Local)
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	svc := TaskService{TaskFilePath: filepath.Join(tasksDir, "inbox.md"), TasksDir: tasksDir}
	yesterday := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	tomorrow := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	inputs := []AddTaskInput{
		{Text: "undated"},
		{Text: "due yesterday", Due: &yesterday},
		{Text: "scheduled today", Scheduled: &today},
		{Text: "due and scheduled today", Due: &today, Scheduled: &today},
		{Text: "completed due today", Due: &today},
		{Text: "due tomorrow", Due: &tomorrow},
		{Text: "scheduled tomorrow", Scheduled: &tomorrow},
		{Text: "due today but scheduled tomorrow", Due: &today, Scheduled: &tomorrow},
		{Text: "scheduled today but due tomorrow", Due: &tomorrow, Scheduled: &today},
	}
	for _, input := range inputs {
		if err := svc.AddTask(input); err != nil {
			t.Fatalf("add %q: %v", input.Text, err)
		}
	}
	open, err := svc.ListOpenTasks()
	if err != nil {
		t.Fatalf("list before complete: %v", err)
	}
	for _, task := range open {
		if task.Text == "completed due today" {
			if err := svc.CompleteTask(task); err != nil {
				t.Fatalf("complete: %v", err)
			}
		}
	}

	// When
	got, err := svc.ListTasks(TaskFilter{Status: "all", ActionableOn: &today, Now: now})
	// Then
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 actionable tasks, got %d: %v", len(got), got)
	}
	for i, want := range []string{"undated", "due yesterday", "scheduled today", "due and scheduled today"} {
		if got[i].Text != want {
			t.Errorf("task %d = %q, want %q", i, got[i].Text, want)
		}
	}
}

func TestListTasks_ActionableOn_sorts_priority_descending(t *testing.T) {
	// Given
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	svc := TaskService{TaskFilePath: filepath.Join(tasksDir, "inbox.md"), TasksDir: tasksDir}
	oversizedPriority := strings.Repeat("9", maxNumericPriorityLength+1)
	for _, text := range []string{
		"low [priority:: 1]",
		"scientific text [priority:: 1e3]",
		"high first [priority:: 10]",
		"unprioritized",
		"alpha text [priority:: alpha]",
		"oversized text [priority:: " + oversizedPriority + "]",
		"infinity text [priority:: infinity]",
		"nan text [priority:: nan]",
		"high second [priority:: 10]",
		"medium [priority:: 2]",
	} {
		if err := svc.AddTask(AddTaskInput{Text: text}); err != nil {
			t.Fatalf("add %q: %v", text, err)
		}
	}
	cutoff := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)

	// When
	got, err := svc.ListTasks(TaskFilter{ActionableOn: &cutoff})
	// Then
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	for i, want := range []string{
		"nan text [priority:: nan]",
		"infinity text [priority:: infinity]",
		"alpha text [priority:: alpha]",
		"oversized text [priority:: " + oversizedPriority + "]",
		"scientific text [priority:: 1e3]",
		"high first [priority:: 10]",
		"high second [priority:: 10]",
		"medium [priority:: 2]",
		"low [priority:: 1]",
		"unprioritized",
	} {
		if got[i].Text != want {
			t.Errorf("task %d = %q, want %q", i, got[i].Text, want)
		}
	}
}

func TestListTasks_ActionableOn_sorts_recurring_tasks_by_following_priority(t *testing.T) {
	// Given
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "- [ ] Low recurring 🔁 every week [priority:: 2]\n" +
		"- [ ] High recurring 🔁 every week [priority:: 10]\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "inbox.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write tasks: %v", err)
	}
	svc := TaskService{TaskFilePath: filepath.Join(tasksDir, "inbox.md"), TasksDir: tasksDir}
	cutoff := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)

	// When
	got, err := svc.ListTasks(TaskFilter{ActionableOn: &cutoff})
	// Then
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 2 || got[0].Text != "High recurring [priority:: 10]" || got[1].Text != "Low recurring [priority:: 2]" {
		t.Fatalf("unexpected recurring priority order: %v", got)
	}
}
