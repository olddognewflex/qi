package vault

import (
	"bufio"
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
	tagRe        = regexp.MustCompile(`#([A-Za-z0-9_\-\/]+)`)
)

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

	var due *time.Time
	if m := dueRe.FindStringSubmatch(content); len(m) == 2 {
		parsed, err := time.Parse("2006-01-02", m[1])
		if err != nil {
			return domain.Task{}, false, fmt.Errorf("parse due date: %w", err)
		}
		due = &parsed
		content = strings.TrimSpace(dueRe.ReplaceAllString(content, ""))
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
		Text:      content,
		Project:   project,
		Tags:      tags,
		Due:       due,
		Completed: completed,
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

	if task.Due != nil {
		parts = append(parts, "📅 "+task.Due.Format("2006-01-02"))
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
		existing, ok, perr := ParseTaskLine(lines[idx])
		if perr != nil {
			return fmt.Errorf("verify line %d: %w", lineNo, perr)
		}
		if !ok {
			return fmt.Errorf("line %d is no longer a task line; refusing to overwrite", lineNo)
		}
		if existing.Text != task.Text {
			return fmt.Errorf("line %d changed since read (have %q, want %q); refusing to overwrite", lineNo, existing.Text, task.Text)
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
