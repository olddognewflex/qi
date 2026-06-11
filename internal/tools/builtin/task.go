package builtin

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

// TaskAddToolName is the registered name of the task-add tool.
const TaskAddToolName = "task.add"

// TaskListToolName is the registered name of the task-list tool.
const TaskListToolName = "task.list"

const dateLayout = "2006-01-02"

var taskAddSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "minLength": 1,
      "description": "Task text. Whitespace-trimmed; must not be empty after trimming."
    },
    "project": {
      "type": "string",
      "description": "Optional project tag (becomes the first #tag and routes the task to its project file)."
    },
    "due": {
      "type": "string",
      "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$",
      "description": "Optional due date, YYYY-MM-DD."
    },
    "scheduled": {
      "type": "string",
      "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$",
      "description": "Optional scheduled date, YYYY-MM-DD."
    }
  },
  "required": ["text"],
  "additionalProperties": false
}`)

var taskListSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Optional substring filter on task text (case-insensitive)."
    },
    "project": {
      "type": "string",
      "description": "Optional exact project-tag filter."
    },
    "all": {
      "type": "boolean",
      "description": "Include completed tasks. Defaults to open tasks only."
    }
  },
  "additionalProperties": false
}`)

type taskAddParams struct {
	Text      string `json:"text"`
	Project   string `json:"project"`
	Due       string `json:"due"`
	Scheduled string `json:"scheduled"`
}

type taskResult struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Project   string `json:"project,omitempty"`
	Due       string `json:"due,omitempty"`
	Scheduled string `json:"scheduled,omitempty"`
	Completed bool   `json:"completed,omitempty"`
	Path      string `json:"path,omitempty"`
}

type taskListParams struct {
	Query   string `json:"query"`
	Project string `json:"project"`
	All     bool   `json:"all"`
}

type taskListResult struct {
	Tasks []taskResult `json:"tasks"`
}

// RegisterTaskAdd binds task.add against the supplied TaskService. The tool is
// mutating, so non-cli callers route through the approval queue before the
// task is written.
func RegisterTaskAdd(r *tools.Registry, tasks service.TaskService) error {
	tool := tools.Tool{
		Name:        TaskAddToolName,
		Version:     "1",
		Mutating:    true,
		Description: "Add a task to the vault. Optional project tag, due date, and scheduled date.",
		Schema:      taskAddSchema,
	}
	handler := func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p taskAddParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("task.add: parse params: %w", err)
		}
		if strings.TrimSpace(p.Text) == "" {
			return nil, fmt.Errorf("task.add: text is empty")
		}
		due, err := parseOptDate("due", p.Due)
		if err != nil {
			return nil, err
		}
		scheduled, err := parseOptDate("scheduled", p.Scheduled)
		if err != nil {
			return nil, err
		}
		task, err := tasks.CreateTask(service.AddTaskInput{
			Text:      p.Text,
			Project:   p.Project,
			Due:       due,
			Scheduled: scheduled,
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal(toTaskResult(task))
	}
	return r.RegisterLocal(tool, handler)
}

// RegisterTaskList binds task.list against the supplied TaskService. Read-only:
// returns open tasks by default, optionally filtered by project tag and/or a
// case-insensitive substring query.
func RegisterTaskList(r *tools.Registry, tasks service.TaskService) error {
	tool := tools.Tool{
		Name:        TaskListToolName,
		Version:     "1",
		Mutating:    false,
		Description: "List tasks from the vault. Open tasks by default; filterable by project and query.",
		Schema:      taskListSchema,
	}
	handler := func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p taskListParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("task.list: parse params: %w", err)
			}
		}

		var (
			all []domain.Task
			err error
		)
		if p.All {
			all, err = tasks.ListAllTasks()
		} else {
			all, err = tasks.ListOpenTasks()
		}
		if err != nil {
			return nil, err
		}

		if p.Project != "" {
			project := strings.TrimSpace(p.Project)
			filtered := all[:0:0]
			for _, t := range all {
				if t.Project == project {
					filtered = append(filtered, t)
				}
			}
			all = filtered
		}
		if q := strings.TrimSpace(p.Query); q != "" {
			all = service.FuzzyMatch(q, all)
		}

		out := taskListResult{Tasks: make([]taskResult, 0, len(all))}
		for _, t := range all {
			out.Tasks = append(out.Tasks, toTaskResult(t))
		}
		return json.Marshal(out)
	}
	return r.RegisterLocal(tool, handler)
}

func parseOptDate(field, v string) (*time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	t, err := time.Parse(dateLayout, v)
	if err != nil {
		return nil, fmt.Errorf("task.add: invalid %s date, use YYYY-MM-DD: %w", field, err)
	}
	return &t, nil
}

func toTaskResult(t domain.Task) taskResult {
	out := taskResult{
		ID:        t.ID,
		Text:      t.Text,
		Project:   t.Project,
		Completed: t.Completed,
		Path:      t.FilePath,
	}
	if t.Due != nil {
		out.Due = t.Due.Format(dateLayout)
	}
	if t.Scheduled != nil {
		out.Scheduled = t.Scheduled.Format(dateLayout)
	}
	return out
}
