package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qi/internal/calendar"
	"qi/internal/domain"
	"qi/internal/service"
	"qi/internal/tools"
)

// stubProvider returns a fixed list of events regardless of the requested range.
type stubProvider struct {
	name   string
	events []domain.Event
}

func (s stubProvider) Name() string { return s.name }
func (s stubProvider) Events(from, to time.Time) ([]domain.Event, error) {
	return s.events, nil
}

func writeTaskFile(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "inbox.md")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write task file: %v", err)
	}
	return path
}

func writeCapture(t *testing.T, inbox, name, body string, modTime time.Time) string {
	t.Helper()
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(inbox, name)
	content := "2026-05-18 10:00:00\n\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDailyReviewComposesSources(t *testing.T) {
	tmp := t.TempDir()
	taskPath := writeTaskFile(t, tmp,
		"- [ ] Ship qid stage 9 #qi 📅 2026-05-19",
		"- [x] Already done #qi",
		"- [ ] Buy oat milk",
	)

	inbox := filepath.Join(tmp, "00-inbox")
	now := time.Now()
	writeCapture(t, inbox, "old.md", "old idea", now.Add(-48*time.Hour))
	writeCapture(t, inbox, "newer.md", "fresh thought", now.Add(-1*time.Hour))
	writeCapture(t, inbox, "newest.md", "just now", now)

	tasksSvc := service.TaskService{TaskFilePath: taskPath}
	agendaSvc := service.AgendaService{Providers: []calendar.Provider{
		stubProvider{name: "stub", events: []domain.Event{
			{Title: "Standup", Start: timeAt(9, 0), End: timeAt(9, 15), Project: "team"},
			{Title: "Deep work", Start: timeAt(10, 0), End: timeAt(12, 0)},
		}},
	}}

	r := tools.NewRegistry()
	if err := RegisterDailyReview(r, tasksSvc, agendaSvc, inbox); err != nil {
		t.Fatalf("register: %v", err)
	}

	regTool, ok := r.Lookup(DailyReviewToolName)
	if !ok {
		t.Fatalf("tool not registered")
	}
	if regTool.Source.Kind != tools.SourceSkill {
		t.Fatalf("source kind = %v, want skill", regTool.Source.Kind)
	}
	if regTool.Mutating {
		t.Fatal("daily-review must not be mutating")
	}

	raw, err := tools.Execute(context.Background(), r, DailyReviewToolName, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var out dailyReviewOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if out.Date != time.Now().Format("2006-01-02") {
		t.Errorf("date = %s", out.Date)
	}
	if len(out.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(out.Events))
	}
	if out.Events[0].Title != "Standup" || out.Events[0].Start != "09:00" || out.Events[0].End != "09:15" {
		t.Errorf("first event = %+v", out.Events[0])
	}

	if len(out.OpenTasks) != 2 {
		t.Fatalf("open tasks = %d, want 2 (incomplete only)", len(out.OpenTasks))
	}
	foundShip, foundMilk := false, false
	for _, task := range out.OpenTasks {
		if strings.Contains(task.Text, "Ship qid stage 9") {
			foundShip = true
			if task.Due != "2026-05-19" {
				t.Errorf("ship task due = %q", task.Due)
			}
			if task.Project != "qi" {
				t.Errorf("ship task project = %q", task.Project)
			}
		}
		if strings.Contains(task.Text, "oat milk") {
			foundMilk = true
		}
	}
	if !foundShip || !foundMilk {
		t.Errorf("missing tasks: ship=%v milk=%v", foundShip, foundMilk)
	}

	if len(out.RecentCaptures) != 3 {
		t.Fatalf("captures = %d, want 3", len(out.RecentCaptures))
	}
	if filepath.Base(out.RecentCaptures[0].Path) != "newest.md" {
		t.Errorf("first capture = %s, want newest first", out.RecentCaptures[0].Path)
	}
	if out.RecentCaptures[0].Preview != "just now" {
		t.Errorf("preview = %q", out.RecentCaptures[0].Preview)
	}
}

func TestDailyReviewMissingInboxIsNotFatal(t *testing.T) {
	tmp := t.TempDir()
	taskPath := writeTaskFile(t, tmp, "- [ ] Solo task")

	r := tools.NewRegistry()
	if err := RegisterDailyReview(r, service.TaskService{TaskFilePath: taskPath}, service.AgendaService{}, filepath.Join(tmp, "no-such-dir")); err != nil {
		t.Fatalf("register: %v", err)
	}
	raw, err := tools.Execute(context.Background(), r, DailyReviewToolName, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out dailyReviewOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.RecentCaptures) != 0 {
		t.Fatalf("expected empty captures, got %+v", out.RecentCaptures)
	}
	if len(out.OpenTasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(out.OpenTasks))
	}
}

func TestRecentCapturesLimit(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "inbox")
	base := time.Now().Add(-1 * time.Hour)
	for i := 0; i < 10; i++ {
		writeCapture(t, inbox, filepath.Base(filepath.Join(inbox, "c"+string(rune('0'+i))+".md")), "body", base.Add(time.Duration(i)*time.Minute))
	}
	got, err := recentCaptures(inbox, recentCapturesLimit)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(got) != recentCapturesLimit {
		t.Fatalf("len = %d, want %d", len(got), recentCapturesLimit)
	}
}

func timeAt(hour, minute int) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
}
