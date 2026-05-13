package domain

import "time"

const EventSourceLocal = "local"

// Event is a calendar event from any provider (Google Calendar, local daily notes).
type Event struct {
	ID      string
	Source  string
	Title   string
	Start   time.Time
	End     time.Time
	Project string
}
