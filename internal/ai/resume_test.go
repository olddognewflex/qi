package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"qi/internal/approval"
	"qi/internal/tools"
	"qi/internal/tools/builtin"
)

// runToPending runs a prompt that proposes one mutating tool call, returning the
// planner result (stopped at the gate) and the queue for approving it.
func runToPending(t *testing.T) (RunResult, *approval.Queue) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	q := approval.NewQueue(nil)
	c := pipeDaemonAndClient(t, r, q)

	llm := &stubLLM{responses: []*GenerateResponse{
		toolUseResp(sanitizeToolName(builtin.CaptureToolName), "tu_1", `{ "text" : "remember milk" }`),
	}}
	p := newTestPlanner(t, c, llm)

	res, err := p.Run(context.Background(), "capture: remember milk")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, q
}

func TestRunPersistsSessionOnPending(t *testing.T) {
	res, queue := runToPending(t)

	if res.StopReason != StopAwaitingApproval {
		t.Fatalf("stop = %q, want %q", res.StopReason, StopAwaitingApproval)
	}
	if res.SessionID == "" {
		t.Fatal("SessionID should be set when awaiting approval")
	}
	if len(res.Pending) != 1 || res.Pending[0].CallID != "tu_1" || res.Pending[0].ApprovalID == "" {
		t.Fatalf("pending call not recorded with linkage: %+v", res.Pending)
	}

	sess, err := LoadSession(res.SessionID)
	if err != nil {
		t.Fatalf("session not persisted: %v", err)
	}
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != RoleAssistant || len(last.ToolCalls) != 1 {
		t.Fatalf("session must end at the assistant tool-call turn: %+v", last)
	}
	if len(sess.Pending) != 1 || sess.Pending[0].ApprovalID != res.Pending[0].ApprovalID {
		t.Fatalf("session pending mismatch: %+v", sess.Pending)
	}
	canonical, err := canonicalJSON(last.ToolCalls[0].Input)
	if err != nil || string(canonical) != `{"text":"remember milk"}` {
		t.Fatalf("persisted input = %s, canonical = %s, error = %v", last.ToolCalls[0].Input, canonical, err)
	}
	queued, ok := queue.Get(res.Pending[0].ApprovalID)
	if !ok || string(queued.Params) != `{"text":"remember milk"}` {
		t.Fatalf("queued params = %s, want exact canonical input", queued.Params)
	}
}

func TestResumeExecutedResultFeedsExactOutput(t *testing.T) {
	res, q := runToPending(t)
	approvalID := res.Pending[0].ApprovalID

	// Simulate the human approving and the tool executing server-side.
	if _, err := q.Approve(approvalID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	execResult := json.RawMessage(`{"status":"captured","path":"00-inbox/x.md"}`)
	if _, err := q.RecordResult(approvalID, execResult, nil); err != nil {
		t.Fatalf("record result: %v", err)
	}

	sess, err := LoadSession(res.SessionID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	sess.Messages[len(sess.Messages)-1].ToolCalls[0].Input = json.RawMessage(`{ "text" : "remember milk" }`)

	// A fresh planner + client over the same queue resumes the loop.
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	c := pipeDaemonAndClient(t, r, q)
	llm := &stubLLM{responses: []*GenerateResponse{textResp("captured it and we're done")}}
	p := newTestPlanner(t, c, llm)

	out, err := p.Resume(context.Background(), sess)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if out.StopReason == StopAwaitingApproval {
		t.Fatalf("resume should have progressed past the gate, got %q", out.StopReason)
	}
	if out.FinalText != "captured it and we're done" {
		t.Errorf("final = %q", out.FinalText)
	}

	// The model's resumed turn must have received the executed result, not the
	// "awaiting approval" placeholder.
	if len(llm.captured) == 0 {
		t.Fatal("resume did not call the model")
	}
	msgs := llm.captured[0].Messages
	lastUser := msgs[len(msgs)-1]
	if lastUser.Role != RoleUser || len(lastUser.ToolResults) != 1 {
		t.Fatalf("resumed turn should end with a tool-results message: %+v", lastUser)
	}
	if !strings.Contains(lastUser.ToolResults[0].Content, "captured") {
		t.Errorf("tool result fed back = %q, want the executed output", lastUser.ToolResults[0].Content)
	}
	if lastUser.ToolResults[0].IsError {
		t.Error("approved result should not be an error")
	}
}

func TestResumePendingAndApprovedRemainUnresolved(t *testing.T) {
	res, q := runToPending(t)
	sess, err := LoadSession(res.SessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	c := pipeDaemonAndClient(t, r, q)
	llm := &stubLLM{responses: []*GenerateResponse{textResp("x")}}
	p := NewForResume(c, llm, sess.SessionID)

	if _, err := p.Resume(context.Background(), sess); err == nil {
		t.Fatal("pending approval should remain unresolved")
	}
	if _, err := q.Approve(res.Pending[0].ApprovalID); err != nil {
		t.Fatalf("approve without terminal result: %v", err)
	}
	if _, err := p.Resume(context.Background(), sess); err == nil {
		t.Fatal("approved approval without terminal result should remain unresolved")
	}
	if len(llm.captured) != 0 {
		t.Fatalf("LLM calls = %d, want 0", len(llm.captured))
	}
}

func TestResumeRejectsUnknownApprovalDistinctly(t *testing.T) {
	res, _ := runToPending(t)
	sess, err := LoadSession(res.SessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	llm := &stubLLM{responses: []*GenerateResponse{textResp("unexpected")}}
	p := NewForResume(pipeDaemonAndClient(t, tools.NewRegistry(), approval.NewQueue(nil)), llm, sess.SessionID)

	_, err = p.Resume(context.Background(), sess)
	if err == nil || !strings.Contains(err.Error(), "approval id not found") {
		t.Fatalf("error = %v, want unknown approval detail", err)
	}
	if len(llm.captured) != 0 {
		t.Fatalf("LLM calls = %d, want 0", len(llm.captured))
	}
}

func TestResumeDeniedResultIncludesReason(t *testing.T) {
	res, q := runToPending(t)
	if _, err := q.Deny(res.Pending[0].ApprovalID, "not today"); err != nil {
		t.Fatalf("deny: %v", err)
	}
	session, err := LoadSession(res.SessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	llm := &stubLLM{responses: []*GenerateResponse{textResp("done")}}
	p := NewForResume(pipeDaemonAndClient(t, tools.NewRegistry(), q), llm, session.SessionID)

	if _, err := p.Resume(context.Background(), session); err != nil {
		t.Fatalf("resume: %v", err)
	}
	result := llm.captured[0].Messages[len(llm.captured[0].Messages)-1].ToolResults[0]
	if !result.IsError || !strings.Contains(result.Content, "not today") {
		t.Fatalf("denial result = %+v", result)
	}
}

func TestResumeFailedResultIncludesExecutionError(t *testing.T) {
	res, q := runToPending(t)
	approvalID := res.Pending[0].ApprovalID
	if _, err := q.Approve(approvalID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := q.RecordResult(approvalID, nil, errors.New("disk full")); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	session, err := LoadSession(res.SessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	llm := &stubLLM{responses: []*GenerateResponse{textResp("done")}}
	p := NewForResume(pipeDaemonAndClient(t, tools.NewRegistry(), q), llm, session.SessionID)

	if _, err := p.Resume(context.Background(), session); err != nil {
		t.Fatalf("resume: %v", err)
	}
	result := llm.captured[0].Messages[len(llm.captured[0].Messages)-1].ToolResults[0]
	if !result.IsError || !strings.Contains(result.Content, "disk full") {
		t.Fatalf("failure result = %+v", result)
	}
}

func TestResumeMultiplePendingPreservesTerminalCallOrder(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	registry := tools.NewRegistry()
	for _, name := range []string{"mutate.one", "mutate.two"} {
		if err := registry.RegisterLocal(tools.Tool{Name: name, Mutating: true, Schema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	queue := approval.NewQueue(nil)
	client := pipeDaemonAndClient(t, registry, queue)
	llm := &stubLLM{responses: []*GenerateResponse{{StopReason: "tool_use", ToolCalls: []ToolCall{
		{ID: "call-1", Name: "mutate_one", Input: json.RawMessage(`{"value":1}`)},
		{ID: "call-2", Name: "mutate_two", Input: json.RawMessage(`{"value":2}`)},
	}}}}
	planner := newTestPlanner(t, client, llm)
	result, err := planner.Run(context.Background(), "do both")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(result.Pending))
	}
	if _, err := queue.Approve(result.Pending[0].ApprovalID); err != nil {
		t.Fatalf("approve first: %v", err)
	}
	if _, err := queue.RecordResult(result.Pending[0].ApprovalID, json.RawMessage(`{"first":true}`), nil); err != nil {
		t.Fatalf("execute first: %v", err)
	}
	if _, err := queue.Deny(result.Pending[1].ApprovalID, "second denied"); err != nil {
		t.Fatalf("deny second: %v", err)
	}
	session, err := LoadSession(result.SessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	resumeLLM := &stubLLM{responses: []*GenerateResponse{textResp("done")}}
	resumePlanner := NewForResume(client, resumeLLM, session.SessionID)

	if _, err := resumePlanner.Resume(context.Background(), session); err != nil {
		t.Fatalf("resume: %v", err)
	}
	results := resumeLLM.captured[0].Messages[len(resumeLLM.captured[0].Messages)-1].ToolResults
	if len(results) != 2 || results[0].CallID != "call-1" || results[1].CallID != "call-2" {
		t.Fatalf("result order = %+v", results)
	}
	if results[0].IsError || !results[1].IsError || !strings.Contains(results[1].Content, "second denied") {
		t.Fatalf("terminal results = %+v", results)
	}
}

func TestResumeSecondApprovalKeepsSessionIDAndCurrentProviderState(t *testing.T) {
	result, queue := runToPending(t)
	firstApproval := result.Pending[0].ApprovalID
	if _, err := queue.Approve(firstApproval); err != nil {
		t.Fatalf("approve first: %v", err)
	}
	if _, err := queue.RecordResult(firstApproval, json.RawMessage(`{"first":true}`), nil); err != nil {
		t.Fatalf("execute first: %v", err)
	}
	session, err := LoadSession(result.SessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	registry := tools.NewRegistry()
	if err := builtin.RegisterCapture(registry, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	currentState := ProviderState{Version: ProviderStateVersion, Active: 1, Entries: []ProviderStateEntry{
		{Provider: ProviderOllama, Model: "local", ConfigID: strings.Repeat("c", 64)},
		{Provider: ProviderAnthropic, Model: "remote", ConfigID: strings.Repeat("d", 64)},
	}}
	resumeLLM := &stubLLM{state: currentState, responses: []*GenerateResponse{
		toolUseResp(sanitizeToolName(builtin.CaptureToolName), "call-2", `{"text":"second"}`),
	}}
	planner := NewForResume(pipeDaemonAndClient(t, registry, queue), resumeLLM, session.SessionID)

	second, err := planner.Resume(context.Background(), session)
	if err != nil {
		t.Fatalf("resume to second gate: %v", err)
	}
	if second.StopReason != StopAwaitingApproval || second.SessionID != result.SessionID {
		t.Fatalf("second gate = %+v, original session = %s", second, result.SessionID)
	}
	saved, err := LoadSession(result.SessionID)
	if err != nil {
		t.Fatalf("load second gate: %v", err)
	}
	if saved.Provider.Active != currentState.Active || len(saved.Provider.Entries) != len(currentState.Entries) || saved.Provider.Entries[0] != currentState.Entries[0] || saved.Provider.Entries[1] != currentState.Entries[1] {
		t.Fatalf("provider snapshot = %+v, want %+v", saved.Provider, currentState)
	}
}

func TestSessionValidationRejectsMalformedFinalCallIDs(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
	}{
		{name: "empty", ids: []string{""}},
		{name: "duplicate", ids: []string{"call-1", "call-1"}},
		{name: "whitespace", ids: []string{"call 1"}},
		{name: "control", ids: []string{"call\n1"}},
		{name: "invalid utf8", ids: []string{string([]byte{0xff})}},
		{name: "too long", ids: []string{strings.Repeat("x", 257)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := make([]ToolCall, 0, len(tt.ids))
			pending := make([]PendingCall, 0, len(tt.ids))
			for i, id := range tt.ids {
				calls = append(calls, ToolCall{ID: id, Name: "task_add", Input: json.RawMessage(`{"text":"x"}`)})
				pending = append(pending, PendingCall{CallID: id, ApprovalID: fmt.Sprintf("approval-%d", i), ToolName: "task.add"})
			}
			sess := Session{
				Version: SessionVersion, SessionID: mustSessionID(t, testSessionID),
				Provider: testProviderState(),
				Messages: []Message{{Role: RoleUser, Text: "x"}, {Role: RoleAssistant, ToolCalls: calls}},
				Pending:  pending,
			}

			if err := validateStoredSession(sess); err == nil {
				t.Fatal("malformed session accepted")
			}
		})
	}
}

func TestSessionValidationRejectsInvalidPartitions(t *testing.T) {
	valid := func() Session {
		return Session{
			Version: SessionVersion, SessionID: mustSessionID(t, testSessionID), Provider: testProviderState(),
			Messages: []Message{{Role: RoleUser, Text: "x"}, {Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "call-1", Name: "task_add", Input: json.RawMessage(`{"text":"x"}`)},
				{ID: "call-2", Name: "vault_capture", Input: json.RawMessage(`{"text":"y"}`)},
			}}},
			Results: []ToolResult{{CallID: "call-1", Content: "ok"}},
			Pending: []PendingCall{{CallID: "call-2", ApprovalID: "approval-2", ToolName: "vault.capture"}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*Session)
	}{
		{name: "duplicate result", mutate: func(s *Session) { s.Results = append(s.Results, s.Results[0]) }},
		{name: "overlap", mutate: func(s *Session) { s.Pending[0].CallID = "call-1"; s.Pending[0].ToolName = "task.add" }},
		{name: "missing", mutate: func(s *Session) { s.Results = nil }},
		{name: "extraneous result", mutate: func(s *Session) { s.Results[0].CallID = "other" }},
		{name: "extraneous pending", mutate: func(s *Session) { s.Pending[0].CallID = "other" }},
		{name: "duplicate approval", mutate: func(s *Session) {
			s.Results = nil
			s.Pending = []PendingCall{{CallID: "call-1", ApprovalID: "same", ToolName: "task.add"}, {CallID: "call-2", ApprovalID: "same", ToolName: "vault.capture"}}
		}},
		{name: "duplicate tool", mutate: func(s *Session) {
			s.Results = nil
			s.Messages[1].ToolCalls[1].Name = "task_add"
			s.Pending = []PendingCall{{CallID: "call-1", ApprovalID: "a", ToolName: "task.add"}, {CallID: "call-2", ApprovalID: "b", ToolName: "task.add"}}
		}},
		{name: "empty approval", mutate: func(s *Session) { s.Pending[0].ApprovalID = "" }},
		{name: "empty tool", mutate: func(s *Session) { s.Pending[0].ToolName = "" }},
		{name: "trailing json", mutate: func(s *Session) { s.Messages[1].ToolCalls[0].Input = json.RawMessage(`{} {}`) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := valid()
			tt.mutate(&session)
			if err := validateStoredSession(session); err == nil {
				t.Fatal("invalid session accepted")
			}
		})
	}
}

func TestResumeRejectsApprovalBindingMismatch(t *testing.T) {
	tests := []struct {
		name       string
		caller     string
		callID     string
		tool       string
		params     json.RawMessage
		mutateSess func(*Session)
	}{
		{name: "caller", caller: CallerPrefix + strings.Repeat("f", 64), callID: "call-1", tool: "task.add", params: json.RawMessage(`{"text":"x"}`)},
		{name: "call id", caller: CallerPrefix + testSessionID, callID: "other", tool: "task.add", params: json.RawMessage(`{"text":"x"}`)},
		{name: "tool", caller: CallerPrefix + testSessionID, callID: "call-1", tool: "vault.capture", params: json.RawMessage(`{"text":"x"}`)},
		{name: "params", caller: CallerPrefix + testSessionID, callID: "call-1", tool: "task.add", params: json.RawMessage(`{"text":"y"}`)},
		{name: "approval id", caller: CallerPrefix + testSessionID, callID: "call-1", tool: "task.add", params: json.RawMessage(`{"text":"x"}`), mutateSess: func(s *Session) { s.Pending[0].ApprovalID = "missing" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := approval.NewQueue(nil)
			approvalID, err := q.EnqueueWithCallID(tt.caller, tt.callID, tt.tool, tt.params, "mutating")
			if err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			if _, err := q.Approve(approvalID); err != nil {
				t.Fatalf("approve: %v", err)
			}
			if _, err := q.RecordResult(approvalID, json.RawMessage(`{"ok":true}`), nil); err != nil {
				t.Fatalf("record result: %v", err)
			}
			sess := Session{
				Version: SessionVersion, SessionID: mustSessionID(t, testSessionID),
				Provider: testProviderState(),
				Messages: []Message{{Role: RoleUser, Text: "x"}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "task_add", Input: json.RawMessage(`{ "text" : "x" }`)}}}},
				Pending:  []PendingCall{{CallID: "call-1", ApprovalID: approvalID, ToolName: "task.add"}},
			}
			if tt.mutateSess != nil {
				tt.mutateSess(&sess)
			}
			executions := 0
			registry := tools.NewRegistry()
			if err := registry.RegisterLocal(tools.Tool{Name: "task.add", Mutating: true, Schema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
				executions++
				return json.RawMessage(`{"ok":true}`), nil
			}); err != nil {
				t.Fatalf("register: %v", err)
			}
			llm := &stubLLM{responses: []*GenerateResponse{textResp("unexpected")}}
			p := NewForResume(pipeDaemonAndClient(t, registry, q), llm, sess.SessionID)

			if _, err := p.Resume(context.Background(), sess); err == nil {
				t.Fatal("binding mismatch accepted")
			}
			if len(llm.captured) != 0 {
				t.Fatalf("LLM calls = %d, want 0", len(llm.captured))
			}
			if executions != 0 {
				t.Fatalf("registry executions = %d, want 0", executions)
			}
		})
	}
}

func TestResumeRejectsFetchedApprovalIDMismatch(t *testing.T) {
	sessionID := mustSessionID(t, testSessionID)
	planner := NewForResume(nil, &stubLLM{}, sessionID)
	call := ToolCall{ID: "call-1", Name: "task_add", Input: json.RawMessage(`{"text":"x"}`)}
	pending := PendingCall{CallID: "call-1", ApprovalID: "expected", ToolName: "task.add"}
	record := approval.Pending{
		ID: "different", Caller: CallerPrefix + testSessionID, CallID: "call-1",
		ToolName: "task.add", Params: json.RawMessage(`{"text":"x"}`), Status: approval.StatusExecuted,
	}

	if err := planner.validateApprovalBinding(call, pending, record); err == nil {
		t.Fatal("fetched approval id mismatch accepted")
	}
}

func TestResumeRejectsInvalidSessionBeforeQIDOrLLMActivity(t *testing.T) {
	session := Session{
		Version: SessionVersion, SessionID: mustSessionID(t, testSessionID), Provider: testProviderState(),
		Messages: []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "bad id", Name: "task_add", Input: json.RawMessage(`{}`)}}}},
		Pending:  []PendingCall{{CallID: "bad id", ApprovalID: "approval", ToolName: "task.add"}},
	}
	llm := &stubLLM{responses: []*GenerateResponse{textResp("unexpected")}}
	planner := NewForResume(nil, llm, session.SessionID)

	if _, err := planner.Resume(context.Background(), session); err == nil {
		t.Fatal("invalid session accepted")
	}
	if len(llm.captured) != 0 {
		t.Fatalf("LLM calls = %d, want 0", len(llm.captured))
	}
}
