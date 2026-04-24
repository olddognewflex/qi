package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qi/internal/domain"
)

func sanitizeFilename(title string) string {
	title = strings.TrimSpace(title)
	title = strings.ToLower(title)
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := b.String()
	s = strings.Trim(s, "-")
	s = strings.ReplaceAll(s, "--", "-")
	if s == "" {
		s = "untitled"
	}
	return s
}

func WriteNote(notesDir, title, body string) (string, error) {
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		return "", fmt.Errorf("create notes directory: %w", err)
	}

	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("note title is empty")
	}

	filename := sanitizeFilename(title) + ".md"
	path := filepath.Join(notesDir, filename)

	_, err := os.Stat(path)
	if err == nil {
		base := sanitizeFilename(title)
		now := time.Now().Format("20060102-150405")
		filename = fmt.Sprintf("%s-%s.md", base, now)
		path = filepath.Join(notesDir, filename)
	}

	content := fmt.Sprintf("# %s\n\n%s\n", title, strings.TrimSpace(body))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write note: %w", err)
	}
	return path, nil
}

func ReadNote(path string) (domain.Note, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return domain.Note{}, "", fmt.Errorf("stat note: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Note{}, "", fmt.Errorf("read note: %w", err)
	}

	title := extractTitle(string(data))
	tags := extractTags(string(data))

	note := domain.Note{
		Path:       path,
		Title:      title,
		Tags:       tags,
		ModifiedAt: info.ModTime(),
	}
	return note, string(data), nil
}

func extractTitle(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func extractTags(content string) []string {
	var tags []string
	for _, m := range tagRe.FindAllStringSubmatch(content, -1) {
		tags = append(tags, m[1])
	}
	return tags
}

func ListNotes(notesDir string) ([]domain.Note, error) {
	entries, err := os.ReadDir(notesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []domain.Note{}, nil
		}
		return nil, fmt.Errorf("read notes directory: %w", err)
	}

	var notes []domain.Note
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(notesDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		title := strings.TrimSuffix(entry.Name(), ".md")
		title = strings.ReplaceAll(title, "-", " ")
		notes = append(notes, domain.Note{
			Path:       path,
			Title:      title,
			ModifiedAt: info.ModTime(),
		})
	}
	return notes, nil
}

func WalkVault(vaultPath string, fn func(path string, content string) error) error {
	return filepath.WalkDir(vaultPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".obsidian" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		return fn(path, string(data))
	})
}
