package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"qi/internal/service"
	"qi/internal/tools"
)

// ProcessInboxToolName is the registered name for the read-only inbox triage
// skill. It scans 00-inbox/ captures and proposes, per item, whether the
// capture should become a task, a note, or simply be archived. It is purely
// read-only — it never writes to the vault. Applying a proposal goes through
// the separate, mutating ProcessInboxApply skill so the policy layer can gate
// it.
const ProcessInboxToolName = "skill.process-inbox"

// ProcessInboxApplyToolName is the registered name for the mutating companion
// skill that executes a single proposal. It is declared Mutating so non-cli
// callers (ai-planner, mcp) route through the approval queue.
const ProcessInboxApplyToolName = "skill.process-inbox-apply"

// Proposed actions emitted by skill.process-inbox and accepted by
// skill.process-inbox-apply. They mirror the service-layer constants that own
// the triage behaviour.
const (
	actionTask    = service.InboxActionTask
	actionNote    = service.InboxActionNote
	actionArchive = service.InboxActionArchive
)

// processInboxOutput is the JSON shape returned by skill.process-inbox.
type processInboxOutput struct {
	Count int         `json:"count"`
	Items []inboxItem `json:"items"`
}

type inboxItem struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
	Action  string `json:"proposed_action"`
	Reason  string `json:"reason"`
}

// RegisterProcessInbox binds the read-only skill.process-inbox tool. inboxDir
// is scanned (non-recursively) for *.md captures.
func RegisterProcessInbox(r *tools.Registry, inboxDir string) error {
	tool := tools.Tool{
		Name:        ProcessInboxToolName,
		Version:     "1",
		Mutating:    false,
		Description: "Triage 00-inbox captures: summarize each and propose task, note, or archive.",
		Schema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
	handler := func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		items, err := proposeInbox(inboxDir)
		if err != nil {
			return nil, err
		}
		return json.Marshal(processInboxOutput{Count: len(items), Items: items})
	}
	return r.RegisterSkill(tool, handler)
}

// proposeInbox triages inboxDir via the service layer and maps the result to
// the skill's JSON wire shape.
func proposeInbox(inboxDir string) ([]inboxItem, error) {
	svc := service.InboxService{InboxDir: inboxDir}
	listed, err := svc.List()
	if err != nil {
		return nil, err
	}
	items := make([]inboxItem, 0, len(listed))
	for _, it := range listed {
		items = append(items, inboxItem{
			Path:    it.Path,
			Summary: it.Summary,
			Action:  it.Action,
			Reason:  it.Reason,
		})
	}
	return items, nil
}

// inboxApplyInput is the JSON shape skill.process-inbox-apply accepts.
type inboxApplyInput struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Title   string `json:"title,omitempty"`
	Project string `json:"project,omitempty"`
}

// inboxApplyOutput reports what the mutation did.
type inboxApplyOutput struct {
	Action   string `json:"action"`
	Source   string `json:"source"`
	Created  string `json:"created,omitempty"`
	Archived string `json:"archived"`
}

// RegisterProcessInboxApply binds the mutating skill.process-inbox-apply tool.
// It executes a single proposal: create a task or note from the capture (and
// then archive it), or simply archive it. archiveDir receives the original
// capture after a successful apply.
func RegisterProcessInboxApply(r *tools.Registry, inboxDir, archiveDir string, tasks service.TaskService, notes service.NoteService) error {
	tool := tools.Tool{
		Name:        ProcessInboxApplyToolName,
		Version:     "1",
		Mutating:    true,
		Description: "Apply a process-inbox proposal: turn a capture into a task or note, then archive it (or just archive).",
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,` +
			`"required":["path","action"],` +
			`"properties":{` +
			`"path":{"type":"string","description":"absolute path to the capture inside the inbox"},` +
			`"action":{"type":"string","enum":["task","note","archive"]},` +
			`"title":{"type":"string","description":"optional title/text override for the task or note"},` +
			`"project":{"type":"string","description":"optional project tag for a task"}}}`),
	}
	handler := func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
		var in inboxApplyInput
		if len(params) > 0 {
			if err := json.Unmarshal(params, &in); err != nil {
				return nil, fmt.Errorf("process-inbox-apply: invalid params: %w", err)
			}
		}
		out, err := applyInbox(inboxDir, archiveDir, tasks, notes, in)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return r.RegisterSkill(tool, handler)
}

// applyInbox runs one proposal through the service layer and maps the result
// to the skill's JSON wire shape.
func applyInbox(inboxDir, archiveDir string, tasks service.TaskService, notes service.NoteService, in inboxApplyInput) (inboxApplyOutput, error) {
	svc := service.InboxService{
		InboxDir:   inboxDir,
		ArchiveDir: archiveDir,
		Tasks:      tasks,
		Notes:      notes,
	}
	out, err := svc.Apply(service.InboxApplyInput{
		Path:    in.Path,
		Action:  in.Action,
		Title:   in.Title,
		Project: in.Project,
	})
	if err != nil {
		return inboxApplyOutput{}, err
	}
	return inboxApplyOutput{
		Action:   out.Action,
		Source:   out.Source,
		Created:  out.Created,
		Archived: out.Archived,
	}, nil
}
