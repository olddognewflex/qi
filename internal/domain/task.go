package domain

import "time"

// Task is the strict internal representation of a markdown task line.
type Task struct {
	ID        string
	Text      string
	Project   string
	Tags      []string
	Due       *time.Time
	Scheduled *time.Time
	// Recurrence is the raw Obsidian Tasks rule text WITHOUT the 🔁 marker
	// (e.g. "every week" or "every 2 weeks when done"). Empty = non-recurring.
	Recurrence string
	// ParentID links a subtask to its parent task's qi block-ref id (e.g.
	// "qi-1a2b3c4d"). Stored in markdown as a Dataview inline field
	// [parent:: qi-…] on the child line. Empty = top-level task. See
	// docs/subtasks-design.md.
	ParentID    string
	Priority    string
	Completed   bool
	CompletedAt *time.Time

	FilePath   string
	LineNumber int
}
