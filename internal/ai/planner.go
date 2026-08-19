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
	"io"
	"os"
	"strings"

	"qi/internal/daemon/client"
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
