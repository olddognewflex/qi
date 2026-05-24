package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// DefaultOllamaURL is the default base URL for a locally-running Ollama
// daemon. Override via NewOllamaProvider for non-standard installs.
const DefaultOllamaURL = "http://localhost:11434"

// DefaultOllamaModel is used when GenerateRequest.Model is empty. The
// model must already be pulled into the local Ollama install.
const DefaultOllamaModel = "qwen3:14b"

// OllamaProvider implements LLM via Ollama's HTTP /api/chat endpoint.
// Prompt caching is not supported by Ollama; the CacheSystem flag is
// silently ignored. Tool calls do not carry IDs on the Ollama wire, so
// this provider fabricates them on receive and drops them on send.
type OllamaProvider struct {
	baseURL string
	http    *http.Client
}

// NewOllamaProvider constructs a provider against baseURL. Pass "" to use
// DefaultOllamaURL.
func NewOllamaProvider(baseURL string, httpClient *http.Client) *OllamaProvider {
	if baseURL == "" {
		baseURL = DefaultOllamaURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OllamaProvider{baseURL: baseURL, http: httpClient}
}

type ollamaToolWrapper struct {
	Type     string       `json:"type"`
	Function ollamaToolFn `json:"function"`
}

type ollamaToolFn struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaChatRequest struct {
	Model    string              `json:"model"`
	Messages []ollamaMessage     `json:"messages"`
	Tools    []ollamaToolWrapper `json:"tools,omitempty"`
	Stream   bool                `json:"stream"`
	Options  map[string]any      `json:"options,omitempty"`
}

type ollamaChatResponse struct {
	Model    string        `json:"model"`
	Message  ollamaMessage `json:"message"`
	Done     bool          `json:"done"`
	DoneReason string      `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

// Generate implements LLM.
func (p *OllamaProvider) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	model := req.Model
	if model == "" {
		model = DefaultOllamaModel
	}

	messages := make([]ollamaMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, ollamaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			// Tool results in Ollama land as separate role=tool messages.
			for _, tr := range m.ToolResults {
				messages = append(messages, ollamaMessage{Role: "tool", Content: tr.Content})
			}
			if m.Text != "" {
				messages = append(messages, ollamaMessage{Role: "user", Content: m.Text})
			}
		case RoleAssistant:
			om := ollamaMessage{Role: "assistant", Content: m.Text}
			for _, tc := range m.ToolCalls {
				var args map[string]any
				if len(tc.Input) > 0 {
					if err := json.Unmarshal(tc.Input, &args); err != nil {
						return nil, fmt.Errorf("ollama: marshal tool input for %q: %w", tc.Name, err)
					}
				}
				var otc ollamaToolCall
				otc.Function.Name = tc.Name
				otc.Function.Arguments = args
				om.ToolCalls = append(om.ToolCalls, otc)
			}
			messages = append(messages, om)
		default:
			return nil, fmt.Errorf("ollama: unknown role %q", m.Role)
		}
	}

	tools := make([]ollamaToolWrapper, 0, len(req.Tools))
	for _, d := range req.Tools {
		schema := d.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, ollamaToolWrapper{
			Type: "function",
			Function: ollamaToolFn{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  schema,
			},
		})
	}

	opts := map[string]any{}
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}

	body, err := json.Marshal(ollamaChatRequest{
		Model:    model,
		Messages: messages,
		Tools:    tools,
		Stream:   false,
		Options:  opts,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: chat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama: %s: %s", resp.Status, string(buf))
	}

	var out ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	gr := &GenerateResponse{
		Text:       out.Message.Content,
		StopReason: out.DoneReason,
		Usage: Usage{
			InputTokens:  int64(out.PromptEvalCount),
			OutputTokens: int64(out.EvalCount),
		},
	}
	for i, tc := range out.Message.ToolCalls {
		argsJSON, err := json.Marshal(tc.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("ollama: re-encode tool args: %w", err)
		}
		gr.ToolCalls = append(gr.ToolCalls, ToolCall{
			ID:    "oll_" + strconv.Itoa(i),
			Name:  tc.Function.Name,
			Input: argsJSON,
		})
	}
	if len(gr.ToolCalls) > 0 && gr.StopReason == "" {
		gr.StopReason = "tool_use"
	}
	return gr, nil
}
