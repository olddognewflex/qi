// Package builtin wires Qi's compiled-in Local tools into a tools.Registry.
// Each tool here is a thin adapter over an existing vault or service
// function, exposed with a stable name and JSON schema so qid can dispatch it
// from the JSON-RPC API and, later, qi-mcp.
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qi/internal/tools"
	"qi/internal/vault"
)

// CaptureToolName is the registered name of the vault capture tool.
const CaptureToolName = "vault.capture"

// captureSchema is the JSON Schema for vault.capture input parameters.
var captureSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "minLength": 1,
      "description": "Free-form capture text. Whitespace-trimmed; must not be empty after trimming."
    }
  },
  "required": ["text"],
  "additionalProperties": false
}`)

type captureParams struct {
	Text string `json:"text"`
}

type captureResult struct {
	Path string `json:"path"`
}

// RegisterCapture binds vault.capture to the supplied inbox directory. The
// inbox path must be set; the tool returns an error if the registry attempts
// to use it with an empty path.
func RegisterCapture(r *tools.Registry, inboxDir string) error {
	if inboxDir == "" {
		return fmt.Errorf("vault.capture: inbox dir is empty")
	}
	tool := tools.Tool{
		Name:        CaptureToolName,
		Version:     "1",
		Mutating:    true,
		Description: "Write a timestamped capture note to the vault inbox.",
		Schema:      captureSchema,
	}
	handler := func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p captureParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("vault.capture: parse params: %w", err)
		}
		if strings.TrimSpace(p.Text) == "" {
			return nil, fmt.Errorf("vault.capture: text is empty")
		}
		path, err := vault.WriteCapture(inboxDir, p.Text)
		if err != nil {
			return nil, err
		}
		return json.Marshal(captureResult{Path: path})
	}
	return r.RegisterLocal(tool, handler)
}
