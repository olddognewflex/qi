package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qi/internal/service"
	"qi/internal/tools"
)

func newTaskRegistry(t *testing.T, validate ClientValidator) (*tools.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	svc := service.TaskService{
		TaskFilePath: filepath.Join(dir, "inbox.md"),
		TasksDir:     dir,
	}
	r := tools.NewRegistry()
	if err := RegisterTaskAdd(r, svc, validate); err != nil {
		t.Fatalf("register: %v", err)
	}
	return r, dir
}

func callTaskAdd(t *testing.T, r *tools.Registry, p taskAddParams) (taskAddResult, error) {
	t.Helper()
	args, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tools.Execute(context.Background(), r, TaskAddToolName, args)
	if err != nil {
		return taskAddResult{}, err
	}
	var res taskAddResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return res, nil
}

func TestTaskAddMintsIDAndRoutesByProject(t *testing.T) {
	r, dir := newTaskRegistry(t, nil)

	res, err := callTaskAdd(t, r, taskAddParams{Text: "Ship release", Project: "qi"})
	if err != nil {
		t.Fatalf("task.add: %v", err)
	}
	if !strings.HasPrefix(res.ID, "qi-") || len(res.ID) != len("qi-")+8 {
		t.Fatalf("id %q not a qi block ref", res.ID)
	}
	wantPath := filepath.Join(dir, "qi.md")
	if res.Path != wantPath {
		t.Fatalf("path %q, want %q (project routing)", res.Path, wantPath)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read task file: %v", err)
	}
	line := string(data)
	if !strings.Contains(line, "Ship release") {
		t.Fatalf("file missing text: %q", line)
	}
	if !strings.Contains(line, "^"+res.ID) {
		t.Fatalf("file missing block-ref id ^%s: %q", res.ID, line)
	}
	if !strings.Contains(line, "#qi") {
		t.Fatalf("file missing project tag #qi: %q", line)
	}
}

func TestTaskAddInboxWhenNoProject(t *testing.T) {
	r, dir := newTaskRegistry(t, nil)
	res, err := callTaskAdd(t, r, taskAddParams{Text: "Unfiled thing"})
	if err != nil {
		t.Fatalf("task.add: %v", err)
	}
	if res.Path != filepath.Join(dir, "inbox.md") {
		t.Fatalf("path %q, want inbox.md", res.Path)
	}
}

func TestTaskAddClientValidation(t *testing.T) {
	r, _ := newTaskRegistry(t, func(name string) bool { return name == "acme" })

	if _, err := callTaskAdd(t, r, taskAddParams{Text: "x", Client: "ghost"}); err == nil {
		t.Fatal("expected error for unknown client")
	}
	if _, err := callTaskAdd(t, r, taskAddParams{Text: "x", Client: "acme"}); err != nil {
		t.Fatalf("valid client rejected: %v", err)
	}
}

func TestTaskAddProjectAndClientMutuallyExclusive(t *testing.T) {
	r, _ := newTaskRegistry(t, func(string) bool { return true })
	if _, err := callTaskAdd(t, r, taskAddParams{Text: "x", Project: "p", Client: "c"}); err == nil {
		t.Fatal("expected error when both project and client set")
	}
}

func TestTaskAddRejectsEmptyTextAndBadDate(t *testing.T) {
	r, _ := newTaskRegistry(t, nil)
	if _, err := callTaskAdd(t, r, taskAddParams{Text: "  "}); err == nil {
		t.Fatal("expected error for empty text")
	}
	if _, err := callTaskAdd(t, r, taskAddParams{Text: "x", Due: "2026/01/01"}); err == nil {
		t.Fatal("expected error for malformed due date")
	}
}

func TestTaskAddRejectsNewlineInjection(t *testing.T) {
	r, dir := newTaskRegistry(t, nil)
	// An embedded newline would forge a second task line in the canonical file.
	if _, err := callTaskAdd(t, r, taskAddParams{Text: "buy milk\n- [ ] forged task"}); err == nil {
		t.Fatal("expected error for newline in text")
	}
	// Carriage return and tab are control chars too.
	if _, err := callTaskAdd(t, r, taskAddParams{Text: "a\tb"}); err == nil {
		t.Fatal("expected error for tab in text")
	}
	// Nothing should have been written.
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("injection attempt wrote files: %v", entries)
	}
}

func TestTaskAddRejectsBadProject(t *testing.T) {
	r, _ := newTaskRegistry(t, nil)
	for _, bad := range []string{"a b", "../escape", "tag\nx", "foo#bar"} {
		if _, err := callTaskAdd(t, r, taskAddParams{Text: "x", Project: bad}); err == nil {
			t.Fatalf("expected error for invalid project %q", bad)
		}
	}
	// Valid tag charset still works (letters/digits/_-/).
	if _, err := callTaskAdd(t, r, taskAddParams{Text: "x", Project: "work/client_a-1"}); err != nil {
		t.Fatalf("valid project rejected: %v", err)
	}
}

func TestTaskAddEmptyTaskFilePath(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterTaskAdd(r, service.TaskService{}, nil); err == nil {
		t.Fatal("expected error for empty task file path")
	}
}
