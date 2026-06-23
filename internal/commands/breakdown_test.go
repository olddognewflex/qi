package commands

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"qi/internal/ai"
	"qi/internal/config"
	"qi/internal/domain"
	"qi/internal/service"
)

// fakeLLM returns a scripted response for every Generate call.
type fakeLLM struct {
	resp *ai.GenerateResponse
	err  error
}

func (f fakeLLM) Generate(context.Context, ai.GenerateRequest) (*ai.GenerateResponse, error) {
	return f.resp, f.err
}

// newBreakdownFixture builds a TaskService over a temp tasks dir and seeds a
// parent task, returning the service and the parsed parent (with its minted ID).
func newBreakdownFixture(t *testing.T) (service.TaskService, domain.Task) {
	t.Helper()
	dir := t.TempDir()
	svc := service.TaskService{
		TaskFilePath: filepath.Join(dir, "inbox.md"),
		TasksDir:     dir,
	}
	parent, err := svc.CreateTask(service.AddTaskInput{Text: "Ship release"})
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if parent.ID == "" {
		t.Fatal("seeded parent has no ID")
	}
	return svc, parent
}

func subtaskLLM() ai.LLM {
	return fakeLLM{resp: &ai.GenerateResponse{
		Text:       `["Cut changelog", "Tag v2"]`,
		StopReason: "end_turn",
	}}
}

func childrenOf(t *testing.T, svc service.TaskService, parentID string) []domain.Task {
	t.Helper()
	all, err := svc.ListAllTasks()
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	var kids []domain.Task
	for _, task := range all {
		if task.ParentID == parentID {
			kids = append(kids, task)
		}
	}
	return kids
}

// Approval gate: a "y" writes each proposed subtask linked to the parent.
func TestRunBreakdown_ApprovedWritesLinkedSubtasks(t *testing.T) {
	svc, parent := newBreakdownFixture(t)
	var out bytes.Buffer
	err := runBreakdown(svc, parent, "normal", subtaskLLM(), "", false, strings.NewReader("y\n"), &out)
	if err != nil {
		t.Fatalf("runBreakdown: %v", err)
	}
	kids := childrenOf(t, svc, parent.ID)
	if len(kids) != 2 {
		t.Fatalf("want 2 subtasks linked to parent, got %d", len(kids))
	}
	for _, k := range kids {
		if k.ParentID != parent.ID {
			t.Errorf("subtask %q ParentID = %q, want %q", k.Text, k.ParentID, parent.ID)
		}
	}
}

// Approval gate: a "N" (declined) writes nothing.
func TestRunBreakdown_DeclinedWritesNothing(t *testing.T) {
	svc, parent := newBreakdownFixture(t)
	var out bytes.Buffer
	if err := runBreakdown(svc, parent, "normal", subtaskLLM(), "", false, strings.NewReader("n\n"), &out); err != nil {
		t.Fatalf("runBreakdown: %v", err)
	}
	if kids := childrenOf(t, svc, parent.ID); len(kids) != 0 {
		t.Fatalf("declined breakdown wrote %d subtasks, want 0", len(kids))
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("expected abort message, got: %q", out.String())
	}
}

// An empty response (bare Enter) is treated as decline — no writes.
func TestRunBreakdown_EmptyResponseDeclines(t *testing.T) {
	svc, parent := newBreakdownFixture(t)
	var out bytes.Buffer
	if err := runBreakdown(svc, parent, "normal", subtaskLLM(), "", false, strings.NewReader("\n"), &out); err != nil {
		t.Fatalf("runBreakdown: %v", err)
	}
	if kids := childrenOf(t, svc, parent.ID); len(kids) != 0 {
		t.Fatalf("empty response wrote %d subtasks, want 0", len(kids))
	}
}

// dry-run prints proposals and writes nothing, never reaching the gate.
func TestRunBreakdown_DryRunWritesNothing(t *testing.T) {
	svc, parent := newBreakdownFixture(t)
	var out bytes.Buffer
	// Reader is empty: dry-run must not block on input.
	if err := runBreakdown(svc, parent, "normal", subtaskLLM(), "", true, strings.NewReader(""), &out); err != nil {
		t.Fatalf("runBreakdown: %v", err)
	}
	if kids := childrenOf(t, svc, parent.ID); len(kids) != 0 {
		t.Fatalf("dry-run wrote %d subtasks, want 0", len(kids))
	}
	if !strings.Contains(out.String(), "dry-run") {
		t.Errorf("expected dry-run notice, got: %q", out.String())
	}
}

// An invalid level is rejected before any LLM call or write.
func TestRunBreakdown_InvalidLevel(t *testing.T) {
	svc, parent := newBreakdownFixture(t)
	var out bytes.Buffer
	err := runBreakdown(svc, parent, "deep", subtaskLLM(), "", false, strings.NewReader("y\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "invalid breakdown level") {
		t.Fatalf("expected invalid-level error, got %v", err)
	}
	if kids := childrenOf(t, svc, parent.ID); len(kids) != 0 {
		t.Fatalf("invalid level wrote %d subtasks, want 0", len(kids))
	}
}

// Command-level: cobra's NoOptDefVal resolves `--breakdown` (no value) to the
// default level and `--breakdown=fine` to the explicit one, while an absent flag
// never invokes breakdown. The base task must persist in every case. This is the
// only path that exercises the actual flag wiring (QI-4 acceptance: "tests cover
// flag parsing incl. optional value").
func TestTaskAdd_BreakdownFlagParsing(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantCalled bool
		wantLevel  string
	}{
		{"no flag", []string{"add", "Ship release"}, false, ""},
		{"bare flag uses default", []string{"add", "Ship release", "--breakdown"}, true, ai.DefaultBreakdownLevel},
		{"explicit level", []string{"add", "Ship release", "--breakdown=fine"}, true, "fine"},
		{"explicit coarse", []string{"add", "Ship release", "--breakdown=coarse"}, true, "coarse"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.Config{TaskFilePath: filepath.Join(dir, "inbox.md")}

			var called bool
			var gotLevel string
			var gotParent domain.Task
			orig := breakdownFn
			breakdownFn = func(_ service.TaskService, parent domain.Task, level string, _ ai.LLM, _ string, _ bool, _ io.Reader, _ io.Writer) error {
				called = true
				gotLevel = level
				gotParent = parent
				return nil
			}
			t.Cleanup(func() { breakdownFn = orig })

			cmd := newTaskCommand(cfg)
			cmd.SetArgs(c.args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute %v: %v", c.args, err)
			}

			if called != c.wantCalled {
				t.Fatalf("breakdown called = %v, want %v", called, c.wantCalled)
			}
			if c.wantCalled {
				if gotLevel != c.wantLevel {
					t.Errorf("resolved level = %q, want %q", gotLevel, c.wantLevel)
				}
				if gotParent.ID == "" {
					t.Error("breakdown received a parent with no ID")
				}
			}

			// The base task must be written regardless of the breakdown flag.
			svc := service.TaskService{TaskFilePath: cfg.TaskFilePath, TasksDir: dir}
			all, err := svc.ListAllTasks()
			if err != nil {
				t.Fatalf("list tasks: %v", err)
			}
			found := false
			for _, task := range all {
				if task.Text == "Ship release" {
					found = true
				}
			}
			if !found {
				t.Fatalf("base task not persisted for args %v", c.args)
			}
		})
	}
}

// Command-level: `qi task breakdown <fuzzy>` resolves a single fuzzy match and
// dispatches it to breakdown with the configured level.
func TestTaskBreakdown_SingleMatchDispatches(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{TaskFilePath: filepath.Join(dir, "inbox.md")}
	svc := service.TaskService{TaskFilePath: cfg.TaskFilePath, TasksDir: dir}
	if _, err := svc.CreateTask(service.AddTaskInput{Text: "Ship release"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var called bool
	var gotParent domain.Task
	var gotLevel string
	orig := breakdownFn
	breakdownFn = func(_ service.TaskService, parent domain.Task, level string, _ ai.LLM, _ string, _ bool, _ io.Reader, _ io.Writer) error {
		called, gotParent, gotLevel = true, parent, level
		return nil
	}
	t.Cleanup(func() { breakdownFn = orig })

	cmd := newTaskCommand(cfg)
	cmd.SetArgs([]string{"breakdown", "ship", "--level", "fine"})
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !called {
		t.Fatal("breakdown not dispatched for a single match")
	}
	if gotParent.Text != "Ship release" {
		t.Errorf("parent = %q, want %q", gotParent.Text, "Ship release")
	}
	if gotLevel != "fine" {
		t.Errorf("level = %q, want fine", gotLevel)
	}
}

// Command-level: a task that already has subtasks is reported and left
// unchanged — breakdown is never invoked (the already-broken-down guard).
func TestTaskBreakdown_AlreadyBrokenDownGuard(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{TaskFilePath: filepath.Join(dir, "inbox.md")}
	svc := service.TaskService{TaskFilePath: cfg.TaskFilePath, TasksDir: dir}
	parent, err := svc.CreateTask(service.AddTaskInput{Text: "Ship release"})
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if _, err := svc.CreateTask(service.AddTaskInput{Text: "Cut changelog", ParentID: parent.ID}); err != nil {
		t.Fatalf("seed child: %v", err)
	}

	var called bool
	orig := breakdownFn
	breakdownFn = func(_ service.TaskService, _ domain.Task, _ string, _ ai.LLM, _ string, _ bool, _ io.Reader, _ io.Writer) error {
		called = true
		return nil
	}
	t.Cleanup(func() { breakdownFn = orig })

	var out bytes.Buffer
	cmd := newTaskCommand(cfg)
	cmd.SetArgs([]string{"breakdown", "ship release"})
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if called {
		t.Fatal("breakdown invoked on a task that already has subtasks")
	}
}

// Command-level: an invalid --level is rejected before any matching or write.
func TestTaskBreakdown_InvalidLevel(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{TaskFilePath: filepath.Join(dir, "inbox.md")}
	cmd := newTaskCommand(cfg)
	cmd.SetArgs([]string{"breakdown", "x", "--level", "deep"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid --level") {
		t.Fatalf("expected invalid-level error, got %v", err)
	}
}

// A parent with no ID cannot be broken down (subtasks would have no link).
func TestRunBreakdown_NoParentID(t *testing.T) {
	svc, _ := newBreakdownFixture(t)
	var out bytes.Buffer
	err := runBreakdown(svc, domain.Task{Text: "orphan"}, "normal", subtaskLLM(), "", false, strings.NewReader("y\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "no id") {
		t.Fatalf("expected no-id error, got %v", err)
	}
}
