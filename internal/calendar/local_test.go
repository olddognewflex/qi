package calendar

import (
	"strings"
	"testing"
	"time"

	"qi/internal/domain"
)

func TestParseLine(t *testing.T) {
	day := time.Date(2026, 4, 26, 0, 0, 0, 0, time.Local)

	tests := []struct {
		line    string
		wantOK  bool
		title   string
		project string
		startH  int
		startM  int
		endH    int
		endM    int
	}{
		{
			line:   "- 09:00 Team standup",
			wantOK: true, title: "Team standup",
			startH: 9, startM: 0, endH: 10, endM: 0,
		},
		{
			line:   "- 09:30-10:30 Design review",
			wantOK: true, title: "Design review",
			startH: 9, startM: 30, endH: 10, endM: 30,
		},
		{
			line:    "- 14:00 Coffee with Jane #qi",
			wantOK:  true, title: "Coffee with Jane",
			project: "qi",
			startH:  14, startM: 0, endH: 15, endM: 0,
		},
		{
			line:   "Random text",
			wantOK: false,
		},
		{
			line:   "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		ev, ok := parseLine(tt.line, day)
		if ok != tt.wantOK {
			t.Errorf("parseLine(%q) ok=%v want %v", tt.line, ok, tt.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if ev.Title != tt.title {
			t.Errorf("parseLine(%q) title=%q want %q", tt.line, ev.Title, tt.title)
		}
		if ev.Project != tt.project {
			t.Errorf("parseLine(%q) project=%q want %q", tt.line, ev.Project, tt.project)
		}
		if ev.Start.Hour() != tt.startH || ev.Start.Minute() != tt.startM {
			t.Errorf("parseLine(%q) start=%02d:%02d want %02d:%02d", tt.line, ev.Start.Hour(), ev.Start.Minute(), tt.startH, tt.startM)
		}
		if ev.End.Hour() != tt.endH || ev.End.Minute() != tt.endM {
			t.Errorf("parseLine(%q) end=%02d:%02d want %02d:%02d", tt.line, ev.End.Hour(), ev.End.Minute(), tt.endH, tt.endM)
		}
		if ev.Source != domain.EventSourceLocal {
			t.Errorf("parseLine(%q) source=%q want %q", tt.line, ev.Source, domain.EventSourceLocal)
		}
	}
}

func TestParseEventsFromNote(t *testing.T) {
	day := time.Date(2026, 4, 26, 0, 0, 0, 0, time.Local)

	note := `# Daily Note

## Tasks
- [ ] Fix the parser

## Schedule
- 09:00 Standup
- 10:00-11:00 Planning session #qi
- 14:00 One-on-one #team

## Notes
Some notes here.
`

	events, err := parseEventsFromNote(strings.NewReader(note), day)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	if events[0].Title != "Standup" {
		t.Errorf("events[0].Title=%q want %q", events[0].Title, "Standup")
	}
	if events[1].Title != "Planning session" || events[1].Project != "qi" {
		t.Errorf("events[1]=%+v unexpected", events[1])
	}
	if events[2].Title != "One-on-one" || events[2].Project != "team" {
		t.Errorf("events[2]=%+v unexpected", events[2])
	}
}

func TestExtractProject(t *testing.T) {
	tests := []struct {
		input, wantTitle, wantProject string
	}{
		{"Team standup", "Team standup", ""},
		{"Coffee with Jane #qi", "Coffee with Jane", "qi"},
		{"#qi", "#qi", ""},
	}

	for _, tt := range tests {
		title, proj := extractProject(tt.input)
		if title != tt.wantTitle || proj != tt.wantProject {
			t.Errorf("extractProject(%q) = (%q,%q) want (%q,%q)", tt.input, title, proj, tt.wantTitle, tt.wantProject)
		}
	}
}
