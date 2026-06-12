package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qi/internal/tools"
)

// nestedDailyPath maps each day to dir/YYYY/Month/YYYY-MM-DD.md so the test can
// prove appendSessionLog creates a nested parent directory on demand.
func nestedDailyPath(dir string) func(time.Time) string {
	return func(d time.Time) string {
		return filepath.Join(dir, d.Format("2006"), d.Format("January"), d.Format("2006-01-02")+".md")
	}
}

func TestSessionLogCreatesDailyAndLogsSection(t *testing.T) {
	tmp := t.TempDir()
	pathFor := nestedDailyPath(tmp)
	now := time.Date(2026, 6, 11, 9, 30, 0, 0, time.Local)

	out, err := appendSessionLog(pathFor, now, "started the migration")
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	if out.Time != "09:30" {
		t.Errorf("time = %q, want 09:30", out.Time)
	}
	if out.Logged != "started the migration" {
		t.Errorf("logged = %q", out.Logged)
	}
	// Parent dir was nested; the file must exist there.
	want := filepath.Join(tmp, "2026", "June", "2026-06-11.md")
	if out.Path != want {
		t.Fatalf("path = %s, want %s", out.Path, want)
	}
	content, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatalf("read daily: %v", err)
	}
	if !strings.Contains(string(content), "## Logs") {
		t.Errorf("missing ## Logs section: %s", content)
	}
	if !strings.Contains(string(content), "- [09:30] started the migration") {
		t.Errorf("missing timestamped line: %s", content)
	}
}

func TestSessionLogSecondAppendUnderSameHeading(t *testing.T) {
	tmp := t.TempDir()
	pathFor := nestedDailyPath(tmp)
	day := time.Date(2026, 6, 11, 0, 0, 0, 0, time.Local)

	first := day.Add(9 * time.Hour)
	second := day.Add(14*time.Hour + 15*time.Minute)

	if _, err := appendSessionLog(pathFor, first, "first entry"); err != nil {
		t.Fatalf("first append: %v", err)
	}
	out2, err := appendSessionLog(pathFor, second, "second entry")
	if err != nil {
		t.Fatalf("second append: %v", err)
	}

	content, err := os.ReadFile(out2.Path)
	if err != nil {
		t.Fatalf("read daily: %v", err)
	}
	s := string(content)
	if strings.Count(s, "## Logs") != 1 {
		t.Errorf("expected a single ## Logs heading: %s", s)
	}
	idx1 := strings.Index(s, "- [09:00] first entry")
	idx2 := strings.Index(s, "- [14:15] second entry")
	if idx1 < 0 || idx2 < 0 {
		t.Fatalf("both entries must be present: %s", s)
	}
	if idx1 > idx2 {
		t.Errorf("entry order not preserved: %s", s)
	}
}

func TestSessionLogEmptyTextErrors(t *testing.T) {
	if _, err := appendSessionLog(nestedDailyPath(t.TempDir()), time.Now(), "   "); err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestSessionLogRegisteredMutating(t *testing.T) {
	tmp := t.TempDir()
	r := tools.NewRegistry()
	if err := RegisterSessionLog(r, nestedDailyPath(tmp)); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := r.Lookup(SessionLogToolName)
	if !ok {
		t.Fatal("tool not registered")
	}
	if tool.Source.Kind != tools.SourceSkill {
		t.Fatalf("source = %v, want skill", tool.Source.Kind)
	}
	if !tool.Mutating {
		t.Fatal("session-log must be Mutating so the policy gate confirms non-cli callers")
	}

	raw, err := tools.Execute(context.Background(), r, SessionLogToolName, json.RawMessage(`{"text":"via registry"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out sessionLogOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Logged != "via registry" {
		t.Fatalf("unexpected output: %+v", out)
	}
}
