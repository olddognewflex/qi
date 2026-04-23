package service

import (
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

type TaskService struct {
	TaskFilePath string
}

type AddTaskInput struct {
	Text    string
	Project string
	Due     *time.Time
}

func (s TaskService) AddTask(input AddTaskInput) error {
	task := domain.Task{
		Text:    strings.TrimSpace(input.Text),
		Project: strings.TrimSpace(input.Project),
		Due:     input.Due,
	}
	return vault.AppendTask(s.TaskFilePath, task)
}

func (s TaskService) CompleteTask(task domain.Task) error {
	now := time.Now()
	task.Completed = true
	task.CompletedAt = &now
	return vault.UpdateTaskLine(task.FilePath, task.LineNumber, task)
}

func (s TaskService) ListOpenTasks() ([]domain.Task, error) {
	tasks, err := vault.ReadTasks(s.TaskFilePath)
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
