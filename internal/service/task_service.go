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
	ID        string // optional; if set, used instead of vault.MintID()
}

// AddTask creates a task with a freshly minted ID. It is a thin wrapper over
// CreateTask, kept for callers that don't need the created task back.
func (s TaskService) AddTask(input AddTaskInput) error {
	_, err := s.CreateTask(input)
	return err
}

// CreateTask creates a task and returns it. When input.ID is set it is treated
// as a provided (cloud-minted) id and the write is made idempotent on that id —
// the only dedup key the markdown carries (the ^qi-… block ref):
//
//   - id present in the vault with matching text → no-op, return the existing
//     task (safe re-drain after a dropped /ack).
//   - id present with DIFFERENT text → id collision; re-mint a fresh local id so
//     the new task is written rather than silently dropped.
//   - id empty → mint as usual.
//
// The returned task carries its FilePath so callers can report where it landed.
func (s TaskService) CreateTask(input AddTaskInput) (domain.Task, error) {
	text := strings.TrimSpace(input.Text)
	project := strings.TrimSpace(input.Project)

	if input.ID != "" {
		existing, found, err := s.findByID(input.ID)
		if err != nil {
			return domain.Task{}, err
		}
		if found {
			if taskTextMatches(existing, text) {
				// Already drained — no-op. Return the existing task unchanged.
				return existing, nil
			}
			// Id collision (same id, different task): drop the provided id and
			// fall through to MintID() so a fresh local id is used and both
			// tasks survive.
			input.ID = ""
		}
	}

	id := input.ID
	if id == "" {
		id = vault.MintID()
	}

	task := domain.Task{
		ID:        id,
		Text:      text,
		Project:   project,
		Due:       input.Due,
		Scheduled: input.Scheduled,
	}

	path := s.TaskFilePath
	if task.Project != "" && s.TasksDir != "" {
		path = filepath.Join(s.TasksDir, projectFileName(task.Project))
	}
	if err := vault.AppendTask(path, task); err != nil {
		return domain.Task{}, err
	}
	task.FilePath = path
	return task, nil
}

// taskTextMatches reports whether an existing vault task is "the same" as the
// trimmed candidate text, for the idempotency guard. ParseTaskLine leaves the
// project tag inline in Text (e.g. "Buy milk #home"), while the candidate is the
// bare text the task was created from ("Buy milk"). Compare against both the raw
// stored text and the stored text with its trailing project tag stripped, so a
// re-drained task is recognised as a no-op rather than a collision.
func taskTextMatches(existing domain.Task, text string) bool {
	stored := strings.TrimSpace(existing.Text)
	if stored == text {
		return true
	}
	if existing.Project != "" {
		bare := strings.TrimSpace(strings.TrimSuffix(stored, "#"+existing.Project))
		if bare == text {
			return true
		}
	}
	return false
}

// findByID scans every task in TasksDir for one carrying the given block-ref id.
// O(n) per lookup is fine off the hot path (drain only). The second return is
// false when no task carries the id.
func (s TaskService) findByID(id string) (domain.Task, bool, error) {
	if id == "" {
		return domain.Task{}, false, nil
	}
	all, err := s.ListAllTasks()
	if err != nil {
		return domain.Task{}, false, err
	}
	for _, t := range all {
		if t.ID == id {
			return t, true, nil
		}
	}
	return domain.Task{}, false, nil
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
