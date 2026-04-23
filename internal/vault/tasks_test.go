package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qi/internal/domain"
)

func TestParseTaskLine(t *testing.T) {
	line := "- [ ] Fix CDK bootstrap #builderhq 📅 2026-04-22"
	task, ok, err := ParseTaskLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected task line to parse")
	}
	if task.Text != "Fix CDK bootstrap #builderhq" {
		t.Fatalf("unexpected text: %q", task.Text)
	}
	if task.Project != "builderhq" {
		t.Fatalf("unexpected project: %q", task.Project)
	}
	if task.Due == nil || task.Due.Format("2006-01-02") != "2026-04-22" {
		t.Fatalf("unexpected due: %#v", task.Due)
	}
	if task.Completed {
		t.Fatal("expected open task")
	}
}

func TestUpdateTaskLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.md")
	initial := "- [ ] Task one\n- [ ] Task two\n- [ ] Task three\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UpdateTaskLine(path, 2, domain.Task{Text: "Task two", Completed: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if lines[0] != "- [ ] Task one" {
		t.Fatalf("line 1 changed: %q", lines[0])
	}
	if lines[1] != "- [x] Task two" {
		t.Fatalf("line 2: got %q, want %q", lines[1], "- [x] Task two")
	}
	if lines[2] != "- [ ] Task three" {
		t.Fatalf("line 3 changed: %q", lines[2])
	}
}

func TestFormatTaskLine(t *testing.T) {
	due := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	line, err := FormatTaskLine(domain.Task{
		Text:    "Fix CDK bootstrap",
		Project: "builderhq",
		Due:     &due,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "- [ ] Fix CDK bootstrap #builderhq 📅 2026-04-22"
	if line != want {
		t.Fatalf("got %q want %q", line, want)
	}
}
