package vault

import (
	"errors"
	"fmt"
	"strings"

	"qi/internal/domain"
)

func FormatTaskLine(task domain.Task) (string, error) {
	text := strings.TrimSpace(task.Text)
	if text == "" {
		return "", errors.New("task text is required")
	}

	status := " "
	if task.Completed {
		status = "x"
	}

	// The first tag is the project when parsed. Move an explicitly selected
	// project ahead of other inline tags, preserving the remaining text.
	if matches := tagRe.FindAllStringIndex(text, -1); task.Project != "" && len(matches) > 0 && text[matches[0][0]+1:matches[0][1]] != task.Project {
		for i := len(matches) - 1; i > 0; i-- {
			start, end := matches[i][0], matches[i][1]
			if text[start+1:end] == task.Project {
				if end < len(text) && text[end] == ' ' {
					end++
				}
				text = text[:start] + text[end:]
			}
		}
		first := matches[0][0]
		text = strings.TrimSpace(text[:first] + "#" + task.Project + " " + text[first:])
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

	if task.Project != "" {
		if _, present := inline[task.Project]; !present {
			parts = append(parts, "#"+task.Project)
			inline[task.Project] = struct{}{}
		}
	}

	for _, tag := range task.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, dup := inline[tag]; dup {
			continue
		}
		parts = append(parts, "#"+tag)
		inline[tag] = struct{}{}
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
