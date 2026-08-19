// Package ai drives an LLM tool-use loop against qid's tool catalog. The
// planner sends a user prompt plus the live tools.list to a configured
// Provider (Anthropic, Ollama, ...), executes any tool calls the model
// emits via the qid JSON-RPC client (caller="ai-planner:<sessionID>"),
// and feeds tool results back until the model produces a final text
// response.
//
// Mutating tools surface as approval-pending tool results so the model
// sees them and can stop or instruct the user — qi-mcp's pattern. The
// planner never bypasses qid's policy gate, regardless of provider.
package ai

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"qi/internal/approval"
	"qi/internal/daemon/client"
	"qi/internal/tools"
)

// DefaultModel is the model used when no override is supplied by the
// caller. Provider-specific defaults (DefaultAnthropicModel,
// DefaultOllamaModel) kick in inside each provider when this value is
// passed through but does not match what the provider expects.
const DefaultModel = DefaultAnthropicModel

// DefaultMaxIterations bounds the tool-use loop. Each iteration is one
// Generate call; a model that keeps proposing tool calls hits this cap
// and is forced to return text.
const DefaultMaxIterations = 8

// DefaultMaxTokens caps the model's response budget per turn.
const DefaultMaxTokens = 2048

// CallerPrefix is used to identify planner-issued tool calls to qid's
// policy layer. The full caller is CallerPrefix + sessionID.
const CallerPrefix = "ai-planner:"

// Planner runs one tool-use conversation against the LLM, dispatching
// proposed tool calls through the qid client.
type Planner struct {
	llm           LLM
	qid           *client.Client
	model         string
	maxTokens     int
	maxIterations int
	sessionID     SessionID
}

// New constructs a Planner using the Anthropic provider. apiKey may be
// empty; the SDK then falls back to ANTHROPIC_API_KEY.
func New(qid *client.Client, apiKey string) (*Planner, error) {
	return NewWithLLM(qid, NewAnthropicProvider(apiKey))
}

// NewWithLLM binds a Planner to any LLM implementation. Used by tests and
// by the commands package when assembling providers from config.
func NewWithLLM(qid *client.Client, llm LLM) (*Planner, error) {
	return newPlannerWithReader(qid, llm, rand.Reader)
}

func newPlannerWithReader(qid *client.Client, llm LLM, reader io.Reader) (*Planner, error) {
	id, err := generateSessionID(reader)
	if err != nil {
		return nil, err
	}
	return NewForResume(qid, llm, id), nil
}

func NewForResume(qid *client.Client, llm LLM, id SessionID) *Planner {
	return &Planner{
		llm:           llm,
		qid:           qid,
		model:         "",
		maxTokens:     DefaultMaxTokens,
		maxIterations: DefaultMaxIterations,
		sessionID:     id,
	}
}

// Caller returns the full caller string sent to qid for every tool call.
func (p *Planner) Caller() string { return CallerPrefix + p.sessionID.String() }

// SetModel overrides the model used for subsequent Run calls. An empty
// string lets the provider use its own default.
func (p *Planner) SetModel(m string) { p.model = m }

// TurnEvent records one assistant turn for logging and tests.
type TurnEvent struct {
	Iteration int
	Text      string
	ToolCalls []ToolCallRecord
}

// ToolCallRecord captures one tool invocation produced by the model.
type ToolCallRecord struct {
	Name     string
	Input    json.RawMessage
	Pending  bool
	Error    string
	ResultID string
}

// RunResult is the outcome of a Planner.Run.
type RunResult struct {
	FinalText string
	Turns     []TurnEvent
	Pending   []PendingCall
	// SessionID is set (with StopReason "awaiting_approval") when the loop
	// stopped at the approval gate and persisted a session to resume from.
	SessionID  string
	StopReason string
	CacheUsage CacheStats
}

// StopAwaitingApproval is the StopReason set when the loop persisted a session
// and stopped at the human approval gate.
const StopAwaitingApproval = "awaiting_approval"

// CacheStats aggregates per-turn token usage across a Run.
type CacheStats struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
}

// Run executes the tool-use loop. Terminates when the model returns a
// turn with no tool calls, when a mutating call hits the approval gate (the
// session is persisted and StopReason is StopAwaitingApproval), or when
// MaxIterations is reached.
func (p *Planner) Run(ctx context.Context, userPrompt string) (RunResult, error) {
	if strings.TrimSpace(userPrompt) == "" {
		return RunResult{}, errors.New("ai.Run: prompt is empty")
	}
	toolDefs, nameMap, err := p.tools(ctx)
	if err != nil {
		return RunResult{}, err
	}
	messages := []Message{{Role: RoleUser, Text: userPrompt}}
	return p.loop(ctx, messages, toolDefs, nameMap)
}

// Resume continues a persisted session across the human approval gate: it
// rebuilds the tool-results turn from the session's already-known results plus
// the now-executed approval outputs, then runs the loop as if the mutation had
// returned inline. This is what lets a multi-step flow ("add a task, then act on
// its id") complete — the planner finally sees the mutation's result (#63).
func (p *Planner) Resume(ctx context.Context, sess Session) (RunResult, error) {
	if len(sess.Messages) == 0 {
		return RunResult{}, errors.New("ai.Resume: empty session")
	}
	p.sessionID = sess.SessionID
	if sess.Model != "" {
		p.model = sess.Model
	}
	toolDefs, nameMap, err := p.tools(ctx)
	if err != nil {
		return RunResult{}, err
	}

	// The last message is the assistant turn whose tool calls we are resolving.
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != RoleAssistant || len(last.ToolCalls) == 0 {
		return RunResult{}, errors.New("ai.Resume: session does not end at a tool-call turn")
	}
	pendingByCall := make(map[string]PendingCall, len(sess.Pending))
	for _, pc := range sess.Pending {
		pendingByCall[pc.CallID] = pc
	}

	toolResults := make([]ToolResult, 0, len(last.ToolCalls))
	for _, call := range last.ToolCalls {
		if r, ok := sess.Results[call.ID]; ok {
			toolResults = append(toolResults, r)
			continue
		}
		pc, ok := pendingByCall[call.ID]
		if !ok {
			// Should not happen: a call with neither a stored result nor a
			// pending record. Feed a neutral error so the model isn't left with a
			// dangling tool_use.
			toolResults = append(toolResults, ToolResult{CallID: call.ID, Content: "no result recorded for this call", IsError: true})
			continue
		}
		tr, err := p.resolveApproval(ctx, call.ID, pc)
		if err != nil {
			return RunResult{}, err
		}
		toolResults = append(toolResults, tr)
	}

	messages := append(sess.Messages, Message{Role: RoleUser, ToolResults: toolResults})
	return p.loop(ctx, messages, toolDefs, nameMap)
}

// resolveApproval turns a pending call into the tool result to feed the model:
// the executed output when approved, an error when denied/failed, or an error
// return when the human has not resolved it yet.
func (p *Planner) resolveApproval(ctx context.Context, callID string, pc PendingCall) (ToolResult, error) {
	ap, err := p.qid.GetApproval(ctx, pc.ApprovalID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("ai.Resume: fetch approval %s: %w", pc.ApprovalID, err)
	}
	switch ap.Status {
	case approval.StatusExecuted:
		return ToolResult{CallID: callID, Content: string(ap.Result)}, nil
	case approval.StatusDenied:
		return ToolResult{CallID: callID, Content: "the user denied this action; do not retry it", IsError: true}, nil
	case approval.StatusFailed:
		return ToolResult{CallID: callID, Content: "the approved action failed to execute", IsError: true}, nil
	default:
		return ToolResult{}, fmt.Errorf("approval %s is not resolved yet (status %q); run 'qi ai approve %s' first", pc.ApprovalID, ap.Status, pc.ApprovalID)
	}
}

// tools fetches qid's live catalog and builds the provider tool defs + name map.
func (p *Planner) tools(ctx context.Context) ([]ToolDef, map[string]string, error) {
	catalog, err := p.qid.ListTools(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("ai: list tools: %w", err)
	}
	return buildToolDefs(catalog)
}

// loop drives the tool-use conversation from the given message history.
func (p *Planner) loop(ctx context.Context, messages []Message, toolDefs []ToolDef, nameMap map[string]string) (RunResult, error) {
	result := RunResult{}
	systemPrompt := systemPrompt(p.Caller())

	for i := 0; i < p.maxIterations; i++ {
		resp, err := p.llm.Generate(ctx, GenerateRequest{
			Model:       p.model,
			System:      systemPrompt,
			Messages:    messages,
			Tools:       toolDefs,
			MaxTokens:   p.maxTokens,
			CacheSystem: true,
		})
		if err != nil {
			return result, fmt.Errorf("ai: generate: %w", err)
		}
		result.CacheUsage.InputTokens += resp.Usage.InputTokens
		result.CacheUsage.OutputTokens += resp.Usage.OutputTokens
		result.CacheUsage.CacheCreationTokens += resp.Usage.CacheCreationTokens
		result.CacheUsage.CacheReadTokens += resp.Usage.CacheReadTokens

		turn := TurnEvent{Iteration: i, Text: strings.TrimSpace(resp.Text)}

		if len(resp.ToolCalls) == 0 {
			result.Turns = append(result.Turns, turn)
			result.FinalText = turn.Text
			result.StopReason = resp.StopReason
			return result, nil
		}

		toolResults, calls, pendings := p.runToolCalls(ctx, resp.ToolCalls, nameMap)
		turn.ToolCalls = calls
		result.Turns = append(result.Turns, turn)

		messages = append(messages, Message{Role: RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls})

		// A mutation hit the approval gate: persist the conversation up to and
		// including this assistant turn, plus the non-pending results, and stop.
		// `qi ai resume` continues once the human approves.
		if len(pendings) > 0 {
			result.Pending = append(result.Pending, pendings...)
			pendingSet := make(map[string]bool, len(pendings))
			for _, pc := range pendings {
				pendingSet[pc.CallID] = true
			}
			resolved := make(map[string]ToolResult)
			for _, r := range toolResults {
				if !pendingSet[r.CallID] {
					resolved[r.CallID] = r
				}
			}
			sess := Session{
				Version:   SessionVersion,
				SessionID: p.sessionID,
				Model:     p.model,
				Messages:  messages,
				Results:   resolved,
				Pending:   pendings,
			}
			if err := sess.Save(); err != nil {
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

func (p *Planner) runToolCalls(ctx context.Context, calls []ToolCall, nameMap map[string]string) ([]ToolResult, []ToolCallRecord, []PendingCall) {
	var results []ToolResult
	var records []ToolCallRecord
	var pendings []PendingCall
	caller := p.Caller()

	for _, c := range calls {
		qidName, ok := nameMap[c.Name]
		if !ok {
			qidName = c.Name
		}
		rec := ToolCallRecord{Name: qidName, Input: c.Input, ResultID: c.ID}

		raw, err := p.qid.CallToolAs(ctx, caller, qidName, c.Input)
		switch {
		case err != nil:
			rec.Error = err.Error()
			results = append(results, ToolResult{CallID: c.ID, Content: "error: " + err.Error(), IsError: true})
		default:
			if pending, isPending := client.IsPending(raw); isPending {
				rec.Pending = true
				pendings = append(pendings, PendingCall{
					CallID:     c.ID,
					ApprovalID: pending.ApprovalID,
					ToolName:   qidName,
					Reason:     pending.Reason,
				})
				// Placeholder content; on resume this call's result is replaced
				// with the executed approval output (or a denial error).
				results = append(results, ToolResult{CallID: c.ID, Content: "awaiting approval " + pending.ApprovalID, IsError: true})
			} else {
				results = append(results, ToolResult{CallID: c.ID, Content: string(raw)})
			}
		}
		records = append(records, rec)
	}
	return results, records, pendings
}

// buildToolDefs converts qid tools into provider-neutral ToolDefs and
// returns the inverse name map so the planner can translate the model's
// sanitized tool_use names back to original qid names before dispatch.
// Anthropic and OpenAI/Ollama both restrict tool names to a subset of
// characters that excludes '.'; sanitization is therefore applied
// uniformly.
func buildToolDefs(catalog []tools.Tool) ([]ToolDef, map[string]string, error) {
	out := make([]ToolDef, 0, len(catalog))
	nameMap := make(map[string]string, len(catalog))
	for _, t := range catalog {
		apiName := sanitizeToolName(t.Name)
		if prev, exists := nameMap[apiName]; exists {
			return nil, nil, fmt.Errorf("ai: sanitized name %q collides between %q and %q", apiName, prev, t.Name)
		}
		nameMap[apiName] = t.Name
		out = append(out, ToolDef{
			Name:        apiName,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}
	return out, nameMap, nil
}

func sanitizeToolName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

func systemPrompt(caller string) string {
	return "You are the Qi assistant's planner. You are calling tools as caller=\"" + caller + "\". " +
		"Mutating tools route through a human approval queue and return tool errors of the form " +
		"\"approval required (id=...)\". When you see one, tell the user verbatim which command to run, " +
		"then stop calling tools."
}

// ProviderFromEnv constructs the default provider for the current process.
// Honors QI_AI_PROVIDER ("anthropic"|"ollama") and provider-specific
// environment variables. Used by commands that don't have config plumbed
// through to them yet.
func ProviderFromEnv() LLM {
	switch Provider(os.Getenv("QI_AI_PROVIDER")) {
	case ProviderOllama:
		return NewOllamaProvider(os.Getenv("OLLAMA_URL"), os.Getenv("OLLAMA_API_KEY"), nil)
	default:
		return NewAnthropicProvider(os.Getenv("ANTHROPIC_API_KEY"))
	}
}
