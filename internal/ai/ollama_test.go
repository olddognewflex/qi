package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaGenerateRequestShape(t *testing.T) {
	var got ollamaChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"qwen3:14b",
			"message":{"role":"assistant","content":"hi back"},
			"done":true,
			"done_reason":"stop",
			"prompt_eval_count":42,
			"eval_count":7
		}`))
	}))
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "", nil)
	resp, err := p.Generate(context.Background(), GenerateRequest{
		Model:  "qwen3:14b",
		System: "you are friendly",
		Messages: []Message{
			{Role: RoleUser, Text: "hi"},
		},
		Tools: []ToolDef{
			{Name: "vault_capture", Description: "Capture text", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)},
		},
		MaxTokens:   512,
		CacheSystem: true,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got.Model != "qwen3:14b" {
		t.Errorf("model = %q", got.Model)
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content != "you are friendly" {
		t.Errorf("first message = %+v, want system", got.Messages[0])
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content != "hi" {
		t.Errorf("second message = %+v", got.Messages[1])
	}
	if len(got.Tools) != 1 || got.Tools[0].Function.Name != "vault_capture" {
		t.Errorf("tools = %+v", got.Tools)
	}
	if got.Options["num_predict"].(float64) != 512 {
		t.Errorf("num_predict = %v", got.Options["num_predict"])
	}
	if got.Stream {
		t.Error("stream should be false")
	}
	if resp.Text != "hi back" {
		t.Errorf("text = %q", resp.Text)
	}
	if resp.Usage.InputTokens != 42 || resp.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestOllamaParsesToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"qwen3:14b",
			"message":{
				"role":"assistant",
				"content":"",
				"tool_calls":[
					{"function":{"name":"vault_capture","arguments":{"text":"hi"}}}
				]
			},
			"done":true,
			"done_reason":"tool_use"
		}`))
	}))
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "", nil)
	resp, err := p.Generate(context.Background(), GenerateRequest{
		Model:    "qwen3:14b",
		Messages: []Message{{Role: RoleUser, Text: "capture hi"}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "vault_capture" {
		t.Errorf("name = %q", resp.ToolCalls[0].Name)
	}
	if !strings.Contains(string(resp.ToolCalls[0].Input), `"text":"hi"`) {
		t.Errorf("input = %s", resp.ToolCalls[0].Input)
	}
	if resp.ToolCalls[0].ID == "" {
		t.Error("ollama IDs must be fabricated, got empty")
	}
}

func TestOllamaErrorPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "", nil)
	_, err := p.Generate(context.Background(), GenerateRequest{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestOllamaToolResultRoundTrip(t *testing.T) {
	var got ollamaChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"qwen3:14b","message":{"role":"assistant","content":"done"},"done":true,"done_reason":"stop"}`))
	}))
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "", nil)
	_, err := p.Generate(context.Background(), GenerateRequest{
		Messages: []Message{
			{Role: RoleUser, Text: "capture please"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "oll_0", Name: "vault_capture", Input: json.RawMessage(`{"text":"hi"}`)}}},
			{Role: RoleUser, ToolResults: []ToolResult{{CallID: "oll_0", Content: `{"path":"/tmp/a.md"}`}}},
		},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// Expect: user → assistant(with tool_calls) → tool(result) on the wire.
	if len(got.Messages) != 3 {
		t.Fatalf("wire messages = %d, want 3", len(got.Messages))
	}
	if got.Messages[0].Role != "user" {
		t.Errorf("[0] role = %q", got.Messages[0].Role)
	}
	if got.Messages[1].Role != "assistant" || len(got.Messages[1].ToolCalls) != 1 {
		t.Errorf("[1] = %+v", got.Messages[1])
	}
	if got.Messages[1].ToolCalls[0].Function.Name != "vault_capture" {
		t.Errorf("tool name on wire = %q", got.Messages[1].ToolCalls[0].Function.Name)
	}
	if got.Messages[2].Role != "tool" {
		t.Errorf("[2] role = %q (expected tool)", got.Messages[2].Role)
	}
	if got.Messages[2].Content != `{"path":"/tmp/a.md"}` {
		t.Errorf("tool content = %q", got.Messages[2].Content)
	}
}

func TestOllamaAuthHeader(t *testing.T) {
	cases := []struct {
		name   string
		apiKey string
		want   string
	}{
		{"cloud sends bearer", "secret-key", "Bearer secret-key"},
		{"local sends none", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`))
			}))
			defer srv.Close()

			p := NewOllamaProvider(srv.URL, tc.apiKey, nil)
			if _, err := p.Generate(context.Background(), GenerateRequest{
				Messages: []Message{{Role: RoleUser, Text: "hi"}},
			}); err != nil {
				t.Fatalf("generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}
