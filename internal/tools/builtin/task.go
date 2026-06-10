package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"qi/internal/service"
	"qi/internal/tools"
)

// TaskAddToolName is the registered name of the task creation tool.
const TaskAddToolName = "task.add"

// taskAddSchema is the JSON Schema for task.add input parameters. It mirrors
// the `qi task add` flags: free-form text plus optional project/client routing
// and due/scheduled dates. project and client are mutually exclusive.
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
      "description": "Free-form project tag (routes to <project>.md). Mutually exclusive with client."
    },
    "client": {
      "type": "string",
      "description": "Configured client name (validated; routes to the client task file). Mutually exclusive with project."
    },
    "due": {
      "type": "string",
      "pattern": "^\\d{4}-\\d{2}-\\d{2}$",
      "description": "Due date, YYYY-MM-DD."
    },
    "schedule": {
      "type": "string",
      "pattern": "^\\d{4}-\\d{2}-\\d{2}$",
      "description": "Scheduled date, YYYY-MM-DD."
    }
  },
  "required": ["text"],
  "additionalProperties": false
}`)

type taskAddParams struct {
	Text     string `json:"text"`
	Project  string `json:"project"`
	Client   string `json:"client"`
	Due      string `json:"due"`
	Schedule string `json:"schedule"`
}

type taskAddResult struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Project string `json:"project,omitempty"`
}

// ClientValidator reports whether name is a configured client. Passed in so
// this package need not depend on config; the daemon supplies a closure over
// Config.ClientByName.
type ClientValidator func(name string) bool

// RegisterTaskAdd binds task.add to the supplied task service. svc must have
// both TaskFilePath and TasksDir set so project routing works; without TasksDir
// every task lands in the inbox file regardless of project. validate may be nil
// (then any non-empty client name is rejected).
func RegisterTaskAdd(r *tools.Registry, svc service.TaskService, validate ClientValidator) error {
	if svc.TaskFilePath == "" {
		return fmt.Errorf("task.add: task file path is empty")
	}
	tool := tools.Tool{
		Name:        TaskAddToolName,
		Version:     "1",
		Mutating:    true,
		Description: "Create a task with a freshly minted ID, routed to its project file or the inbox.",
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
		// Reject control characters in text. A task line is a SINGLE markdown
		// line; an embedded newline (or other control char) from an untrusted
		// remote caller would forge additional task/markdown lines in the
		// canonical vault file. FormatTaskLine only trims edges, so this is the
		// trust boundary that must defend the invariant.
		if hasControlChar(p.Text) {
			return nil, fmt.Errorf("task.add: text contains control characters")
		}

		// --client and --project are mutually exclusive, mirroring the CLI.
		tag := strings.TrimSpace(p.Project)
		if tag != "" && !validProjectTag(tag) {
			// project becomes a #tag and a filename; constrain it to the tag
			// charset so it can neither break the line nor escape the tasks dir.
			return nil, fmt.Errorf("task.add: invalid project %q (allowed: letters, digits, _-/)", tag)
		}
		if c := strings.TrimSpace(p.Client); c != "" {
			if tag != "" {
				return nil, fmt.Errorf("task.add: project and client are mutually exclusive")
			}
			// client is trusted only after validation against configured names.
			if validate == nil || !validate(c) {
				return nil, fmt.Errorf("task.add: unknown client %q", c)
			}
			tag = c
		}

		due, err := parseTaskDate("due", p.Due)
		if err != nil {
			return nil, err
		}
		sched, err := parseTaskDate("schedule", p.Schedule)
		if err != nil {
			return nil, err
		}

		task, err := svc.CreateTask(service.AddTaskInput{
			Text:      p.Text,
			Project:   tag,
			Due:       due,
			Scheduled: sched,
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal(taskAddResult{ID: task.ID, Path: task.FilePath, Project: task.Project})
	}
	return r.RegisterLocal(tool, handler)
}

// hasControlChar reports whether s contains any ASCII control character
// (including CR/LF and tab). Task text must stay on one markdown line.
func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// projectTagRe is the charset a free-form project tag may use. It matches the
// vault tag charset (see vault.tagRe) so the tag round-trips and the derived
// filename (projectFileName flattens "/" to "-") cannot traverse directories.
var projectTagRe = regexp.MustCompile(`^[A-Za-z0-9_\-/]+$`)

func validProjectTag(s string) bool {
	return projectTagRe.MatchString(s)
}

func parseTaskDate(field, v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, fmt.Errorf("task.add: invalid %s date, use YYYY-MM-DD: %w", field, err)
	}
	return &t, nil
}
