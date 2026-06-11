package domain

import "time"

type Note struct {
	Path       string
	Title      string
	Tags       []string
	ModifiedAt time.Time
}

// Search result kinds, derived from a result's vault subdir at query time.
// The FTS index is a single table over all vault markdown; these label which
// part of the vault a hit came from (no schema column backs them).
const (
	SearchKindNote  = "note"
	SearchKindTask  = "task"
	SearchKindDaily = "daily"
	SearchKindInbox = "inbox"
	SearchKindOther = "other"
)

type SearchResult struct {
	Note  Note
	Match string
	Rank  float64
	// Kind labels which vault area the hit came from (see SearchKind* consts),
	// classified from Note.Path at query time.
	Kind string
}
