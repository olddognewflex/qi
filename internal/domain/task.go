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
	Recurrence  string
	Priority    string
	Completed   bool
	CompletedAt *time.Time

	FilePath   string
	LineNumber int
}
