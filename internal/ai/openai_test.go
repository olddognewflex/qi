package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIGenerateRequestShape(t *testing.T) {
	var got oaiChatRequest
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"hi back"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":42,"completion_tokens":7}
		}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("kimi", srv.URL, "sekrit", nil)
	resp, err := p.Generate(context.Background(), GenerateRequest{
		Model:  "kimi-k2.5",
		System: "you are friendly",
		Messages: []Message{
			{Role: RoleUser, Text: "hi"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "vault_capture", Input: json.RawMessage(`{"text":"x"}`)}}},
			{Role: RoleUser, ToolResults: []ToolResult{{CallID: "call_1", Content: "ok"}}},
		},
		Tools: []ToolDef{
			{Name: "vault_capture", Description: "Capture text", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)},
		},
		MaxTokens: 512,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if auth != "Bearer sekrit" {
		t.Errorf("auth = %q", auth)
	}
	if got.Model != "kimi-k2.5" {
		t.Errorf("model = %q", got.Model)
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content != "you are friendly" {
		t.Errorf("first message = %+v, want system", got.Messages[0])
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content != "hi" {
		t.Errorf("second message = %+v", got.Messages[1])
	}
	asst := got.Messages[2]
	if asst.Role != "assistant" || len(asst.ToolCalls) != 1 {
		t.Fatalf("assistant message = %+v", asst)
	}
	if asst.ToolCalls[0].ID != "call_1" || asst.ToolCalls[0].Type != "function" ||
		asst.ToolCalls[0].Function.Name != "vault_capture" || asst.ToolCalls[0].Function.Arguments != `{"text":"x"}` {
		t.Errorf("tool call = %+v", asst.ToolCalls[0])
	}
	if got.Messages[3].Role != "tool" || got.Messages[3].ToolCallID != "call_1" || got.Messages[3].Content != "ok" {
		t.Errorf("tool result message = %+v", got.Messages[3])
	}
	if len(got.Tools) != 1 || got.Tools[0].Function.Name != "vault_capture" || got.Tools[0].Type != "function" {
		t.Errorf("tools = %+v", got.Tools)
	}
	// kimi is not OpenAI proper: max_tokens, not max_completion_tokens.
	if got.MaxTokens != 512 || got.MaxCompletionTokens != 0 {
		t.Errorf("max tokens = %d / %d", got.MaxTokens, got.MaxCompletionTokens)
	}
	if resp.Text != "hi back" || resp.StopReason != "stop" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 42 || resp.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestOpenAIUsesMaxCompletionTokensForOpenAIProper(t *testing.T) {
	var got oaiChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("openai", srv.URL, "k", nil)
	if _, err := p.Generate(context.Background(), GenerateRequest{Model: "gpt-x", MaxTokens: 256, Messages: []Message{{Role: RoleUser, Text: "hi"}}}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got.MaxCompletionTokens != 256 || got.MaxTokens != 0 {
		t.Errorf("max tokens = %d / %d, want max_completion_tokens only", got.MaxTokens, got.MaxCompletionTokens)
	}
}

func TestOpenAIParsesToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_abc","type":"function","function":{"name":"task_add","arguments":"{\"text\":\"buy milk\"}"}},
				{"type":"function","function":{"name":"task_list","arguments":""}}
			]},"finish_reason":"tool_calls"}]
		}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("zai", srv.URL, "k", nil)
	resp, err := p.Generate(context.Background(), GenerateRequest{Model: "glm-4.7", Messages: []Message{{Role: RoleUser, Text: "hi"}}})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].ID != "call_abc" || resp.ToolCalls[0].Name != "task_add" || string(resp.ToolCalls[0].Input) != `{"text":"buy milk"}` {
		t.Errorf("call 0 = %+v", resp.ToolCalls[0])
	}
	// Missing id is fabricated; empty arguments become {}.
	if resp.ToolCalls[1].ID != "oai_1" || string(resp.ToolCalls[1].Input) != "{}" {
		t.Errorf("call 1 = %+v", resp.ToolCalls[1])
	}
	if resp.StopReason != "tool_calls" {
		t.Errorf("stop reason = %q", resp.StopReason)
	}
}

func TestOpenAIRequiresModel(t *testing.T) {
	p := NewOpenAIProvider("opencode", "http://unused", "k", nil)
	if _, err := p.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Text: "hi"}}}); err == nil {
		t.Fatal("want error for empty model")
	}
}

func TestOpenAINon200IsProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"usage limit reached"}}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("openai", srv.URL, "k", nil)
	_, err := p.Generate(context.Background(), GenerateRequest{Model: "gpt-x", Messages: []Message{{Role: RoleUser, Text: "hi"}}})
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("want ProviderError, got %v", err)
	}
	if pe.Provider != "openai" || pe.StatusCode != 429 {
		t.Errorf("provider error = %+v", pe)
	}
	if !IsExhausted(err) {
		t.Error("429 should be exhausted")
	}
	if !ShouldFailover(err) {
		t.Error("429 should be failover-worthy")
	}
}
