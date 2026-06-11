package skills

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"qi/internal/service"
	"qi/internal/tools"
)

// ProcessInboxToolName is the registered name for the read-only inbox triage
// skill. It scans 00-inbox/ captures and proposes, per item, whether the
// capture should become a task, a note, or simply be archived. It is purely
// read-only — it never writes to the vault. Applying a proposal goes through
// the separate, mutating ProcessInboxApply skill so the policy layer can gate
// it.
const ProcessInboxToolName = "skill.process-inbox"

// ProcessInboxApplyToolName is the registered name for the mutating companion
// skill that executes a single proposal. It is declared Mutating so non-cli
// callers (ai-planner, mcp) route through the approval queue.
const ProcessInboxApplyToolName = "skill.process-inbox-apply"

// Proposed actions emitted by skill.process-inbox and accepted by
// skill.process-inbox-apply.
const (
	actionTask    = "task"
	actionNote    = "note"
	actionArchive = "archive"
)

// taskLineMax is the length below which a single-line capture is treated as a
// task rather than a note. Short, single-line thoughts read as todos; longer
// or multi-line content reads as a note.
const taskLineMax = 80

// processInboxOutput is the JSON shape returned by skill.process-inbox.
type processInboxOutput struct {
	Count int         `json:"count"`
	Items []inboxItem `json:"items"`
}

type inboxItem struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
	Action  string `json:"proposed_action"`
	Reason  string `json:"reason"`
}

// RegisterProcessInbox binds the read-only skill.process-inbox tool. inboxDir
// is scanned (non-recursively) for *.md captures.
func RegisterProcessInbox(r *tools.Registry, inboxDir string) error {
	tool := tools.Tool{
		Name:        ProcessInboxToolName,
		Version:     "1",
		Mutating:    false,
		Description: "Triage 00-inbox captures: summarize each and propose task, note, or archive.",
		Schema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
	handler := func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		items, err := proposeInbox(inboxDir)
		if err != nil {
			return nil, err
		}
		return json.Marshal(processInboxOutput{Count: len(items), Items: items})
	}
	return r.RegisterSkill(tool, handler)
}

// proposeInbox scans inboxDir for captures and returns one proposal per item,
// sorted by path for determinism. A missing inbox directory is not an error.
func proposeInbox(inboxDir string) ([]inboxItem, error) {
	items := []inboxItem{}
	if inboxDir == "" {
		return items, nil
	}
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return items, nil
		}
		return nil, fmt.Errorf("read inbox: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(inboxDir, e.Name())
		body := captureBody(path)
		action, reason := proposeAction(body)
		items = append(items, inboxItem{
			Path:    path,
			Summary: summarize(body),
			Action:  action,
			Reason:  reason,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items, nil
}

// proposeAction applies deterministic heuristics to a capture's body lines:
//   - no content                       → archive (nothing to action)
//   - explicit task marker on a line   → task
//   - single short line                → task
//   - anything longer / multi-line     → note
func proposeAction(body []string) (action, reason string) {
	if len(body) == 0 {
		return actionArchive, "empty capture — nothing to action"
	}
	for _, line := range body {
		if hasTaskMarker(line) {
			return actionTask, "contains a task marker"
		}
	}
	if len(body) == 1 && len(body[0]) <= taskLineMax {
		return actionTask, "short single-line capture reads as a task"
	}
	return actionNote, "multi-line or long content reads as a note"
}

// hasTaskMarker reports whether a line is explicitly task-shaped.
func hasTaskMarker(line string) bool {
	l := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.HasPrefix(l, "- [ ]"), strings.HasPrefix(l, "- [x]"):
		return true
	case strings.HasPrefix(l, "[ ]"), strings.HasPrefix(l, "[x]"):
		return true
	case strings.HasPrefix(l, "todo:"), strings.HasPrefix(l, "todo "):
		return true
	case strings.HasPrefix(l, "task:"):
		return true
	default:
		return false
	}
}

// summarize returns a one-line preview of a capture body.
func summarize(body []string) string {
	if len(body) == 0 {
		return "(empty)"
	}
	return body[0]
}

// captureBody returns the non-empty content lines of a capture, skipping the
// leading "YYYY-MM-DD HH:MM:SS" header that WriteCapture emits. Best-effort:
// an unreadable file yields no body.
func captureBody(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	sawHeader := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !sawHeader {
			sawHeader = true
			continue
		}
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// inboxApplyInput is the JSON shape skill.process-inbox-apply accepts.
type inboxApplyInput struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Title   string `json:"title,omitempty"`
	Project string `json:"project,omitempty"`
}

// inboxApplyOutput reports what the mutation did.
type inboxApplyOutput struct {
	Action   string `json:"action"`
	Source   string `json:"source"`
	Created  string `json:"created,omitempty"`
	Archived string `json:"archived"`
}

// RegisterProcessInboxApply binds the mutating skill.process-inbox-apply tool.
// It executes a single proposal: create a task or note from the capture (and
// then archive it), or simply archive it. archiveDir receives the original
// capture after a successful apply.
func RegisterProcessInboxApply(r *tools.Registry, inboxDir, archiveDir string, tasks service.TaskService, notes service.NoteService) error {
	tool := tools.Tool{
		Name:        ProcessInboxApplyToolName,
		Version:     "1",
		Mutating:    true,
		Description: "Apply a process-inbox proposal: turn a capture into a task or note, then archive it (or just archive).",
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,` +
			`"required":["path","action"],` +
			`"properties":{` +
			`"path":{"type":"string","description":"absolute path to the capture inside the inbox"},` +
			`"action":{"type":"string","enum":["task","note","archive"]},` +
			`"title":{"type":"string","description":"optional title/text override for the task or note"},` +
			`"project":{"type":"string","description":"optional project tag for a task"}}}`),
	}
	handler := func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
		var in inboxApplyInput
		if len(params) > 0 {
			if err := json.Unmarshal(params, &in); err != nil {
				return nil, fmt.Errorf("process-inbox-apply: invalid params: %w", err)
			}
		}
		out, err := applyInbox(inboxDir, archiveDir, tasks, notes, in)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return r.RegisterSkill(tool, handler)
}

func applyInbox(inboxDir, archiveDir string, tasks service.TaskService, notes service.NoteService, in inboxApplyInput) (inboxApplyOutput, error) {
	src, err := validateCapturePath(inboxDir, in.Path)
	if err != nil {
		return inboxApplyOutput{}, err
	}
	body := captureBody(src)

	out := inboxApplyOutput{Action: in.Action, Source: src}
	switch in.Action {
	case actionArchive:
		// nothing to create.
	case actionTask:
		text := firstNonEmpty(in.Title, summarize(body))
		if strings.TrimSpace(text) == "" {
			return inboxApplyOutput{}, fmt.Errorf("process-inbox-apply: capture has no text for a task")
		}
		created, err := tasks.CreateTask(service.AddTaskInput{Text: text, Project: in.Project})
		if err != nil {
			return inboxApplyOutput{}, fmt.Errorf("process-inbox-apply: add task: %w", err)
		}
		out.Created = created.FilePath
	case actionNote:
		title := firstNonEmpty(in.Title, summarize(body))
		if strings.TrimSpace(title) == "" || title == "(empty)" {
			title = "Inbox note"
		}
		note, err := notes.AddNote(title, strings.Join(body, "\n"))
		if err != nil {
			return inboxApplyOutput{}, fmt.Errorf("process-inbox-apply: add note: %w", err)
		}
		out.Created = note.Path
	default:
		return inboxApplyOutput{}, fmt.Errorf("process-inbox-apply: unknown action %q (want task, note, or archive)", in.Action)
	}

	dest, err := archiveCapture(src, archiveDir)
	if err != nil {
		return inboxApplyOutput{}, fmt.Errorf("process-inbox-apply: archive: %w", err)
	}
	out.Archived = dest
	return out, nil
}

// validateCapturePath ensures path resolves to a regular file sitting directly
// inside inboxDir, blocking traversal outside the inbox.
func validateCapturePath(inboxDir, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("process-inbox-apply: path is required")
	}
	if inboxDir == "" {
		return "", fmt.Errorf("process-inbox-apply: inbox directory not configured")
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(inboxDir, clean)
	}
	if filepath.Dir(clean) != filepath.Clean(inboxDir) {
		return "", fmt.Errorf("process-inbox-apply: %q is not a capture inside the inbox", path)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("process-inbox-apply: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("process-inbox-apply: %q is a directory", path)
	}
	return clean, nil
}

// archiveCapture moves src into archiveDir, creating the directory if needed
// and avoiding name collisions by appending a numeric suffix.
func archiveCapture(src, archiveDir string) (string, error) {
	if archiveDir == "" {
		return "", fmt.Errorf("archive directory not configured")
	}
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(src)
	dest := filepath.Join(archiveDir, base)
	if dest != src {
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		for i := 1; ; i++ {
			if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
				break
			}
			dest = filepath.Join(archiveDir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		}
	}
	if err := os.Rename(src, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
