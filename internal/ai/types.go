// This file defines the provider-neutral types that the planner depends on.
// Each provider (Anthropic, Ollama, ...) implements the LLM interface and
// translates between these neutral types and its own wire shape.

package ai

import (
	"context"
	"encoding/json"
)

// Role identifies the speaker of a Message in a planner conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one conversation turn in provider-neutral form. A turn is
// either a user message (plain text or carrying tool results) or an
// assistant message (plain text and/or tool calls).
type Message struct {
	Role        Role
	Text        string
	ToolCalls   []ToolCall   // assistant turns that proposed tool calls
	ToolResults []ToolResult // user turns that are returning tool results
}

// ToolCall is one tool_use proposal emitted by the model.
type ToolCall struct {
	ID    string          // unique within the conversation
	Name  string          // sanitized name as sent to the provider
	Input json.RawMessage // JSON-encoded arguments
}

// ToolResult carries one tool's output back to the model. CallID matches
// the originating ToolCall.ID; providers that do not support correlation
// ids (Ollama) ignore the field and rely on ordering.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// ToolDef describes a tool the provider may invoke.
type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// GenerateRequest is the planner's per-turn input to a Provider.
type GenerateRequest struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
	// CacheSystem hints that the provider should cache the tools+system
	// prefix when supported. Providers without prompt caching ignore it.
	CacheSystem bool
}

// GenerateResponse is the provider's reply for one turn.
type GenerateResponse struct {
	Text       string
	ToolCalls  []ToolCall
	StopReason string
	Usage      Usage
}

// Usage tallies tokens for one turn. CacheCreationTokens and
// CacheReadTokens are zero for providers without prompt caching.
type Usage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
}

// LLM is the provider-neutral abstraction the planner depends on.
type LLM interface {
	Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error)
}

// Provider identifies a configured backend. Anthropic and Ollama have
// native wire implementations; the rest share OpenAIProvider's
// chat-completions wire with per-provider endpoints (see providers.go).
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOllama    Provider = "ollama"
	ProviderOpenAI    Provider = "openai"
	ProviderKimi      Provider = "kimi"
	ProviderOpenCode  Provider = "opencode"
	ProviderZAI       Provider = "zai"
)
