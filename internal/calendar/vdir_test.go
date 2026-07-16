package calendar

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"qi/internal/domain"
)

// vdirRange covers the whole fixture window.
var (
	vdirFrom = utc(2026, 4, 6, 0, 0)
	vdirTo   = utc(2026, 4, 10, 0, 0)
)

func vdirEvents(t *testing.T, dir string) []domain.Event {
	t.Helper()
	p := VdirProvider{CalName: "work", Path: dir}
	events, err := p.Events(vdirFrom, vdirTo)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Start.Before(events[j].Start) })
	return events
}

func TestVdirProvider_Events(t *testing.T) {
	events := vdirEvents(t, filepath.Join("testdata", "vdir", "work"))

	// This is the end-to-end wiring check: files on disk -> expandCalendarEvents.
	// The list is exhaustive and order-checked, so it also pins that the overridden
	// slot (04-07 14:00 "Pairing") is replaced rather than duplicated and that the
	// cancelled slot (04-07 18:00 "Gym") is gone. Override *semantics* are covered
	// exhaustively against inline objects in expand_test.go.
	want := []struct {
		title string
		start time.Time
	}{
		{"Plain standup", utc(2026, 4, 6, 9, 0)},
		{"Daily sync", utc(2026, 4, 6, 11, 0)},
		{"Pairing", utc(2026, 4, 6, 14, 0)},
		{"Gym", utc(2026, 4, 6, 18, 0)},
		{"Daily sync", utc(2026, 4, 7, 11, 0)},
		{"Pairing (moved)", utc(2026, 4, 7, 16, 30)},
		{"Daily sync", utc(2026, 4, 8, 11, 0)},
		{"Pairing", utc(2026, 4, 8, 14, 0)},
		{"Gym", utc(2026, 4, 8, 18, 0)},
		{"Daily sync", utc(2026, 4, 9, 11, 0)},
	}

	if len(events) != len(want) {
		for _, e := range events {
			t.Logf("got %s %s", e.Start.UTC().Format(time.RFC3339), e.Title)
		}
		t.Fatalf("len(Events()) = %d, want %d", len(events), len(want))
	}
	for i, w := range want {
		if events[i].Title != w.title {
			t.Errorf("event[%d].Title = %q, want %q", i, events[i].Title, w.title)
		}
		if !events[i].Start.Equal(w.start) {
			t.Errorf("event[%d].Start = %s, want %s", i,
				events[i].Start.UTC().Format(time.RFC3339), w.start.Format(time.RFC3339))
		}
		if events[i].Source != "work" {
			t.Errorf("event[%d].Source = %q, want %q", i, events[i].Source, "work")
		}
	}
}

func TestVdirProvider_OccurrenceIDsUnique(t *testing.T) {
	events := vdirEvents(t, filepath.Join("testdata", "vdir", "work"))

	seen := map[string]bool{}
	for _, e := range events {
		if e.ID == "" {
			t.Errorf("event %q has empty ID", e.Title)
			continue
		}
		if seen[e.ID] {
			t.Errorf("duplicate occurrence ID %q", e.ID)
		}
		seen[e.ID] = true
	}

	// A non-recurring event keeps its bare UID; occurrences are start-qualified.
	if !seen["plain-1"] {
		t.Errorf("non-recurring event should keep bare UID, got IDs %v", keys(seen))
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestVdirProvider_SkipsMalformedAndTmp(t *testing.T) {
	events := vdirEvents(t, filepath.Join("testdata", "vdir", "work"))

	// malformed.ics must not be fatal; pending.ics.tmp must be invisible.
	if len(events) == 0 {
		t.Fatal("a malformed .ics sank the whole collection")
	}
	for _, e := range events {
		if e.Title == "Half-written event" {
			t.Errorf("a .tmp file was read: %q", e.Title)
		}
	}
}

func TestVdirProvider_MissingDir(t *testing.T) {
	p := VdirProvider{CalName: "gone", Path: filepath.Join(t.TempDir(), "nope")}
	if _, err := p.Events(vdirFrom, vdirTo); err == nil {
		t.Error("Events() on a missing collection should error")
	}
}

func TestVdirDisplayName(t *testing.T) {
	root := t.TempDir()

	tests := []struct {
		name     string
		contents *string // nil = no displayname file
		fallback string
		want     string
	}{
		{name: "honored", contents: strptr("Work Calendar\n"), fallback: "work", want: "Work Calendar"},
		{name: "trimmed", contents: strptr("  Spaced  \n"), fallback: "work", want: "Spaced"},
		{name: "empty falls back", contents: strptr("   \n"), fallback: "work", want: "work"},
		{name: "absent falls back", contents: nil, fallback: "personal", want: "personal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(root, tt.name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.contents != nil {
				if err := os.WriteFile(filepath.Join(dir, "displayname"), []byte(*tt.contents), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := vdirDisplayName(dir, tt.fallback); got != tt.want {
				t.Errorf("vdirDisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func strptr(s string) *string { return &s }

func TestDiscoverVdir(t *testing.T) {
	providers, err := DiscoverVdir(filepath.Join("testdata", "vdir"))
	if err != nil {
		t.Fatalf("DiscoverVdir() error = %v", err)
	}

	// "personal" has no displayname (falls back to the dir name); "work" has one.
	want := []string{"Work Calendar", "personal"}
	if len(providers) != len(want) {
		t.Fatalf("len(DiscoverVdir()) = %d, want %d", len(providers), len(want))
	}
	for i, w := range want {
		if got := providers[i].Name(); got != w {
			t.Errorf("provider[%d].Name() = %q, want %q", i, got, w)
		}
	}

	// The discovered collection must actually read its own directory.
	events, err := providers[1].Events(vdirFrom, vdirTo)
	if err != nil {
		t.Fatalf("personal Events() error = %v", err)
	}
	if len(events) != 1 || events[0].Title != "Lunch" {
		t.Errorf("personal events = %+v, want one \"Lunch\"", events)
	}
}

func TestDiscoverVdir_MissingRoot(t *testing.T) {
	if _, err := DiscoverVdir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("DiscoverVdir() on a missing root should error")
	}
}
