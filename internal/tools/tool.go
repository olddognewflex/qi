// Package tools is the in-memory catalog of executable tools available to qid.
// Tools come from three places: compiled-in Go code (Local), connected MCP
// servers (MCP), or composed deterministic workflows (Skill). The registry is
// derived state — it can be rebuilt from code plus live MCP connections, and
// is never persisted.
package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SourceKind identifies where a tool's implementation lives.
type SourceKind int

const (
	SourceLocal SourceKind = iota + 1
	SourceMCP
	SourceSkill
)

func (k SourceKind) String() string {
	switch k {
	case SourceLocal:
		return "local"
	case SourceMCP:
		return "mcp"
	case SourceSkill:
		return "skill"
	default:
		return "unknown"
	}
}

func (k SourceKind) MarshalJSON() ([]byte, error) {
	s := k.String()
	if s == "unknown" {
		return nil, fmt.Errorf("invalid source kind %d", k)
	}
	return json.Marshal(s)
}

func (k *SourceKind) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	switch s {
	case "local":
		*k = SourceLocal
	case "mcp":
		*k = SourceMCP
	case "skill":
		*k = SourceSkill
	default:
		return fmt.Errorf("unknown source kind %q", s)
	}
	return nil
}

// Source describes the origin of a tool. ID is set only for MCP tools, where
// it names the connected server (e.g. "github", "obsidian"). For Local and
// Skill tools, ID is empty.
type Source struct {
	Kind SourceKind `json:"kind"`
	ID   string     `json:"id,omitempty"`
}

func (s Source) String() string {
	if s.Kind == SourceMCP {
		return "mcp:" + s.ID
	}
	return s.Kind.String()
}

// Tool is a single entry in the registry. Schema is the JSON Schema for the
// tool's input parameters; it may be nil during early development but the
// runtime executor will require it before dispatching calls.
type Tool struct {
	Name        string          `json:"name"`
	Source      Source          `json:"source"`
	Version     string          `json:"version,omitempty"`
	Mutating    bool            `json:"mutating"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

func (t Tool) validate() error {
	if t.Name == "" {
		return fmt.Errorf("tool name is empty")
	}
	if strings.ContainsAny(t.Name, " \t\n\r") {
		return fmt.Errorf("tool name %q contains whitespace", t.Name)
	}
	switch t.Source.Kind {
	case SourceLocal, SourceSkill:
		if t.Source.ID != "" {
			return fmt.Errorf("tool %q: source %s must not set ID", t.Name, t.Source.Kind)
		}
	case SourceMCP:
		if t.Source.ID == "" {
			return fmt.Errorf("tool %q: MCP source requires server ID", t.Name)
		}
	default:
		return fmt.Errorf("tool %q: unknown source kind %d", t.Name, t.Source.Kind)
	}
	return nil
}
