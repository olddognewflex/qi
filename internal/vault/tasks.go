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

	hasProjectTag := false
	for _, tag := range task.Tags {
		if strings.TrimSpace(tag) == "" {
			continue
		}
		parts = append(parts, "#"+tag)
		if tag == task.Project {
			hasProjectTag = true
		}
	}

	if task.Project != "" && !hasProjectTag {
		parts = append(parts, "#"+task.Project)
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

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open task file: %w", err)
	}
	defer f.Close()

	_, err = f.WriteString(line + "\n")
	if err != nil {
		return fmt.Errorf("append task: %w", err)
	}
	return nil
}

func UpdateTaskLine(path string, lineNo int, task domain.Task) error {
	if lineNo < 1 {
		return fmt.Errorf("invalid line number: %d", lineNo)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read task file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	idx := lineNo - 1
	if idx >= len(lines) {
		return fmt.Errorf("line %d out of range (file has %d lines)", lineNo, len(lines))
	}

	newLine, err := FormatTaskLine(task)
	if err != nil {
		return err
	}

	lines[idx] = newLine
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
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
