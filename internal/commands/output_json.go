package commands

import (
	"encoding/json"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/agentrt"
	"qi/internal/domain"
)

// This file defines the stable JSON schema for qi's read verbs (`task list`,
// `agenda`, `search`) under `--json`, for agent/script consumption. The shapes
// here are an API surface: field names and semantics should stay stable across
// releases. They are deliberately explicit DTOs rather than the raw domain
// types so internal fields (FilePath, LineNumber, ...) never leak and the
// on-wire format is decoupled from internal refactors.

// printJSON writes v as indented JSON to the command's stdout, followed by a
// newline. Callers must pass a non-nil slice for list outputs so an empty
// result encodes as `[]`, not `null`.
func printJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// dateJSON formats an optional date as YYYY-MM-DD (matching the CLI's date
// flags), or "" when unset so the field is omitted.
func dateJSON(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// taskJSON is the stable shape of a task for `qi task list --json`.
type taskJSON struct {
	ID          string   `json:"id"`
	Text        string   `json:"text"`
	Project     string   `json:"project,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Due         string   `json:"due,omitempty"`
	Scheduled   string   `json:"scheduled,omitempty"`
	Recurrence  string   `json:"recurrence,omitempty"`
	ParentID    string   `json:"parent_id,omitempty"`
	Priority    string   `json:"priority,omitempty"`
	Completed   bool     `json:"completed"`
	CompletedAt string   `json:"completed_at,omitempty"`
}

// tasksToJSON maps domain tasks to their stable JSON DTOs, always returning a
// non-nil slice so an empty result encodes as `[]`.
func tasksToJSON(tasks []domain.Task) []taskJSON {
	out := make([]taskJSON, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskJSON{
			ID:          t.ID,
			Text:        t.Text,
			Project:     t.Project,
			Tags:        t.Tags,
			Due:         dateJSON(t.Due),
			Scheduled:   dateJSON(t.Scheduled),
			Recurrence:  t.Recurrence,
			ParentID:    t.ParentID,
			Priority:    t.Priority,
			Completed:   t.Completed,
			CompletedAt: dateJSON(t.CompletedAt),
		})
	}
	return out
}

// eventJSON is the stable shape of a calendar event for `qi agenda --json`.
// Start/End are RFC3339 timestamps; End is omitted for zero-duration events.
type eventJSON struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Start   string `json:"start"`
	End     string `json:"end,omitempty"`
	Project string `json:"project,omitempty"`
}

// eventsToJSON maps domain events to their stable JSON DTOs, always returning a
// non-nil slice so an empty result encodes as `[]`.
func eventsToJSON(events []domain.Event) []eventJSON {
	out := make([]eventJSON, 0, len(events))
	for _, e := range events {
		end := ""
		if !e.End.IsZero() && e.End != e.Start {
			end = e.End.Format(time.RFC3339)
		}
		out = append(out, eventJSON{
			ID:      e.ID,
			Source:  e.Source,
			Title:   e.Title,
			Start:   e.Start.Format(time.RFC3339),
			End:     end,
			Project: e.Project,
		})
	}
	return out
}

// searchResultJSON is the stable shape of a search hit for `qi search --json`.
// Path is relative to the vault root; Rank is the cosine score under
// --semantic and the FTS rank otherwise.
type searchResultJSON struct {
	Kind  string  `json:"kind"`
	Path  string  `json:"path"`
	Match string  `json:"match"`
	Rank  float64 `json:"rank"`
}

// agentJSON is the stable shape of one agent instance for `qi agent list
// --json`. `id` is the runtime's opaque handle (for Herdr, the pane id);
// `session_id` is the agent's own session identity when the runtime knows it.
type agentJSON struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Name            string `json:"name,omitempty"`
	State           string `json:"state"`
	SessionID       string `json:"session_id,omitempty"`
	WorkspaceID     string `json:"workspace_id"`
	WorkspaceLabel  string `json:"workspace_label,omitempty"`
	WorkspaceNumber int    `json:"workspace_number,omitempty"`
	PaneID          string `json:"pane_id"`
	PaneLabel       string `json:"pane_label"`
	Cwd             string `json:"cwd,omitempty"`
	Focused         bool   `json:"focused"`
	Title           string `json:"title,omitempty"`
}

// agentListJSON wraps the agent list with the runtime that produced it, so a
// consumer can tell "no agents" from "no runtime".
type agentListJSON struct {
	Runtime string      `json:"runtime"`
	Agents  []agentJSON `json:"agents"`
}

func agentsToJSON(runtime string, agents []agentrt.AgentInstance) agentListJSON {
	out := make([]agentJSON, 0, len(agents))
	for _, a := range agents {
		out = append(out, agentJSON{
			ID:              string(a.ID),
			Kind:            string(a.Kind),
			Name:            a.Name,
			State:           string(a.State),
			SessionID:       a.SessionID,
			WorkspaceID:     a.Workspace.ID,
			WorkspaceLabel:  a.Workspace.Label,
			WorkspaceNumber: a.Workspace.Number,
			PaneID:          a.PaneID,
			PaneLabel:       a.PaneLabel(),
			Cwd:             a.Cwd,
			Focused:         a.Focused,
			Title:           a.Title,
		})
	}
	return agentListJSON{Runtime: runtime, Agents: out}
}
