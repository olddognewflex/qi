package service

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"qi/internal/domain"
	"qi/internal/vault"
)

func FuzzyMatch(query string, tasks []domain.Task) []domain.Task {
	if query == "" {
		return tasks
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]domain.Task, 0)
	for _, t := range tasks {
		if strings.Contains(strings.ToLower(t.Text), q) {
			out = append(out, t)
		}
	}
	return out
}

// projectFileName maps a project tag to its task file name.
// Any "/" in the project is replaced with "-" for filesystem safety.
// e.g. "work/clientA" -> "work-clientA.md"
func projectFileName(project string) string {
	flat := strings.ReplaceAll(project, "/", "-")
	return flat + ".md"
}

// isSyncConflict reports whether a filename should be skipped during aggregation.
// Obsidian Sync marks conflicting copies with "sync-conflict" or "conflicted copy"
// in the filename.
func isSyncConflict(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "sync-conflict") || strings.Contains(lower, "conflicted copy")
}

type TaskService struct {
	TaskFilePath string
	TasksDir     string
}

type AddTaskInput struct {
	Text      string
	Project   string
	Due       *time.Time
	Scheduled *time.Time
}

func (s TaskService) AddTask(input AddTaskInput) error {
	task := domain.Task{
		ID:        vault.MintID(),
		Text:      strings.TrimSpace(input.Text),
		Project:   strings.TrimSpace(input.Project),
		Due:       input.Due,
		Scheduled: input.Scheduled,
	}

	path := s.TaskFilePath
	if task.Project != "" && s.TasksDir != "" {
		path = filepath.Join(s.TasksDir, projectFileName(task.Project))
	}
	return vault.AppendTask(path, task)
}

func (s TaskService) CompleteTask(task domain.Task) error {
	now := time.Now()
	task.Completed = true
	task.CompletedAt = &now
	return vault.UpdateTaskLine(task.FilePath, task.LineNumber, task)
}

// ListAllTasks aggregates tasks from every *.md file in TasksDir.
// Falls back to reading TaskFilePath when TasksDir is empty.
// Skips sync-conflict files.
func (s TaskService) ListAllTasks() ([]domain.Task, error) {
	if s.TasksDir == "" {
		return vault.ReadTasks(s.TaskFilePath)
	}

	entries, err := os.ReadDir(s.TasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []domain.Task{}, nil
		}
		return nil, err
	}

	var all []domain.Task
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		if isSyncConflict(name) {
			continue
		}
		tasks, err := vault.ReadTasks(filepath.Join(s.TasksDir, name))
		if err != nil {
			return nil, err
		}
		all = append(all, tasks...)
	}
	return all, nil
}

func (s TaskService) ListOpenTasks() ([]domain.Task, error) {
	tasks, err := s.ListAllTasks()
	if err != nil {
		return nil, err
	}
	out := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if !task.Completed {
			out = append(out, task)
		}
	}
	return out, nil
}
