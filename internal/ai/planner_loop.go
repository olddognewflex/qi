package ai

import (
	"context"
	"fmt"
	"strings"

	"qi/internal/daemon/client"
	"qi/internal/tools"
)

type providerStateSource interface {
	ProviderState() (ProviderState, error)
}

func (p *Planner) tools(ctx context.Context) ([]ToolDef, map[string]string, error) {
	catalog, err := p.qid.ListTools(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("ai: list tools: %w", err)
	}
	return buildToolDefs(catalog)
}

func (p *Planner) loop(ctx context.Context, messages []Message, toolDefs []ToolDef, nameMap map[string]string) (RunResult, error) {
	result := RunResult{}
	for iteration := 0; iteration < p.maxIterations; iteration++ {
		resp, err := p.llm.Generate(ctx, GenerateRequest{
			Model: p.model, System: systemPrompt(p.Caller()), Messages: messages,
			Tools: toolDefs, MaxTokens: p.maxTokens, CacheSystem: true,
		})
		if err != nil {
			return result, fmt.Errorf("ai: generate: %w", err)
		}
		result.CacheUsage.InputTokens += resp.Usage.InputTokens
		result.CacheUsage.OutputTokens += resp.Usage.OutputTokens
		result.CacheUsage.CacheCreationTokens += resp.Usage.CacheCreationTokens
		result.CacheUsage.CacheReadTokens += resp.Usage.CacheReadTokens

		turn := TurnEvent{Iteration: iteration, Text: strings.TrimSpace(resp.Text)}
		if len(resp.ToolCalls) == 0 {
			result.Turns = append(result.Turns, turn)
			result.FinalText = turn.Text
			result.StopReason = resp.StopReason
			return result, nil
		}

		canonicalCalls, err := canonicalToolCalls(resp.ToolCalls)
		if err != nil {
			return result, fmt.Errorf("ai: invalid tool calls: %w", err)
		}
		toolResults, records, pendings := p.runToolCalls(ctx, canonicalCalls, nameMap)
		turn.ToolCalls = records
		result.Turns = append(result.Turns, turn)
		messages = append(messages, Message{Role: RoleAssistant, Text: resp.Text, ToolCalls: canonicalCalls})

		if len(pendings) > 0 {
			result.Pending = append(result.Pending, pendings...)
			resolved := resolvedResults(toolResults, pendings)
			stateSource, ok := p.llm.(providerStateSource)
			if !ok {
				return result, fmt.Errorf("ai: provider does not expose resumable state")
			}
			providerState, err := stateSource.ProviderState()
			if err != nil {
				return result, fmt.Errorf("ai: snapshot provider state: %w", err)
			}
			session := Session{
				Version: SessionVersion, SessionID: p.sessionID, Model: p.model,
				Provider: providerState, Messages: messages, Results: resolved, Pending: pendings,
			}
			if err := session.Save(); err != nil {
				return result, fmt.Errorf("ai: persist session: %w", err)
			}
			result.SessionID = p.sessionID.String()
			result.StopReason = StopAwaitingApproval
			return result, nil
		}
		messages = append(messages, Message{Role: RoleUser, ToolResults: toolResults})
	}
	result.StopReason = "max_iterations"
	return result, nil
}

func canonicalToolCalls(calls []ToolCall) ([]ToolCall, error) {
	canonical := make([]ToolCall, 0, len(calls))
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if err := validateCallID(call.ID); err != nil {
			return nil, fmt.Errorf("call id %q: %w", call.ID, err)
		}
		if _, exists := seen[call.ID]; exists {
			return nil, fmt.Errorf("duplicate call id %q", call.ID)
		}
		input, err := canonicalJSON(call.Input)
		if err != nil {
			return nil, fmt.Errorf("call %q input: %w", call.ID, err)
		}
		call.Input = input
		canonical = append(canonical, call)
		seen[call.ID] = struct{}{}
	}
	return canonical, nil
}

func (p *Planner) runToolCalls(ctx context.Context, calls []ToolCall, nameMap map[string]string) ([]ToolResult, []ToolCallRecord, []PendingCall) {
	results := make([]ToolResult, 0, len(calls))
	records := make([]ToolCallRecord, 0, len(calls))
	pendings := make([]PendingCall, 0, len(calls))
	for _, call := range calls {
		qidName, ok := nameMap[call.Name]
		if !ok {
			qidName = call.Name
		}
		record := ToolCallRecord{Name: qidName, Input: call.Input, ResultID: call.ID}
		raw, err := p.qid.CallToolAsWithID(ctx, p.Caller(), call.ID, qidName, call.Input)
		if err != nil {
			record.Error = err.Error()
			results = append(results, ToolResult{CallID: call.ID, Content: "error: " + err.Error(), IsError: true})
		} else if pending, isPending := client.IsPending(raw); isPending {
			record.Pending = true
			pendings = append(pendings, PendingCall{
				CallID: call.ID, ApprovalID: pending.ApprovalID, ToolName: qidName, Reason: pending.Reason,
			})
			results = append(results, ToolResult{CallID: call.ID, Content: "awaiting approval " + pending.ApprovalID, IsError: true})
		} else {
			results = append(results, ToolResult{CallID: call.ID, Content: string(raw)})
		}
		records = append(records, record)
	}
	return results, records, pendings
}

func resolvedResults(results []ToolResult, pendings []PendingCall) []ToolResult {
	pending := make(map[string]struct{}, len(pendings))
	for _, call := range pendings {
		pending[call.CallID] = struct{}{}
	}
	resolved := make([]ToolResult, 0, len(results)-len(pendings))
	for _, result := range results {
		if _, exists := pending[result.CallID]; !exists {
			resolved = append(resolved, result)
		}
	}
	return resolved
}

func buildToolDefs(catalog []tools.Tool) ([]ToolDef, map[string]string, error) {
	out := make([]ToolDef, 0, len(catalog))
	nameMap := make(map[string]string, len(catalog))
	for _, tool := range catalog {
		apiName := sanitizeToolName(tool.Name)
		if previous, exists := nameMap[apiName]; exists {
			return nil, nil, fmt.Errorf("ai: sanitized name %q collides between %q and %q", apiName, previous, tool.Name)
		}
		nameMap[apiName] = tool.Name
		out = append(out, ToolDef{Name: apiName, Description: tool.Description, InputSchema: tool.Schema})
	}
	return out, nameMap, nil
}

func sanitizeToolName(name string) string { return strings.ReplaceAll(name, ".", "_") }

func systemPrompt(caller string) string {
	return "You are the Qi assistant's planner. You are calling tools as caller=\"" + caller + "\". " +
		"Mutating tools route through a human approval queue and return tool errors of the form " +
		"\"approval required (id=...)\". When you see one, tell the user verbatim which command to run, " +
		"then stop calling tools."
}
