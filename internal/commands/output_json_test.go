package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/domain"
)

func TestDateJSON(t *testing.T) {
	if got := dateJSON(nil); got != "" {
		t.Errorf("dateJSON(nil) = %q, want empty", got)
	}
	d := time.Date(2026, 7, 20, 13, 45, 0, 0, time.UTC)
	if got := dateJSON(&d); got != "2026-07-20" {
		t.Errorf("dateJSON = %q, want 2026-07-20", got)
	}
}

func TestTasksToJSON(t *testing.T) {
	due := time.Date(2026, 7, 21, 0, 0, 0, 0, time.Local)
	sched := time.Date(2026, 7, 20, 0, 0, 0, 0, time.Local)
	done := time.Date(2026, 7, 19, 0, 0, 0, 0, time.Local)
	tasks := []domain.Task{
		{
			ID: "qi-abc", Text: "write tests", Project: "qi",
			Tags: []string{"qi", "urgent"}, Due: &due, Scheduled: &sched,
			Recurrence: "every week", ParentID: "qi-parent", Priority: "high",
			Completed: true, CompletedAt: &done,
			// Internal fields must NOT leak into the DTO.
			FilePath: "/vault/10-tasks/inbox.md", LineNumber: 42,
		},
	}
	out := tasksToJSON(tasks)
	if len(out) != 1 {
		t.Fatalf("want 1 task, got %d", len(out))
	}
	got := out[0]
	if got.ID != "qi-abc" || got.Text != "write tests" || got.Project != "qi" {
		t.Errorf("core fields wrong: %+v", got)
	}
	if got.Due != "2026-07-21" || got.Scheduled != "2026-07-20" || got.CompletedAt != "2026-07-19" {
		t.Errorf("date fields wrong: %+v", got)
	}
	if got.Recurrence != "every week" || got.ParentID != "qi-parent" || got.Priority != "high" || !got.Completed {
		t.Errorf("meta fields wrong: %+v", got)
	}

	// FilePath/LineNumber must not appear on the wire.
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leaked := range []string{"FilePath", "file_path", "LineNumber", "line_number"} {
		if strings.Contains(string(b), leaked) {
			t.Errorf("internal field %q leaked into JSON: %s", leaked, b)
		}
	}
}

func TestTasksToJSONEmptyEncodesAsArray(t *testing.T) {
	assertEncodesAsArray(t, tasksToJSON(nil))
}

func TestEventsToJSON(t *testing.T) {
	start := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	events := []domain.Event{
		{ID: "e1", Source: "local", Title: "standup", Start: start, End: end, Project: "qi"},
		// Zero-duration: End must be omitted.
		{ID: "e2", Source: "ics", Title: "all day", Start: start},
	}
	out := eventsToJSON(events)
	if len(out) != 2 {
		t.Fatalf("want 2 events, got %d", len(out))
	}
	if out[0].Start != start.Format(time.RFC3339) || out[0].End != end.Format(time.RFC3339) {
		t.Errorf("event[0] times wrong: %+v", out[0])
	}
	if out[1].End != "" {
		t.Errorf("zero-duration event should omit End, got %q", out[1].End)
	}

	b, _ := json.Marshal(out[1])
	if strings.Contains(string(b), "\"end\"") {
		t.Errorf("omitempty End should not appear for zero-duration event: %s", b)
	}
}

func TestEventsToJSONEmptyEncodesAsArray(t *testing.T) {
	assertEncodesAsArray(t, eventsToJSON(nil))
}

func TestPrintJSONWritesToCommandStdout(t *testing.T) {
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := printJSON(cmd, []searchResultJSON{{Kind: "note", Path: "20-notes/a.md", Match: "hit", Rank: 0.87}}); err != nil {
		t.Fatalf("printJSON: %v", err)
	}
	var back []searchResultJSON
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, buf.String())
	}
	if len(back) != 1 || back[0].Path != "20-notes/a.md" || back[0].Rank != 0.87 {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}

// assertEncodesAsArray fails if v marshals to JSON null rather than an empty
// array — the invariant that keeps `--json` on an empty result emit `[]`.
func assertEncodesAsArray(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("empty result should encode as [], got %s", b)
	}
}
