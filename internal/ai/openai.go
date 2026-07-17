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

// OpenAIProvider implements LLM via the OpenAI chat-completions wire format,
// which OpenAI, Moonshot (Kimi), OpenCode Zen, and Z.AI all speak. The
// provider is endpoint-agnostic: name labels errors, baseURL selects the
// service. Prompt caching is provider-managed on this wire; CacheSystem is
// silently ignored. There is no default model — chat-completions services
// share no common model id, so Generate requires req.Model.
type OpenAIProvider struct {
	name    string
	baseURL string
	apiKey  string
	http    *http.Client
	// OpenAI proper rejects max_tokens for reasoning models and wants
	// max_completion_tokens; the compatible providers only accept max_tokens.
	useMaxCompletionTokens bool
}

// NewOpenAIProvider constructs a provider for an OpenAI-compatible endpoint.
// name is the label used in errors ("openai", "kimi", ...); baseURL is the
// API root without the /chat/completions suffix. apiKey is sent as a Bearer
// token when non-empty.
func NewOpenAIProvider(name, baseURL, apiKey string, httpClient *http.Client) *OpenAIProvider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OpenAIProvider{
		name:                   name,
		baseURL:                baseURL,
		apiKey:                 apiKey,
		http:                   httpClient,
		useMaxCompletionTokens: Provider(name) == ProviderOpenAI,
	}
}

type oaiFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded, as a string
}

type oaiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function oaiFunctionCall `json:"function"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiTool struct {
	Type     string     `json:"type"`
	Function oaiToolDef `json:"function"`
}

type oaiChatRequest struct {
	Model               string       `json:"model"`
	Messages            []oaiMessage `json:"messages"`
	Tools               []oaiTool    `json:"tools,omitempty"`
	MaxTokens           int          `json:"max_tokens,omitempty"`
	MaxCompletionTokens int          `json:"max_completion_tokens,omitempty"`
}

type oaiChatResponse struct {
	Choices []struct {
		Message      oaiMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// Generate implements LLM.
func (p *OpenAIProvider) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("%s: model is required (set model in [[ai.providers]] or pass --model)", p.name)
	}

	messages := make([]oaiMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, oaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			for _, tr := range m.ToolResults {
				messages = append(messages, oaiMessage{Role: "tool", ToolCallID: tr.CallID, Content: tr.Content})
			}
			if m.Text != "" {
				messages = append(messages, oaiMessage{Role: "user", Content: m.Text})
			}
		case RoleAssistant:
			om := oaiMessage{Role: "assistant", Content: m.Text}
			for _, tc := range m.ToolCalls {
				args := string(tc.Input)
				if args == "" {
					args = "{}"
				}
				om.ToolCalls = append(om.ToolCalls, oaiToolCall{
					ID:       tc.ID,
					Type:     "function",
					Function: oaiFunctionCall{Name: tc.Name, Arguments: args},
				})
			}
			messages = append(messages, om)
		default:
			return nil, fmt.Errorf("%s: unknown role %q", p.name, m.Role)
		}
	}

	tools := make([]oaiTool, 0, len(req.Tools))
	for _, d := range req.Tools {
		schema := d.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, oaiTool{
			Type: "function",
			Function: oaiToolDef{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  schema,
			},
		})
	}

	chatReq := oaiChatRequest{
		Model:    req.Model,
		Messages: messages,
		Tools:    tools,
	}
	if p.useMaxCompletionTokens {
		chatReq.MaxCompletionTokens = req.MaxTokens
	} else {
		chatReq.MaxTokens = req.MaxTokens
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", p.name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: chat request: %w", p.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &ProviderError{Provider: p.name, StatusCode: resp.StatusCode, Body: string(buf)}
	}

	var out oaiChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", p.name, err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("%s: response has no choices", p.name)
	}
	choice := out.Choices[0]

	gr := &GenerateResponse{
		Text:       choice.Message.Content,
		StopReason: choice.FinishReason,
		Usage: Usage{
			InputTokens:  out.Usage.PromptTokens,
			OutputTokens: out.Usage.CompletionTokens,
		},
	}
	for i, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return nil, fmt.Errorf("%s: tool call %q has invalid JSON arguments", p.name, tc.Function.Name)
		}
		id := tc.ID
		if id == "" {
			id = "oai_" + strconv.Itoa(i)
		}
		gr.ToolCalls = append(gr.ToolCalls, ToolCall{
			ID:    id,
			Name:  tc.Function.Name,
			Input: json.RawMessage(args),
		})
	}
	if len(gr.ToolCalls) > 0 && gr.StopReason == "" {
		gr.StopReason = "tool_use"
	}
	return gr, nil
}
