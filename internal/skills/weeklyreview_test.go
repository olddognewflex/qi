package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qi/internal/service"
	"qi/internal/tools"
)

// fixedDailyPath returns a dailyPathFor closure mapping each day to
// dir/YYYY-MM-DD.md, mirroring how cfg.DailyNotePath resolves a daily note.
func fixedDailyPath(dir string) func(time.Time) string {
	return func(d time.Time) string {
		return filepath.Join(dir, d.Format("2006-01-02")+".md")
	}
}

func TestWeeklyReviewAggregates(t *testing.T) {
	tmp := t.TempDir()
	tasksDir := filepath.Join(tmp, "10-tasks")
	inbox := filepath.Join(tmp, "00-inbox")
	archive := filepath.Join(inbox, "archive")
	dailyDir := filepath.Join(tmp, "30-daily")

	// Window: 2026-06-04 .. 2026-06-10 (end = 2026-06-10, days = 7).
	end := time.Date(2026, 6, 10, 12, 0, 0, 0, time.Local)

	// Tasks: two completed in-window, one completed out-of-window, one open.
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskLines := strings.Join([]string{
		"- [x] Ship weekly review #qi 📅 2026-06-09 ✅ 2026-06-09",
		"- [x] Fix the parser bug ✅ 2026-06-05",
		"- [x] Old completed thing ✅ 2026-05-30",
		"- [ ] Still open task #qi",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "inbox.md"), []byte(taskLines), 0o644); err != nil {
		t.Fatal(err)
	}

	// Captures: in-window inbox (2026-06-10, two on same day), out-of-window
	// inbox (2026-06-01), and one in-window archive capture (2026-06-07).
	writeDatedCapture(t, inbox, "2026-06-10-101010.md", "in window one")
	writeDatedCapture(t, inbox, "2026-06-10-111111.md", "in window two")
	writeDatedCapture(t, inbox, "2026-06-01-101010.md", "out of window")
	writeDatedCapture(t, archive, "2026-06-07-090000.md", "archived in window")

	// Daily highlights: an in-window day with a non-empty ## Logs section.
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	daily := "# 2026-06-08\n\n## Logs\nShipped the parser fix and paired on review.\n"
	if err := os.WriteFile(filepath.Join(dailyDir, "2026-06-08.md"), []byte(daily), 0o644); err != nil {
		t.Fatal(err)
	}
	// An in-window day with an empty Logs section must be excluded.
	emptyLogs := "# 2026-06-09\n\n## Logs\n\n"
	if err := os.WriteFile(filepath.Join(dailyDir, "2026-06-09.md"), []byte(emptyLogs), 0o644); err != nil {
		t.Fatal(err)
	}

	tasksSvc := service.TaskService{TasksDir: tasksDir}

	out, err := proposeWeeklyReview(tasksSvc, inbox, archive, fixedDailyPath(dailyDir), end, 7)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	if out.WeekStart != "2026-06-04" || out.WeekEnd != "2026-06-10" {
		t.Fatalf("window = %s..%s, want 2026-06-04..2026-06-10", out.WeekStart, out.WeekEnd)
	}

	// Completed: only the two in-window done tasks, sorted by done date then text.
	if out.CompletedN != 2 || len(out.Completed) != 2 {
		t.Fatalf("completed count = %d, want 2: %+v", out.CompletedN, out.Completed)
	}
	if out.Completed[0].Done != "2026-06-05" || !strings.Contains(out.Completed[0].Text, "Fix the parser bug") {
		t.Errorf("first completed = %+v, want parser bug @ 2026-06-05", out.Completed[0])
	}
	if out.Completed[1].Done != "2026-06-09" || !strings.Contains(out.Completed[1].Text, "Ship weekly review") {
		t.Errorf("second completed = %+v, want ship review @ 2026-06-09", out.Completed[1])
	}
	if out.Completed[1].Project != "qi" {
		t.Errorf("project = %q, want qi", out.Completed[1].Project)
	}

	// Captures: 2 (inbox 06-10) + 1 (archive 06-07) = 3, out-of-window excluded.
	if out.CaptureTotal != 3 {
		t.Fatalf("capture total = %d, want 3: %+v", out.CaptureTotal, out.CapturesByDay)
	}
	// Sorted ascending by date.
	if len(out.CapturesByDay) != 2 ||
		out.CapturesByDay[0].Date != "2026-06-07" || out.CapturesByDay[0].Count != 1 ||
		out.CapturesByDay[1].Date != "2026-06-10" || out.CapturesByDay[1].Count != 2 {
		t.Fatalf("captures by day = %+v", out.CapturesByDay)
	}

	// Highlights: only the day with a non-empty ## Logs.
	if len(out.Highlights) != 1 {
		t.Fatalf("highlights = %d, want 1: %+v", len(out.Highlights), out.Highlights)
	}
	if out.Highlights[0].Date != "2026-06-08" || !strings.Contains(out.Highlights[0].Logs, "parser fix") {
		t.Errorf("highlight = %+v", out.Highlights[0])
	}

	// Proposed title + body are non-empty and carry the salient content.
	if out.ProposedTitle == "" || out.ProposedBody == "" {
		t.Fatal("proposed title/body must be non-empty")
	}
	if !strings.Contains(out.ProposedBody, "Ship weekly review") {
		t.Errorf("body missing completed task: %s", out.ProposedBody)
	}
	if !strings.Contains(out.ProposedBody, "parser fix") {
		t.Errorf("body missing highlight: %s", out.ProposedBody)
	}
	if !strings.Contains(out.ProposedBody, "## Captures (3)") {
		t.Errorf("body missing capture total: %s", out.ProposedBody)
	}
}

func TestWeeklyReviewRegisteredReadOnly(t *testing.T) {
	tmp := t.TempDir()
	r := tools.NewRegistry()
	if err := RegisterWeeklyReview(r, service.TaskService{TasksDir: tmp}, filepath.Join(tmp, "00-inbox"), filepath.Join(tmp, "00-inbox", "archive"), fixedDailyPath(tmp)); err != nil {
		t.Fatalf("register: %v", err)
	}
	regTool, ok := r.Lookup(WeeklyReviewToolName)
	if !ok {
		t.Fatal("tool not registered")
	}
	if regTool.Source.Kind != tools.SourceSkill {
		t.Fatalf("source = %v, want skill", regTool.Source.Kind)
	}
	if regTool.Mutating {
		t.Fatal("weekly-review must be read-only")
	}

	// Execute with empty params: defaults apply, no error on missing dirs.
	raw, err := tools.Execute(context.Background(), r, WeeklyReviewToolName, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out weeklyReviewOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.CompletedN != 0 || out.CaptureTotal != 0 {
		t.Fatalf("expected empty aggregation, got %+v", out)
	}
	if out.ProposedTitle == "" || out.ProposedBody == "" {
		t.Fatal("proposed title/body must be non-empty even when empty")
	}
}

func TestWeeklyReviewMissingDirsNotFatal(t *testing.T) {
	tmp := t.TempDir()
	tasksDir := filepath.Join(tmp, "10-tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "inbox.md"), []byte("- [ ] open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 6, 10, 0, 0, 0, 0, time.Local)
	out, err := proposeWeeklyReview(service.TaskService{TasksDir: tasksDir},
		filepath.Join(tmp, "no-inbox"), filepath.Join(tmp, "no-archive"),
		fixedDailyPath(filepath.Join(tmp, "no-daily")), end, 7)
	if err != nil {
		t.Fatalf("missing dirs should not be fatal: %v", err)
	}
	if out.CaptureTotal != 0 || len(out.Highlights) != 0 {
		t.Fatalf("expected empty captures/highlights, got %+v", out)
	}
}

func TestWeeklyReviewApplyIsMutating(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterWeeklyReviewApply(r, service.NoteService{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := r.Lookup(WeeklyReviewApplyToolName)
	if !ok {
		t.Fatal("apply tool not registered")
	}
	if !tool.Mutating {
		t.Fatal("weekly-review-apply must be Mutating so the policy gate confirms non-cli callers")
	}
}

func TestWeeklyReviewApplyWritesNote(t *testing.T) {
	tmp := t.TempDir()
	notes := service.NoteService{NotesDir: filepath.Join(tmp, "20-notes")}

	title := "Weekly Review 2026-06-04 — 2026-06-10"
	body := "# Weekly Review 2026-06-04 — 2026-06-10\n\n## Completed (1)\n- Ship it — 2026-06-09\n"

	out, err := applyWeeklyReview(notes, weeklyReviewApplyInput{Title: title, Body: body})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Created == "" {
		t.Fatal("created path not returned")
	}
	if out.Title != title {
		t.Errorf("title = %q, want %q", out.Title, title)
	}
	content, err := os.ReadFile(out.Created)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if !strings.Contains(string(content), "Ship it — 2026-06-09") {
		t.Errorf("note missing body: %s", content)
	}
}

func TestWeeklyReviewApplyRejectsEmpty(t *testing.T) {
	notes := service.NoteService{NotesDir: t.TempDir()}
	if _, err := applyWeeklyReview(notes, weeklyReviewApplyInput{Title: "", Body: "x"}); err == nil {
		t.Error("expected error for empty title")
	}
	if _, err := applyWeeklyReview(notes, weeklyReviewApplyInput{Title: "x", Body: "  "}); err == nil {
		t.Error("expected error for empty body")
	}
}

// writeDatedCapture writes a capture file named by the YYYY-MM-DD... convention
// into dir (creating it), with a timestamp header and body.
func writeDatedCapture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "2026-06-10 10:00:00\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
