package ai

import (
	"bytes"
	"context"
	"fmt"

	"qi/internal/approval"
)

// Resume continues a validated persisted conversation after every pending
// approval reaches a terminal state.
func (p *Planner) Resume(ctx context.Context, session Session) (RunResult, error) {
	if err := validateStoredSession(session); err != nil {
		return RunResult{}, fmt.Errorf("ai.Resume: %w", err)
	}
	p.sessionID = session.SessionID
	if session.Model != "" {
		p.model = session.Model
	}

	last := session.Messages[len(session.Messages)-1]
	resultsByCall := make(map[string]ToolResult, len(session.Results))
	for _, result := range session.Results {
		resultsByCall[result.CallID] = result
	}
	pendingByCall := make(map[string]PendingCall, len(session.Pending))
	for _, pending := range session.Pending {
		pendingByCall[pending.CallID] = pending
	}

	toolResults := make([]ToolResult, 0, len(last.ToolCalls))
	for _, call := range last.ToolCalls {
		if result, exists := resultsByCall[call.ID]; exists {
			toolResults = append(toolResults, result)
			continue
		}
		result, err := p.resolveApproval(ctx, call, pendingByCall[call.ID])
		if err != nil {
			return RunResult{}, err
		}
		toolResults = append(toolResults, result)
	}

	toolDefs, nameMap, err := p.tools(ctx)
	if err != nil {
		return RunResult{}, err
	}
	messages := append(session.Messages, Message{Role: RoleUser, ToolResults: toolResults})
	return p.loop(ctx, messages, toolDefs, nameMap)
}

func (p *Planner) resolveApproval(ctx context.Context, call ToolCall, pending PendingCall) (ToolResult, error) {
	record, err := p.qid.GetApproval(ctx, pending.ApprovalID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("ai.Resume: fetch approval %s: %w", pending.ApprovalID, err)
	}
	if err := p.validateApprovalBinding(call, pending, record); err != nil {
		return ToolResult{}, err
	}
	switch record.Status {
	case approval.StatusExecuted:
		return ToolResult{CallID: call.ID, Content: string(record.Result)}, nil
	case approval.StatusDenied:
		content := "the user denied this action"
		if record.Reason != "" {
			content += ": " + record.Reason
		}
		return ToolResult{CallID: call.ID, Content: content, IsError: true}, nil
	case approval.StatusFailed:
		content := "the approved action failed to execute"
		if record.Err != "" {
			content += ": " + record.Err
		}
		return ToolResult{CallID: call.ID, Content: content, IsError: true}, nil
	case approval.StatusPending, approval.StatusApproved:
		return ToolResult{}, fmt.Errorf("approval %s is not resolved yet (status %q); run 'qi ai approve %s' first", pending.ApprovalID, record.Status, pending.ApprovalID)
	default:
		return ToolResult{}, fmt.Errorf("approval %s has unsupported status %q", pending.ApprovalID, record.Status)
	}
}

func (p *Planner) validateApprovalBinding(call ToolCall, pending PendingCall, record approval.Pending) error {
	if record.ID != pending.ApprovalID {
		return fmt.Errorf("ai.Resume: approval id binding mismatch")
	}
	if record.Caller != p.Caller() {
		return fmt.Errorf("ai.Resume: approval caller binding mismatch")
	}
	if record.CallID != call.ID {
		return fmt.Errorf("ai.Resume: approval call id binding mismatch")
	}
	if record.ToolName != pending.ToolName {
		return fmt.Errorf("ai.Resume: approval tool binding mismatch")
	}
	callParams, err := canonicalJSON(call.Input)
	if err != nil {
		return fmt.Errorf("ai.Resume: canonicalize persisted params: %w", err)
	}
	recordParams, err := canonicalJSON(record.Params)
	if err != nil {
		return fmt.Errorf("ai.Resume: canonicalize approval params: %w", err)
	}
	if !bytes.Equal(callParams, recordParams) {
		return fmt.Errorf("ai.Resume: approval params binding mismatch")
	}
	return nil
}
