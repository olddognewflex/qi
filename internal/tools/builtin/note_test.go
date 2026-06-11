package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"qi/internal/domain"
	"qi/internal/tools"
)

type fakeSearcher struct {
	results []domain.SearchResult
	err     error
	gotQ    string
}

func (f *fakeSearcher) Search(query string) ([]domain.SearchResult, error) {
	f.gotQ = query
	return f.results, f.err
}

func TestNoteSearchRoundtrip(t *testing.T) {
	fs := &fakeSearcher{results: []domain.SearchResult{
		{Note: domain.Note{Path: "20-notes/a.md", Title: "Alpha"}, Match: "…alpha…", Rank: -1.2},
	}}
	r := tools.NewRegistry()
	if err := RegisterNoteSearch(r, fs); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := r.Lookup(NoteSearchToolName)
	if !ok {
		t.Fatalf("lookup miss for %s", NoteSearchToolName)
	}
	if tool.Mutating {
		t.Fatal("note.search must be read-only")
	}

	params, _ := json.Marshal(noteSearchParams{Query: "alpha"})
	out, err := tools.Execute(context.Background(), r, NoteSearchToolName, params)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if fs.gotQ != "alpha" {
		t.Fatalf("searcher got %q", fs.gotQ)
	}
	var res noteSearchResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Results) != 1 || res.Results[0].Title != "Alpha" {
		t.Fatalf("unexpected results: %+v", res.Results)
	}
}

func TestNoteSearchRejectsEmptyQuery(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterNoteSearch(r, &fakeSearcher{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	params, _ := json.Marshal(noteSearchParams{Query: "  "})
	if _, err := tools.Execute(context.Background(), r, NoteSearchToolName, params); err == nil {
		t.Fatal("expected empty-query error")
	}
}

func TestNoteSearchPropagatesError(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterNoteSearch(r, &fakeSearcher{err: errors.New("db down")}); err != nil {
		t.Fatalf("register: %v", err)
	}
	params, _ := json.Marshal(noteSearchParams{Query: "x"})
	if _, err := tools.Execute(context.Background(), r, NoteSearchToolName, params); err == nil {
		t.Fatal("expected propagated search error")
	}
}

func TestNoteSearchNilSearcher(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterNoteSearch(r, nil); err == nil {
		t.Fatal("expected error for nil searcher")
	}
}
