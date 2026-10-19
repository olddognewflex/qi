package service

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Inbox triage actions. Task/Note/Archive are the deterministic proposals
// emitted by InboxService.List and accepted by Apply; Delete is an
// interactive-only action (the TUI offers it) that removes a capture outright
// rather than archiving it.
const (
	InboxActionTask    = "task"
	InboxActionNote    = "note"
	InboxActionArchive = "archive"
	InboxActionDelete  = "delete"
)

// inboxTaskLineMax is the length below which a single-line capture is treated
// as a task rather than a note. Short, single-line thoughts read as todos;
// longer or multi-line content reads as a note.
const inboxTaskLineMax = 80

// InboxItem is one inbox capture plus the action proposed for it.
type InboxItem struct {
	Path    string
	Summary string
	Body    []string
	Action  string
	Reason  string
	// Repeats are the paths of other captures with an identical body, set
	// only by CollapseRepeats; List never fills it.
	Repeats []string
}

// InboxApplyInput selects a capture and the action to take on it. Title
// overrides the derived task text / note title; Project tags a task.
type InboxApplyInput struct {
	Path    string
	Action  string
	Title   string
	Project string
}

// InboxApplyOutput reports what an Apply did. Created is the path of a new
// task or note (empty for archive/delete); Archived is where the capture
// landed (empty for delete).
type InboxApplyOutput struct {
	Action   string
	Source   string
	Created  string
	Archived string
}

// InboxService triages 00-inbox captures. It is the canonical home for the
// triage heuristics and apply logic; both the qi inbox command and the
// skill.process-inbox tools drive the vault through it.
type InboxService struct {
	InboxDir   string
	ArchiveDir string
	Tasks      TaskService
	Notes      NoteService
}

// List scans InboxDir (non-recursively) for *.md captures and returns one
// proposal per item, sorted by path for determinism. A missing inbox
// directory is not an error.
func (s InboxService) List() ([]InboxItem, error) {
	items := []InboxItem{}
	if s.InboxDir == "" {
		return items, nil
	}
	entries, err := os.ReadDir(s.InboxDir)
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
		path := filepath.Join(s.InboxDir, e.Name())
		body := captureBody(path)
		action, reason := proposeInboxAction(body)
		items = append(items, InboxItem{
			Path:    path,
			Summary: summarizeBody(body),
			Body:    body,
			Action:  action,
			Reason:  reason,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items, nil
}

// Apply executes a single action on one capture: create a task or note (and
// archive the capture), archive it, or delete it outright. The capture path is
// validated to sit directly inside InboxDir, blocking traversal.
//
// For task/note the capture is archived FIRST and the task or note created
// second; if creation fails the capture is moved back. So a created task or
// note never leaves its capture behind in the inbox to be proposed — and
// created — again on the next run.
func (s InboxService) Apply(in InboxApplyInput) (InboxApplyOutput, error) {
	src, err := validateInboxPath(s.InboxDir, in.Path)
	if err != nil {
		return InboxApplyOutput{}, err
	}
	body := captureBody(src)

	out := InboxApplyOutput{Action: in.Action, Source: src}
	var create func() (string, error)
	switch in.Action {
	case InboxActionArchive:
		// nothing to create.
	case InboxActionDelete:
		if err := os.Remove(src); err != nil {
			return InboxApplyOutput{}, fmt.Errorf("inbox delete: %w", err)
		}
		return out, nil
	case InboxActionTask:
		text := firstNonEmptyValue(in.Title, summarizeBody(body))
		if strings.TrimSpace(text) == "" {
			return InboxApplyOutput{}, fmt.Errorf("inbox: capture has no text for a task")
		}
		create = func() (string, error) {
			created, err := s.Tasks.CreateTask(AddTaskInput{Text: text, Project: in.Project})
			if err != nil {
				return "", fmt.Errorf("inbox: add task: %w", err)
			}
			return created.FilePath, nil
		}
	case InboxActionNote:
		title := firstNonEmptyValue(in.Title, summarizeBody(body))
		if strings.TrimSpace(title) == "" || title == "(empty)" {
			title = "Inbox note"
		}
		create = func() (string, error) {
			note, err := s.Notes.AddNote(title, strings.Join(body, "\n"))
			if err != nil {
				return "", fmt.Errorf("inbox: add note: %w", err)
			}
			return note.Path, nil
		}
	default:
		return InboxApplyOutput{}, fmt.Errorf("inbox: unknown action %q (want task, note, archive, or delete)", in.Action)
	}

	dest, err := archiveCapture(src, s.ArchiveDir)
	if err != nil {
		return InboxApplyOutput{}, fmt.Errorf("inbox: archive: %w", err)
	}
	if create != nil {
		created, err := create()
		if err != nil {
			if rerr := os.Rename(dest, src); rerr != nil {
				return InboxApplyOutput{}, fmt.Errorf("%w (and restoring the capture from %s failed: %v)", err, dest, rerr)
			}
			return InboxApplyOutput{}, err
		}
		out.Created = created
	}
	out.Archived = dest
	return out, nil
}

// ApplyGroup applies one decision to a collapsed item. The repeats are
// disposed of FIRST — archived for task/note/archive, deleted for delete —
// and the action itself (the only step that creates anything) is applied to
// it.Path last. A repeat that has already vanished is skipped; any other
// repeat error stops the group before the task or note is created, so a
// failure can leave captures behind but never a created item whose captures
// will propose it again. Outputs for everything applied are returned
// alongside any error, the representative's first when it was applied.
func (s InboxService) ApplyGroup(it InboxItem, action string) ([]InboxApplyOutput, error) {
	rest := InboxActionArchive
	if action == InboxActionDelete {
		rest = InboxActionDelete
	}
	var disposed []InboxApplyOutput
	for _, p := range it.Repeats {
		o, err := s.Apply(InboxApplyInput{Path: p, Action: rest})
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return disposed, fmt.Errorf("repeat %s: %w", filepath.Base(p), err)
		}
		disposed = append(disposed, o)
	}
	first, err := s.Apply(InboxApplyInput{Path: it.Path, Action: action})
	if err != nil {
		return disposed, err
	}
	return append([]InboxApplyOutput{first}, disposed...), nil
}

// proposeInboxAction applies deterministic heuristics to a capture's body:
//   - no content                       → archive (nothing to action)
//   - explicit task marker on a line   → task
//   - email summary capture            → by its mail-filter tag
//     (reply_needed/flag_for_followup → task; read_now/mark_read/
//     unsubscribe/delete → archive; other tags fall through)
//   - single short line                → task
//   - anything longer / multi-line     → note
func proposeInboxAction(body []string) (action, reason string) {
	if len(body) == 0 {
		return InboxActionArchive, "empty capture — nothing to action"
	}
	for _, line := range body {
		if hasTaskMarker(line) {
			return InboxActionTask, "contains a task marker"
		}
	}
	if tag, ok := emailCaptureTag(body); ok {
		if action, known := emailTagActions[tag]; known {
			return action, "email tag [" + tag + "]"
		}
	}
	if len(body) == 1 && len(body[0]) <= inboxTaskLineMax {
		return InboxActionTask, "short single-line capture reads as a task"
	}
	return InboxActionNote, "multi-line or long content reads as a note"
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

// summarizeBody returns a one-line preview of a capture body.
func summarizeBody(body []string) string {
	if len(body) == 0 {
		return "(empty)"
	}
	return body[0]
}

// captureBody returns the non-empty content lines of a capture, skipping the
// leading "YYYY-MM-DD HH:MM:SS" header that vault.WriteCapture emits.
// Best-effort: an unreadable file yields no body.
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

// validateInboxPath ensures path resolves to a regular file sitting directly
// inside inboxDir, blocking traversal outside the inbox.
func validateInboxPath(inboxDir, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("inbox: path is required")
	}
	if inboxDir == "" {
		return "", fmt.Errorf("inbox: inbox directory not configured")
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(inboxDir, clean)
	}
	if filepath.Dir(clean) != filepath.Clean(inboxDir) {
		return "", fmt.Errorf("inbox: %q is not a capture inside the inbox", path)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("inbox: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("inbox: %q is a directory", path)
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

func firstNonEmptyValue(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
