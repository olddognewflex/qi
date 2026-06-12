package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qi/internal/domain"
	"qi/internal/service"
	"qi/internal/tools"
)

// QuickTaskToolName is the registered name for the mutating quick-task skill.
// It composes task creation with an open-task listing so a single gated call
// adds a task AND returns the refreshed open list — the value over the bare
// task.add builtin. It is declared Mutating so non-cli callers (ai-planner,
// mcp) route through the approval queue.
const QuickTaskToolName = "skill.quick-task"

// quickTaskInput is the JSON shape skill.quick-task accepts. Only text is
// required; the rest mirror the task.add builtin's optional fields, plus an
// optional Obsidian recurrence rule.
type quickTaskInput struct {
	Text      string `json:"text"`
	Project   string `json:"project"`
	Due       string `json:"due"`
	Scheduled string `json:"scheduled"`
	Repeat    string `json:"repeat"`
}

// quickTaskOutput is the JSON shape returned by skill.quick-task: the created
// task plus the post-create open-task list (the composition this skill adds).
type quickTaskOutput struct {
	Created   createdTask `json:"created"`
	OpenCount int         `json:"open_count"`
	OpenTasks []openTask  `json:"open_tasks"`
}

type createdTask struct {
	Text       string `json:"text"`
	Project    string `json:"project,omitempty"`
	Due        string `json:"due,omitempty"`
	Scheduled  string `json:"scheduled,omitempty"`
	Recurrence string `json:"recurrence,omitempty"`
	File       string `json:"file,omitempty"`
}

type openTask struct {
	Text      string `json:"text"`
	Project   string `json:"project,omitempty"`
	Due       string `json:"due,omitempty"`
	Scheduled string `json:"scheduled,omitempty"`
}

// RegisterQuickTask binds the mutating skill.quick-task tool against the
// supplied TaskService.
func RegisterQuickTask(r *tools.Registry, tasks service.TaskService) error {
	tool := tools.Tool{
		Name:        QuickTaskToolName,
		Version:     "1",
		Mutating:    true,
		Description: "Add a task and return the refreshed open-task list in one gated call.",
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,` +
			`"required":["text"],` +
			`"properties":{` +
			`"text":{"type":"string","minLength":1,"description":"Task text. Whitespace-trimmed; must not be empty after trimming."},` +
			`"project":{"type":"string","description":"Optional project tag (becomes the first #tag and routes the task to its project file)."},` +
			`"due":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$","description":"Optional due date, YYYY-MM-DD."},` +
			`"scheduled":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$","description":"Optional scheduled date, YYYY-MM-DD."},` +
			`"repeat":{"type":"string","description":"Obsidian recurrence rule, e.g. 'every week'."}}}`),
	}
	handler := func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
		var in quickTaskInput
		if len(params) > 0 {
			if err := json.Unmarshal(params, &in); err != nil {
				return nil, fmt.Errorf("quick-task: invalid params: %w", err)
			}
		}
		out, err := addQuickTask(tasks, in)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return r.RegisterSkill(tool, handler)
}

// addQuickTask creates a task from the input and returns it alongside the
// refreshed open-task list. Empty text is rejected; due/scheduled are parsed as
// YYYY-MM-DD when non-empty. The open list preserves ListOpenTasks order (it
// already reads files in a stable order) — it is not re-sorted.
func addQuickTask(tasks service.TaskService, in quickTaskInput) (quickTaskOutput, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return quickTaskOutput{}, fmt.Errorf("quick-task: text is empty")
	}

	due, err := parseQuickDate("due", in.Due)
	if err != nil {
		return quickTaskOutput{}, err
	}
	scheduled, err := parseQuickDate("scheduled", in.Scheduled)
	if err != nil {
		return quickTaskOutput{}, err
	}

	created, err := tasks.CreateTask(service.AddTaskInput{
		Text:       text,
		Project:    in.Project,
		Due:        due,
		Scheduled:  scheduled,
		Recurrence: strings.TrimSpace(in.Repeat),
	})
	if err != nil {
		return quickTaskOutput{}, fmt.Errorf("quick-task: create task: %w", err)
	}

	open, err := tasks.ListOpenTasks()
	if err != nil {
		return quickTaskOutput{}, fmt.Errorf("quick-task: list open tasks: %w", err)
	}

	out := quickTaskOutput{
		Created:   toCreatedTask(created),
		OpenCount: len(open),
		OpenTasks: make([]openTask, 0, len(open)),
	}
	for _, t := range open {
		out.OpenTasks = append(out.OpenTasks, toOpenTask(t))
	}
	return out, nil
}

// parseQuickDate parses a YYYY-MM-DD date, returning nil for an empty value and
// wrapping a parse failure with the offending field name.
func parseQuickDate(field, v string) (*time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return nil, fmt.Errorf("quick-task: invalid %s date, use YYYY-MM-DD: %w", field, err)
	}
	return &t, nil
}

func toCreatedTask(t domain.Task) createdTask {
	out := createdTask{
		Text:       t.Text,
		Project:    t.Project,
		Recurrence: t.Recurrence,
		File:       t.FilePath,
	}
	if t.Due != nil {
		out.Due = t.Due.Format("2006-01-02")
	}
	if t.Scheduled != nil {
		out.Scheduled = t.Scheduled.Format("2006-01-02")
	}
	return out
}

func toOpenTask(t domain.Task) openTask {
	out := openTask{
		Text:    t.Text,
		Project: t.Project,
	}
	if t.Due != nil {
		out.Due = t.Due.Format("2006-01-02")
	}
	if t.Scheduled != nil {
		out.Scheduled = t.Scheduled.Format("2006-01-02")
	}
	return out
}
