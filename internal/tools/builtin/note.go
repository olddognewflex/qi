package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qi/internal/domain"
	"qi/internal/tools"
)

// NoteSearchToolName is the registered name of the note-search tool.
const NoteSearchToolName = "note.search"

// NoteSearcher runs a full-text query against the note index. It is satisfied
// by *index.Indexer; the indirection keeps this package free of the index/
// SQLite dependency and makes the tool testable with a fake.
type NoteSearcher interface {
	Search(query string) ([]domain.SearchResult, error)
}

var noteSearchSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "minLength": 1,
      "description": "FTS5 query against the note index. Whitespace-trimmed; must not be empty."
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`)

type noteSearchParams struct {
	Query string `json:"query"`
}

type noteSearchHit struct {
	Path  string  `json:"path"`
	Title string  `json:"title,omitempty"`
	Match string  `json:"match,omitempty"`
	Rank  float64 `json:"rank"`
}

type noteSearchResult struct {
	Results []noteSearchHit `json:"results"`
}

// RegisterNoteSearch binds note.search against the supplied searcher. Read-only:
// queries the derived SQLite FTS index and returns ranked note hits.
func RegisterNoteSearch(r *tools.Registry, searcher NoteSearcher) error {
	if searcher == nil {
		return fmt.Errorf("note.search: searcher is nil")
	}
	tool := tools.Tool{
		Name:        NoteSearchToolName,
		Version:     "1",
		Mutating:    false,
		Description: "Full-text search notes via the FTS index. Returns ranked path/title/snippet hits.",
		Schema:      noteSearchSchema,
	}
	handler := func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p noteSearchParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("note.search: parse params: %w", err)
		}
		if strings.TrimSpace(p.Query) == "" {
			return nil, fmt.Errorf("note.search: query is empty")
		}
		results, err := searcher.Search(p.Query)
		if err != nil {
			return nil, err
		}
		out := noteSearchResult{Results: make([]noteSearchHit, 0, len(results))}
		for _, res := range results {
			out.Results = append(out.Results, noteSearchHit{
				Path:  res.Note.Path,
				Title: res.Note.Title,
				Match: res.Match,
				Rank:  res.Rank,
			})
		}
		return json.Marshal(out)
	}
	return r.RegisterLocal(tool, handler)
}
