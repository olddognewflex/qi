package vault

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"qi/internal/domain"
)

var (
	taskPrefixRe = regexp.MustCompile(`^\s*-\s\[( |x)\]\s+`)
	dueRe        = regexp.MustCompile(`📅\s+(\d{4}-\d{2}-\d{2})`)
	scheduledRe  = regexp.MustCompile(`⏳\s*(\d{4}-\d{2}-\d{2})`)
	doneRe       = regexp.MustCompile(`✅\s*(\d{4}-\d{2}-\d{2})`)
	// recurrenceRe captures the Obsidian Tasks 🔁 rule text. Go's regexp has no
	// lookahead, so a negated character class bounds the rule: it runs until the
	// next field/tag emoji (#, 📅, ⏳, ✅, 🔁), a block-ref caret, or end of line.
	recurrenceRe = regexp.MustCompile(`🔁\s*([^#📅⏳✅🔁\^\n]+)`)
	tagRe        = regexp.MustCompile(`#([A-Za-z0-9_\-\/]+)`)
	// parentRe captures the Dataview inline field linking a child task to its
	// parent's qi block-ref id, e.g. "[parent:: qi-1a2b3c4d]". Extracted before
	// tag parsing so it never leaks into Text/Tags. See docs/subtasks-design.md.
	parentRe   = regexp.MustCompile(`\[parent::\s*(qi-[0-9a-f]{8})\]`)
	idRe       = regexp.MustCompile(`\^(qi-[0-9a-f]{8})\s*$`)
	anyBlockRe = regexp.MustCompile(`\^[A-Za-z0-9_-]+\s*$`)
)

// MintID returns a new unique task ID of the form "qi-" + 8 lowercase hex chars.
// Uses crypto/rand so IDs are unpredictable and collisions are negligible.
func MintID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("vault.MintID: crypto/rand unavailable: " + err.Error())
	}
	return "qi-" + hex.EncodeToString(b[:])
}

func ParseTaskLine(line string) (domain.Task, bool, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return domain.Task{}, false, nil
	}
	if !taskPrefixRe.MatchString(trimmed) {
		return domain.Task{}, false, nil
	}

	completed := strings.HasPrefix(trimmed, "- [x]")
	content := taskPrefixRe.ReplaceAllString(trimmed, "")

	// Extract trailing qi block-ref ID before any other parsing so it never
	// leaks into Text or Tags. A foreign ^ref (non-qi format) is left untouched.
	var taskID string
	if m := idRe.FindStringSubmatch(content); len(m) == 2 {
		taskID = m[1]
		content = strings.TrimSpace(idRe.ReplaceAllString(content, ""))
	}

	var due *time.Time
	if m := dueRe.FindStringSubmatch(content); len(m) == 2 {
		parsed, err := time.Parse("2006-01-02", m[1])
		if err != nil {
			return domain.Task{}, false, fmt.Errorf("parse due date: %w", err)
		}
		due = &parsed
		content = strings.TrimSpace(dueRe.ReplaceAllString(content, ""))
	}

	var scheduled *time.Time
	if m := scheduledRe.FindStringSubmatch(content); len(m) == 2 {
		parsed, err := time.Parse("2006-01-02", m[1])
		if err != nil {
			return domain.Task{}, false, fmt.Errorf("parse scheduled date: %w", err)
		}
		scheduled = &parsed
		content = strings.TrimSpace(scheduledRe.ReplaceAllString(content, ""))
	}

	var completedAt *time.Time
	if m := doneRe.FindStringSubmatch(content); len(m) == 2 {
		parsed, err := time.Parse("2006-01-02", m[1])
		if err != nil {
			return domain.Task{}, false, fmt.Errorf("parse done date: %w", err)
		}
		completedAt = &parsed
		content = strings.TrimSpace(doneRe.ReplaceAllString(content, ""))
	}

	// Extract recurrence after the date emojis are gone (so they can't bound the
	// negated class oddly) and before tag parsing (so the rule text never leaks
	// into Tags/Project — the negated class already stops at the first #).
	recurrence := ""
	if m := recurrenceRe.FindStringSubmatch(content); len(m) == 2 {
		recurrence = strings.TrimSpace(m[1])
		content = strings.TrimSpace(recurrenceRe.ReplaceAllString(content, ""))
	}

	// Extract the [parent:: qi-…] inline field before tag parsing so the link
	// never leaks into Text. An unknown/dangling parent id is preserved as-is —
	// the parent may live in another file or be created later (never auto-drop).
	parentID := ""
	if m := parentRe.FindStringSubmatch(content); len(m) == 2 {
		parentID = m[1]
		content = strings.TrimSpace(parentRe.ReplaceAllString(content, ""))
	}

	tags := make([]string, 0)
	project := ""
	for _, m := range tagRe.FindAllStringSubmatch(content, -1) {
		tag := m[1]
		tags = append(tags, tag)
		if project == "" {
			project = tag
		}
	}
	content = strings.TrimSpace(content)

	return domain.Task{
		ID:          taskID,
		Text:        content,
		Project:     project,
		Tags:        tags,
		Due:         due,
		Scheduled:   scheduled,
		Recurrence:  recurrence,
		ParentID:    parentID,
		Completed:   completed,
		CompletedAt: completedAt,
	}, true, nil
}

func FormatTaskLine(task domain.Task) (string, error) {
	text := strings.TrimSpace(task.Text)
	if text == "" {
		return "", errors.New("task text is required")
	}

	status := " "
	if task.Completed {
		status = "x"
	}

	parts := []string{fmt.Sprintf("- [%s] %s", status, text)}

	// ParseTaskLine does not strip inline #tags from Text, so a parsed task
	// carries each tag in BOTH Text and Tags. Re-appending those would
	// duplicate them on every rewrite and compound on each subsequent
	// read/format cycle. Collect tags already inline in Text and skip them.
	inline := make(map[string]struct{})
	for _, m := range tagRe.FindAllStringSubmatch(text, -1) {
		inline[m[1]] = struct{}{}
	}

	hasProjectTag := false
	for _, tag := range task.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if tag == task.Project {
			hasProjectTag = true
		}
		if _, dup := inline[tag]; dup {
			continue
		}
		parts = append(parts, "#"+tag)
		inline[tag] = struct{}{}
	}

	if task.Project != "" && !hasProjectTag {
		if _, dup := inline[task.Project]; !dup {
			parts = append(parts, "#"+task.Project)
		}
	}

	// Recurrence is plain rule text (🔁 already stripped on parse); re-prepend the
	// emoji. Canonical Obsidian order: after tags, before ⏳ scheduled / 📅 due.
	if task.Recurrence != "" {
		parts = append(parts, "🔁 "+strings.TrimSpace(task.Recurrence))
	}

	if task.Scheduled != nil {
		parts = append(parts, "⏳ "+task.Scheduled.Format("2006-01-02"))
	}

	if task.Due != nil {
		parts = append(parts, "📅 "+task.Due.Format("2006-01-02"))
	}

	// Obsidian Tasks done-date: emitted after 📅 due, before the block ref, when
	// the task carries a completion date (CompleteTask stamps it).
	if task.CompletedAt != nil {
		parts = append(parts, "✅ "+task.CompletedAt.Format("2006-01-02"))
	}

	// Subtask link: the parent's qi id as a Dataview inline field. Canonical
	// slot is after ✅ done-date and before the trailing ^qi-id block ref (which
	// must stay last, anchored by idRe's `$`). See docs/subtasks-design.md.
	if task.ParentID != "" {
		parts = append(parts, "[parent:: "+task.ParentID+"]")
	}

	// Append qi block-ref ID as the very last token when the task carries one.
	// Obsidian allows exactly ONE block ref per block. If the text already ends
	// with any ^ref (foreign ref — not a qi id), refuse to append a second one.
	if task.ID != "" {
		joined := strings.Join(parts, " ")
		if anyBlockRe.MatchString(joined) && !idRe.MatchString(joined) {
			// Foreign block ref present — refuse to manage, emit without id.
			return joined, nil
		}
		// Strip any existing qi id from the joined line before appending, so
		// re-formatting an already-formatted line never doubles the id.
		joined = strings.TrimSpace(idRe.ReplaceAllString(joined, ""))
		return joined + " ^" + task.ID, nil
	}

	return strings.Join(parts, " "), nil
}

func EnsureTaskFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create task directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("create task file: %w", err)
	}
	return f.Close()
}

func AppendTask(path string, task domain.Task) error {
	if err := EnsureTaskFile(path); err != nil {
		return err
	}
	line, err := FormatTaskLine(task)
	if err != nil {
		return err
	}

	// Locked: an unlocked O_APPEND would write to the current inode, which a
	// concurrent UpdateTaskLine could rename away underneath us, losing the
	// append. The shared lock serializes both mutators.
	return withFileLock(path, func() error {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open task file: %w", err)
		}
		defer f.Close()

		if _, err := f.WriteString(line + "\n"); err != nil {
			return fmt.Errorf("append task: %w", err)
		}
		return nil
	})
}

func UpdateTaskLine(path string, lineNo int, task domain.Task) error {
	if lineNo < 1 {
		return fmt.Errorf("invalid line number: %d", lineNo)
	}

	// The read-guard-write below must be atomic against other qi processes, or
	// two concurrent updates to different lines would each rewrite the whole
	// file from a stale snapshot and clobber each other's change. Hold the lock
	// for the entire critical section, not just the write.
	return withFileLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read task file: %w", err)
		}

		lines := strings.Split(string(data), "\n")
		idx := lineNo - 1
		if idx >= len(lines) {
			return fmt.Errorf("line %d out of range (file has %d lines)", lineNo, len(lines))
		}

		// TOCTOU guard. The caller captured lineNo at ReadTasks time; the file
		// may have changed since (another qi process, an Obsidian edit, a sync
		// pull). Confirm the target line still parses to the same task before
		// overwriting it, so we never flip the checkbox on the wrong line.
		//
		// When task.ID is set, match by ID — this lets a renamed task still be
		// updated correctly (the ID is stable even when the text changes).
		// When task.ID is empty, fall back to the legacy Text-match behaviour.
		existing, ok, perr := ParseTaskLine(lines[idx])
		if perr != nil {
			return fmt.Errorf("verify line %d: %w", lineNo, perr)
		}
		if !ok {
			return fmt.Errorf("line %d is no longer a task line; refusing to overwrite", lineNo)
		}
		if task.ID != "" {
			if existing.ID != task.ID {
				return fmt.Errorf("line %d ID changed since read (have %q, want %q); refusing to overwrite", lineNo, existing.ID, task.ID)
			}
		} else {
			if existing.Text != task.Text {
				return fmt.Errorf("line %d changed since read (have %q, want %q); refusing to overwrite", lineNo, existing.Text, task.Text)
			}
		}

		newLine, err := FormatTaskLine(task)
		if err != nil {
			return err
		}

		lines[idx] = newLine
		return writeFileAtomic(path, []byte(strings.Join(lines, "\n")), 0o644)
	})
}

func ReadTasks(path string) ([]domain.Task, error) {
	if err := EnsureTaskFile(path); err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open task file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	tasks := make([]domain.Task, 0)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		task, ok, err := ParseTaskLine(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("parse line %d: %w", lineNo, err)
		}
		if !ok {
			continue
		}
		task.FilePath = path
		task.LineNumber = lineNo
		tasks = append(tasks, task)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan task file: %w", err)
	}
	return tasks, nil
}
