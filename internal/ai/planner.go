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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

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
	sessionID     string
}

// New constructs a Planner using the Anthropic provider. apiKey may be
// empty; the SDK then falls back to ANTHROPIC_API_KEY.
func New(qid *client.Client, apiKey string) *Planner {
	return NewWithLLM(qid, NewAnthropicProvider(apiKey))
}

// NewWithLLM binds a Planner to any LLM implementation. Used by tests and
// by the commands package when assembling providers from config.
func NewWithLLM(qid *client.Client, llm LLM) *Planner {
	return &Planner{
		llm:           llm,
		qid:           qid,
		model:         "",
		maxTokens:     DefaultMaxTokens,
		maxIterations: DefaultMaxIterations,
		sessionID:     newSessionID(),
	}
}

// Caller returns the full caller string sent to qid for every tool call.
func (p *Planner) Caller() string { return CallerPrefix + p.sessionID }

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
	FinalText  string
	Turns      []TurnEvent
	Pending    []client.PendingResult
	StopReason string
	CacheUsage CacheStats
}

// CacheStats aggregates per-turn token usage across a Run.
type CacheStats struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
}

// Run executes the tool-use loop. Terminates when the model returns a
// turn with no tool calls, or when MaxIterations is reached.
func (p *Planner) Run(ctx context.Context, userPrompt string) (RunResult, error) {
	if strings.TrimSpace(userPrompt) == "" {
		return RunResult{}, errors.New("ai.Run: prompt is empty")
	}
	catalog, err := p.qid.ListTools(ctx)
	if err != nil {
		return RunResult{}, fmt.Errorf("ai.Run: list tools: %w", err)
	}
	toolDefs, nameMap, err := buildToolDefs(catalog)
	if err != nil {
		return RunResult{}, err
	}

	messages := []Message{{Role: RoleUser, Text: userPrompt}}
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
			return result, fmt.Errorf("ai.Run: generate: %w", err)
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
		result.Pending = append(result.Pending, pendings...)

		messages = append(messages,
			Message{Role: RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls},
			Message{Role: RoleUser, ToolResults: toolResults},
		)
	}

	result.StopReason = "max_iterations"
	return result, nil
}

func (p *Planner) runToolCalls(ctx context.Context, calls []ToolCall, nameMap map[string]string) ([]ToolResult, []ToolCallRecord, []client.PendingResult) {
	var results []ToolResult
	var records []ToolCallRecord
	var pendings []client.PendingResult
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
				pendings = append(pendings, pending)
				msg := fmt.Sprintf(
					"approval required (id=%s). The user must run: qi ai approve %s",
					pending.ApprovalID, pending.ApprovalID,
				)
				if pending.Reason != "" {
					msg += "\nreason: " + pending.Reason
				}
				results = append(results, ToolResult{CallID: c.ID, Content: msg, IsError: true})
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

func newSessionID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
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
