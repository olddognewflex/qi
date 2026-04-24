package domain

import "time"

type Note struct {
	Path       string
	Title      string
	Tags       []string
	ModifiedAt time.Time
}

type SearchResult struct {
	Note  Note
	Match string
	Rank  float64
}
