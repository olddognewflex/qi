package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultAnthropicModel is the model the Anthropic provider falls back to
// when GenerateRequest.Model is empty.
const DefaultAnthropicModel = "claude-sonnet-4-6"

// AnthropicProvider implements LLM against the official Anthropic SDK.
type AnthropicProvider struct {
	client *anthropic.Client
}

// NewAnthropicProvider constructs an AnthropicProvider. apiKey may be empty;
// the SDK then falls back to ANTHROPIC_API_KEY in the environment.
func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	var opts []option.RequestOption
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	c := anthropic.NewClient(opts...)
	return &AnthropicProvider{client: &c}
}

// Generate implements LLM.
func (p *AnthropicProvider) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	model := req.Model
	if model == "" {
		model = DefaultAnthropicModel
	}
	maxTokens := int64(req.MaxTokens)
	if maxTokens == 0 {
		maxTokens = 2048
	}

	tools, err := anthropicTools(req.Tools)
	if err != nil {
		return nil, err
	}
	messages, err := anthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	systemBlock := anthropic.TextBlockParam{Text: req.System}
	if req.CacheSystem && req.System != "" {
		systemBlock.CacheControl = anthropic.NewCacheControlEphemeralParam()
	}

	msg, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Messages:  messages,
		Tools:     tools,
		System:    []anthropic.TextBlockParam{systemBlock},
	})
	if err != nil {
		// Surface API-level failures as ProviderError so the fallback
		// chain can classify them without importing the SDK.
		var apierr *anthropic.Error
		if errors.As(err, &apierr) {
			return nil, &ProviderError{Provider: "anthropic", StatusCode: apierr.StatusCode, Body: apierr.Error()}
		}
		return nil, err
	}

	out := &GenerateResponse{StopReason: string(msg.StopReason)}
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			if out.Text != "" {
				out.Text += "\n"
			}
			out.Text += b.Text
		case "tool_use":
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage(b.JSON.Input.Raw())
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:    b.ID,
				Name:  b.Name,
				Input: input,
			})
		}
	}
	out.Usage = Usage{
		InputTokens:         msg.Usage.InputTokens,
		OutputTokens:        msg.Usage.OutputTokens,
		CacheCreationTokens: msg.Usage.CacheCreationInputTokens,
		CacheReadTokens:     msg.Usage.CacheReadInputTokens,
	}
	return out, nil
}

func anthropicTools(defs []ToolDef) ([]anthropic.ToolUnionParam, error) {
	out := make([]anthropic.ToolUnionParam, 0, len(defs))
	for _, d := range defs {
		schema, err := anthropicSchema(d.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("ai: schema for %q: %w", d.Name, err)
		}
		tp := anthropic.ToolParam{
			Name:        d.Name,
			InputSchema: schema,
		}
		if d.Description != "" {
			tp.Description = anthropic.String(d.Description)
		}
		copyTP := tp
		out = append(out, anthropic.ToolUnionParam{OfTool: &copyTP})
	}
	return out, nil
}

func anthropicSchema(schemaJSON json.RawMessage) (anthropic.ToolInputSchemaParam, error) {
	if len(schemaJSON) == 0 {
		return anthropic.ToolInputSchemaParam{Properties: map[string]any{}}, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(schemaJSON, &raw); err != nil {
		return anthropic.ToolInputSchemaParam{}, err
	}
	out := anthropic.ToolInputSchemaParam{}
	if props, ok := raw["properties"].(map[string]any); ok {
		out.Properties = props
	} else {
		out.Properties = map[string]any{}
	}
	if reqAny, ok := raw["required"].([]any); ok {
		req := make([]string, 0, len(reqAny))
		for _, r := range reqAny {
			if s, ok := r.(string); ok {
				req = append(req, s)
			}
		}
		out.Required = req
	}
	extras := map[string]any{}
	for k, v := range raw {
		if k == "properties" || k == "required" || k == "type" {
			continue
		}
		extras[k] = v
	}
	if len(extras) > 0 {
		out.ExtraFields = extras
	}
	return out, nil
}

func anthropicMessages(in []Message) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(in))
	for _, m := range in {
		switch m.Role {
		case RoleUser:
			var blocks []anthropic.ContentBlockParamUnion
			if m.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Text))
			}
			for _, r := range m.ToolResults {
				blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropic.NewUserMessage(blocks...))
		case RoleAssistant:
			var blocks []anthropic.ContentBlockParamUnion
			if m.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Text))
			}
			for _, tc := range m.ToolCalls {
				var inputObj any
				if len(tc.Input) > 0 {
					if err := json.Unmarshal(tc.Input, &inputObj); err != nil {
						return nil, fmt.Errorf("ai: marshal tool input for %q: %w", tc.Name, err)
					}
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, inputObj, tc.Name))
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		default:
			return nil, fmt.Errorf("ai: unknown role %q", m.Role)
		}
	}
	return out, nil
}
