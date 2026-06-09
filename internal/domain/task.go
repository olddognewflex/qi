package domain

import "time"

// Task is the strict internal representation of a markdown task line.
type Task struct {
	ID          string
	Text        string
	Project     string
	Tags        []string
	Due         *time.Time
	Scheduled   *time.Time
	Priority    string
	Completed   bool
	CompletedAt *time.Time

	FilePath   string
	LineNumber int
}
