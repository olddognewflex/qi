package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"qi/internal/approval"
	"qi/internal/daemon"
	"qi/internal/daemon/client"
	"qi/internal/policy"
	"qi/internal/tools"
	"qi/internal/tools/builtin"
)

// stubLLM scripts a sequence of GenerateResponse values the planner will
// receive on successive Generate calls. The last entry is returned for
// every additional call.
type stubLLM struct {
	mu        sync.Mutex
	responses []*GenerateResponse
	captured  []GenerateRequest
	state     ProviderState
	stateErr  error
	idx       int
}

func (s *stubLLM) ProviderState() (ProviderState, error) {
	if s.stateErr != nil {
		return ProviderState{}, s.stateErr
	}
	if s.state.Version != 0 {
		return s.state, nil
	}
	return testProviderState(), nil
}

type nonResumableLLM struct{ base *stubLLM }

func (n *nonResumableLLM) Generate(ctx context.Context, request GenerateRequest) (*GenerateResponse, error) {
	return n.base.Generate(ctx, request)
}

func testProviderState() ProviderState {
	return ProviderState{
		Version: ProviderStateVersion,
		Entries: []ProviderStateEntry{{Provider: ProviderAnthropic, Model: "test-model", ConfigID: strings.Repeat("b", 64)}},
	}
}

func (s *stubLLM) Generate(_ context.Context, req GenerateRequest) (*GenerateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.captured = append(s.captured, req)
	i := s.idx
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	s.idx++
	return s.responses[i], nil
}

func textResp(text string) *GenerateResponse {
	return &GenerateResponse{Text: text, StopReason: "end_turn"}
}

func textRespWithUsage(text string, input, output, cw, cr int64) *GenerateResponse {
	r := textResp(text)
	r.Usage = Usage{InputTokens: input, OutputTokens: output, CacheCreationTokens: cw, CacheReadTokens: cr}
	return r
}

func toolUseResp(toolName, callID, inputJSON string) *GenerateResponse {
	return &GenerateResponse{
		StopReason: "tool_use",
		ToolCalls: []ToolCall{
			{ID: callID, Name: toolName, Input: json.RawMessage(inputJSON)},
		},
	}
}

func pipeDaemonAndClient(t *testing.T, r *tools.Registry, q *approval.Queue) *client.Client {
	t.Helper()
	server := daemon.NewServer(r, policy.DefaultDecider{}, q, nil)
	cliConn, srvConn := net.Pipe()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		server.ServeConn(context.Background(), srvConn)
	}()
	c := client.NewFromConn(cliConn)
	t.Cleanup(func() {
		_ = cliConn.Close()
		wg.Wait()
	})
	return c
}

func newTestPlanner(t *testing.T, c *client.Client, llm LLM) *Planner {
	t.Helper()
	p, err := NewWithLLM(c, llm)
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	return p
}

func TestSanitizeToolName(t *testing.T) {
	cases := map[string]string{
		"vault.capture":           "vault_capture",
		"skill.daily-review":      "skill_daily-review",
		"mcp.github.create_issue": "mcp_github_create_issue",
		"already_clean":           "already_clean",
	}
	for in, want := range cases {
		if got := sanitizeToolName(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildToolDefsCollision(t *testing.T) {
	catalog := []tools.Tool{
		{Name: "ab.c", Source: tools.Source{Kind: tools.SourceLocal}},
		{Name: "ab_c", Source: tools.Source{Kind: tools.SourceLocal}},
	}
	if _, _, err := buildToolDefs(catalog); err == nil {
		t.Fatal("expected collision error when sanitized names match")
	}
}

func TestRunRejectsEmptyPrompt(t *testing.T) {
	c := pipeDaemonAndClient(t, tools.NewRegistry(), nil)
	p := newTestPlanner(t, c, &stubLLM{responses: []*GenerateResponse{textResp("ok")}})
	if _, err := p.Run(context.Background(), "   "); err == nil {
		t.Fatal("expected error for empty prompt")
	}
}

func TestRunReturnsTextWhenNoToolUse(t *testing.T) {
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	c := pipeDaemonAndClient(t, r, nil)
	llm := &stubLLM{responses: []*GenerateResponse{textResp("hello back")}}
	p := newTestPlanner(t, c, llm)

	res, err := p.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinalText != "hello back" {
		t.Fatalf("final text = %q", res.FinalText)
	}
	if len(res.Turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(res.Turns))
	}
	if len(llm.captured[0].Tools) == 0 {
		t.Fatal("expected tool catalog to be sent")
	}
	if !llm.captured[0].CacheSystem {
		t.Fatal("expected CacheSystem=true to be propagated")
	}
}

func TestRunDispatchesToolAndLoops(t *testing.T) {
	r := tools.NewRegistry()
	const readOnlyTool = "ro.echo"
	echo := func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(params, &p)
		out, _ := json.Marshal(map[string]string{"echoed": p.Text})
		return out, nil
	}
	if err := r.RegisterLocal(tools.Tool{
		Name:        readOnlyTool,
		Description: "Echoes input",
		Mutating:    false,
		Schema:      json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}, echo); err != nil {
		t.Fatalf("register: %v", err)
	}
	c := pipeDaemonAndClient(t, r, nil)

	llm := &stubLLM{responses: []*GenerateResponse{
		toolUseResp(sanitizeToolName(readOnlyTool), "tu_1", `{"text":"ping"}`),
		textResp("done"),
	}}
	p := newTestPlanner(t, c, llm)

	res, err := p.Run(context.Background(), "say hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinalText != "done" {
		t.Fatalf("final = %q", res.FinalText)
	}
	if len(res.Turns) != 2 {
		t.Fatalf("turns = %d", len(res.Turns))
	}
	if len(res.Turns[0].ToolCalls) != 1 || res.Turns[0].ToolCalls[0].Name != readOnlyTool {
		t.Fatalf("first turn calls = %+v", res.Turns[0].ToolCalls)
	}
	if res.Turns[0].ToolCalls[0].Pending {
		t.Fatal("read-only tool should not be marked pending")
	}
	if res.Turns[0].ToolCalls[0].Error != "" {
		t.Fatalf("unexpected error: %s", res.Turns[0].ToolCalls[0].Error)
	}

	// Verify the second Generate received the tool result in messages.
	if len(llm.captured) != 2 {
		t.Fatalf("captured = %d, want 2", len(llm.captured))
	}
	msgs := llm.captured[1].Messages
	if len(msgs) < 3 {
		t.Fatalf("turn-2 messages = %d, want at least 3", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != RoleUser || len(last.ToolResults) != 1 {
		t.Fatalf("expected trailing user/tool-result message, got %+v", last)
	}
	if last.ToolResults[0].CallID != "tu_1" {
		t.Fatalf("call id = %q", last.ToolResults[0].CallID)
	}
}

func TestRunSurfacesPendingApproval(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // the planner now persists a session on pending
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	q := approval.NewQueue(nil)
	c := pipeDaemonAndClient(t, r, q)

	llm := &stubLLM{responses: []*GenerateResponse{
		toolUseResp(sanitizeToolName(builtin.CaptureToolName), "tu_1", `{"text":"AI proposed"}`),
		textResp("told the user to approve"),
	}}
	p := newTestPlanner(t, c, llm)

	res, err := p.Run(context.Background(), "capture this for me")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(res.Pending))
	}
	pendingList := q.List(approval.StatusPending)
	if len(pendingList) != 1 {
		t.Fatalf("queue pending = %d", len(pendingList))
	}
	if !strings.HasPrefix(pendingList[0].Caller, CallerPrefix) {
		t.Fatalf("caller = %q, want prefix %q", pendingList[0].Caller, CallerPrefix)
	}
}

func TestRunDeniesPendingApprovalWhenSessionSaveFails(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative")
	registry := tools.NewRegistry()
	if err := builtin.RegisterCapture(registry, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	queue := approval.NewQueue(nil)
	planner := newTestPlanner(t, pipeDaemonAndClient(t, registry, queue), &stubLLM{responses: []*GenerateResponse{
		toolUseResp(sanitizeToolName(builtin.CaptureToolName), "call-1", `{"text":"orphan"}`),
	}})

	_, err := planner.Run(context.Background(), "capture this")

	if err == nil || !strings.Contains(err.Error(), "persist session") {
		t.Fatalf("error = %v, want persistence failure", err)
	}
	entries := queue.List("")
	if len(entries) != 1 || entries[0].Status != approval.StatusDenied {
		t.Fatalf("approvals = %+v, want one denied approval", entries)
	}
}

func TestRunDeniesPendingApprovalWhenProviderStateFails(t *testing.T) {
	tests := []struct {
		name string
		llm  LLM
	}{
		{name: "unsupported", llm: &nonResumableLLM{base: &stubLLM{responses: []*GenerateResponse{
			toolUseResp(sanitizeToolName(builtin.CaptureToolName), "call-1", `{"text":"orphan"}`),
		}}}},
		{name: "snapshot error", llm: &stubLLM{stateErr: errors.New("state failed"), responses: []*GenerateResponse{
			toolUseResp(sanitizeToolName(builtin.CaptureToolName), "call-1", `{"text":"orphan"}`),
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			registry := tools.NewRegistry()
			if err := builtin.RegisterCapture(registry, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			queue := approval.NewQueue(nil)
			planner := newTestPlanner(t, pipeDaemonAndClient(t, registry, queue), tt.llm)

			_, err := planner.Run(context.Background(), "capture this")

			if err == nil {
				t.Fatal("provider state failure was accepted")
			}
			entries := queue.List("")
			if len(entries) != 1 || entries[0].Status != approval.StatusDenied {
				t.Fatalf("approvals = %+v, want one denied approval", entries)
			}
		})
	}
}

func TestRunRejectsInvalidToolCallsBeforeQIDActivity(t *testing.T) {
	registry := tools.NewRegistry()
	executions := 0
	if err := registry.RegisterLocal(tools.Tool{Name: "mutate.one", Mutating: true, Schema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions++
		return json.RawMessage(`{"ok":true}`), nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	queue := approval.NewQueue(nil)
	llm := &stubLLM{responses: []*GenerateResponse{{StopReason: "tool_use", ToolCalls: []ToolCall{
		{ID: "call-1", Name: "mutate_one", Input: json.RawMessage(`{} {}`)},
	}}}}
	planner := newTestPlanner(t, pipeDaemonAndClient(t, registry, queue), llm)

	if _, err := planner.Run(context.Background(), "mutate"); err == nil {
		t.Fatal("trailing tool input JSON accepted")
	}
	if executions != 0 || len(queue.List("")) != 0 {
		t.Fatalf("qid activity: executions=%d approvals=%d", executions, len(queue.List("")))
	}
}

func TestRunStopsAtMaxIterations(t *testing.T) {
	r := tools.NewRegistry()
	const readOnlyTool = "ro.noop"
	if err := r.RegisterLocal(tools.Tool{
		Name:     readOnlyTool,
		Mutating: false,
		Schema:   json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, p json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	c := pipeDaemonAndClient(t, r, nil)

	llm := &stubLLM{responses: []*GenerateResponse{
		toolUseResp(sanitizeToolName(readOnlyTool), "tu_1", `{}`),
	}}
	p := newTestPlanner(t, c, llm)
	p.maxIterations = 3

	res, err := p.Run(context.Background(), "loop forever")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.StopReason != "max_iterations" {
		t.Fatalf("stop = %q, want max_iterations", res.StopReason)
	}
	if len(res.Turns) != 3 {
		t.Fatalf("turns = %d", len(res.Turns))
	}
}

func TestCacheStatsAggregateAcrossTurns(t *testing.T) {
	r := tools.NewRegistry()
	const readOnlyTool = "ro.noop"
	if err := r.RegisterLocal(tools.Tool{
		Name:     readOnlyTool,
		Mutating: false,
		Schema:   json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, p json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	c := pipeDaemonAndClient(t, r, nil)

	first := toolUseResp(sanitizeToolName(readOnlyTool), "tu_1", `{}`)
	first.Usage = Usage{InputTokens: 100, OutputTokens: 20, CacheCreationTokens: 80}
	second := textRespWithUsage("done", 30, 10, 0, 80)
	llm := &stubLLM{responses: []*GenerateResponse{first, second}}
	p := newTestPlanner(t, c, llm)
	res, err := p.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := CacheStats{InputTokens: 130, OutputTokens: 30, CacheCreationTokens: 80, CacheReadTokens: 80}
	if res.CacheUsage != want {
		t.Fatalf("cache usage = %+v, want %+v", res.CacheUsage, want)
	}
}

func TestCallerHasPrefix(t *testing.T) {
	c := pipeDaemonAndClient(t, tools.NewRegistry(), nil)
	p := newTestPlanner(t, c, &stubLLM{responses: []*GenerateResponse{textResp("ok")}})
	if !strings.HasPrefix(p.Caller(), CallerPrefix) {
		t.Fatalf("caller = %q", p.Caller())
	}
}
