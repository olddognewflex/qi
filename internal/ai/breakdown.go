package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Breakdown levels control how granular the AI subtask split is. They are the
// vocabulary shared by `qi task add --breakdown[=level]` (QI-4) and
// `qi task breakdown` (QI-5).
const (
	LevelCoarse = "coarse"
	LevelNormal = "normal"
	LevelFine   = "fine"
)

// DefaultBreakdownLevel is used when --breakdown is passed without a value.
const DefaultBreakdownLevel = LevelNormal

// breakdownMaxTokens caps the breakdown response. Subtask lists are short; this
// is generous enough for the "fine" level without inviting runaway output.
const breakdownMaxTokens = 1024

// levelGuidance maps a level to the granularity instruction handed to the model.
var levelGuidance = map[string]string{
	LevelCoarse: "Produce 2-4 broad subtasks covering the major phases of the work.",
	LevelNormal: "Produce 3-6 subtasks, each a concrete unit of work.",
	LevelFine:   "Produce 6-12 fine-grained subtasks, each a single focused step.",
}

// ValidLevel reports whether level is one of the supported breakdown levels.
// An empty string is treated as valid (it resolves to DefaultBreakdownLevel in
// NormalizeLevel).
func ValidLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", LevelCoarse, LevelNormal, LevelFine:
		return true
	default:
		return false
	}
}

// NormalizeLevel lowercases/trims a level and substitutes the default for an
// empty value. Callers should ValidLevel first; an unknown level normalizes to
// itself so the caller's validation error wins.
func NormalizeLevel(level string) string {
	l := strings.ToLower(strings.TrimSpace(level))
	if l == "" {
		return DefaultBreakdownLevel
	}
	return l
}

// Breakdown asks the LLM to split parentText into individually-implementable
// subtasks at the given level. It is provider-neutral (any LLM impl) and makes a
// single Generate call with no tools — the model returns a JSON array of strings
// which Breakdown parses and trims. It never writes anything; the caller is
// responsible for the confirmation gate before any vault mutation.
//
// model may be empty (the provider applies its own default). An empty or
// malformed model response, or zero parsed subtasks, is returned as an error so
// the caller can surface a clear message and leave the parent task intact.
func Breakdown(ctx context.Context, llm LLM, model, parentText, level string) ([]string, error) {
	parentText = strings.TrimSpace(parentText)
	if parentText == "" {
		return nil, fmt.Errorf("breakdown: empty task text")
	}
	level = NormalizeLevel(level)
	guidance, ok := levelGuidance[level]
	if !ok {
		return nil, fmt.Errorf("breakdown: unknown level %q (want coarse|normal|fine)", level)
	}

	system := "You break a single task into smaller subtasks. Each subtask must be " +
		"atomic and independently implementable by one person or agent: clear, " +
		"actionable, and self-contained. " + guidance + " " +
		"Respond with ONLY a JSON array of strings — the subtask titles, in order. " +
		"No prose, no numbering, no markdown, no code fences. Each string is a short " +
		"imperative task title without a trailing period."

	user := "Break down this task:\n\n" + parentText

	resp, err := llm.Generate(ctx, GenerateRequest{
		Model:     model,
		System:    system,
		Messages:  []Message{{Role: RoleUser, Text: user}},
		MaxTokens: breakdownMaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("breakdown: %w", err)
	}

	subtasks, err := parseSubtaskList(resp.Text)
	if err != nil {
		return nil, err
	}
	if len(subtasks) == 0 {
		return nil, fmt.Errorf("breakdown: model returned no subtasks")
	}
	return subtasks, nil
}

// parseSubtaskList extracts a JSON array of strings from a model response.
// Models sometimes wrap the array in prose or code fences despite instructions,
// so it isolates the outermost [...] span before unmarshalling. Blank entries
// are dropped and each is trimmed.
func parseSubtaskList(text string) ([]string, error) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return nil, fmt.Errorf("breakdown: empty model response")
	}

	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end < 0 || end < start {
		return nil, fmt.Errorf("breakdown: no JSON array in model response: %q", truncate(raw, 200))
	}

	var items []string
	if err := json.Unmarshal([]byte(raw[start:end+1]), &items); err != nil {
		return nil, fmt.Errorf("breakdown: parse subtask list: %w", err)
	}

	out := make([]string, 0, len(items))
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it != "" {
			out = append(out, it)
		}
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
