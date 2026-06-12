package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qi/internal/tools"
	"qi/internal/vault"
)

// SessionLogToolName is the registered name for the mutating session-log skill.
// It appends a timestamped journal entry to today's daily note ## Logs — the
// headless equivalent of `qi daily cp`, minus the Obsidian EnsureNote step. It
// is declared Mutating so non-cli callers (ai-planner, mcp) route through the
// approval queue.
const SessionLogToolName = "skill.session-log"

// sessionLogInput is the JSON shape skill.session-log accepts.
type sessionLogInput struct {
	Text string `json:"text"`
}

// sessionLogOutput reports what was logged and where.
type sessionLogOutput struct {
	Logged string `json:"logged"`
	Path   string `json:"path"`
	Time   string `json:"time"` // HH:MM
}

// RegisterSessionLog binds the mutating skill.session-log tool. dailyPathFor
// resolves the daily-note path for a given day (typically cfg.DailyNotePath).
func RegisterSessionLog(r *tools.Registry, dailyPathFor func(time.Time) string) error {
	tool := tools.Tool{
		Name:        SessionLogToolName,
		Version:     "1",
		Mutating:    true,
		Description: "Append a timestamped entry to today's daily note ## Logs (headless qi daily cp).",
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,` +
			`"required":["text"],` +
			`"properties":{` +
			`"text":{"type":"string","minLength":1,"description":"Log entry text."}}}`),
	}
	handler := func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
		var in sessionLogInput
		if len(params) > 0 {
			if err := json.Unmarshal(params, &in); err != nil {
				return nil, fmt.Errorf("session-log: invalid params: %w", err)
			}
		}
		out, err := appendSessionLog(dailyPathFor, time.Now(), in.Text)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return r.RegisterSkill(tool, handler)
}

// appendSessionLog appends a timestamped line to the ## Logs section of the
// daily note for now. Empty text is rejected. now is injected so tests control
// the HH:MM stamp. AppendToSection creates the section/file on demand but does
// NOT create the parent directory, so it is ensured here first.
func appendSessionLog(dailyPathFor func(time.Time) string, now time.Time, text string) (sessionLogOutput, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return sessionLogOutput{}, fmt.Errorf("session-log: text is empty")
	}

	path := dailyPathFor(now)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return sessionLogOutput{}, fmt.Errorf("session-log: ensure daily dir: %w", err)
	}

	stamp := now.Format("15:04")
	line := "- [" + stamp + "] " + text
	if err := vault.AppendToSection(path, "Logs", line); err != nil {
		return sessionLogOutput{}, fmt.Errorf("session-log: append: %w", err)
	}

	return sessionLogOutput{Logged: text, Path: path, Time: stamp}, nil
}
