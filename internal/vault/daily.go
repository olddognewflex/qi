package vault

import (
	"os"
	"strings"
)

// managedOrder is the canonical ordering of qi-managed daily-note sections.
// When a section is created, it is placed to respect this order: a new managed
// section is inserted immediately above "## Logs" if Logs exists, otherwise at
// the end of the file. qi never reorders or rewrites content it did not create.
const logsHeading = "Logs"

// ReadSection returns the body text between "## heading" and the next "## "
// line (or EOF). found reports whether the heading exists. The body excludes
// the heading line itself; leading and trailing blank lines are trimmed.
func ReadSection(path, heading string) (body string, found bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}

	lines := strings.Split(string(data), "\n")
	start, end := sectionBounds(lines, heading)
	if start < 0 {
		return "", false, nil
	}

	bodyLines := lines[start+1 : end]
	return strings.Trim(strings.Join(bodyLines, "\n"), "\n"), true, nil
}

// ReplaceSection overwrites the body of the named section. If the section does
// not exist it is created using the managed-placement rule. A trailing newline
// at EOF is preserved.
func ReplaceSection(path, heading, body string) error {
	data, _ := os.ReadFile(path)
	lines := splitKeep(string(data))

	start, end := sectionBounds(lines, heading)
	newSection := renderSection(heading, body)

	var out []string
	if start < 0 {
		out = insertManaged(lines, heading, newSection)
	} else {
		out = append(out, lines[:start]...)
		out = append(out, newSection...)
		out = append(out, lines[end:]...)
	}

	return writeLines(path, out)
}

// AppendToSection appends line as a new line within the named section,
// creating the section on demand using the managed-placement rule.
func AppendToSection(path, heading, line string) error {
	body, found, err := ReadSection(path, heading)
	if err != nil {
		return err
	}
	if found && body != "" {
		body = body + "\n" + line
	} else {
		body = line
	}
	return ReplaceSection(path, heading, body)
}

// sectionBounds finds the index of the "## heading" line and the index of the
// next "## " line (exclusive end). Returns (-1, -1) if the heading is absent.
func sectionBounds(lines []string, heading string) (start, end int) {
	want := "## " + heading
	start = -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t") == want {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	end = len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return start, end
}

// renderSection produces the lines for a managed section: a heading line, the
// body, and a single trailing blank line for readability between sections.
func renderSection(heading, body string) []string {
	out := []string{"## " + heading}
	body = strings.Trim(body, "\n")
	if body != "" {
		out = append(out, strings.Split(body, "\n")...)
	}
	out = append(out, "")
	return out
}

// insertManaged places a new managed section above "## Logs" if it exists,
// otherwise at the end of the file.
func insertManaged(lines []string, heading string, section []string) []string {
	// Empty/whitespace-only file: just the section.
	if isBlank(lines) {
		return section
	}

	if heading != logsHeading {
		if idx := headingIndex(lines, logsHeading); idx >= 0 {
			var out []string
			out = append(out, lines[:idx]...)
			out = append(out, section...)
			out = append(out, lines[idx:]...)
			return out
		}
	}

	// Append at end. Ensure exactly one blank line separates prior content.
	out := trimTrailingBlanks(lines)
	out = append(out, "")
	out = append(out, section...)
	return out
}

func headingIndex(lines []string, heading string) int {
	want := "## " + heading
	for i, l := range lines {
		if strings.TrimRight(l, " \t") == want {
			return i
		}
	}
	return -1
}

// splitKeep splits content into lines. An empty file yields no lines so a fresh
// section can be written cleanly. A trailing newline produces a final empty
// element which is normalized away here and re-added on write.
func splitKeep(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	// Drop a single trailing empty element produced by a trailing newline.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func isBlank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

func trimTrailingBlanks(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[:end]
}

// writeLines joins lines with newlines and writes the file with a single
// trailing newline at EOF.
func writeLines(path string, lines []string) error {
	lines = trimTrailingBlanks(lines)
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
