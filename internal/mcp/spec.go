// Package mcp connects qid to external Model Context Protocol servers,
// surfaces their tools into the qi tools.Registry under the namespace
// "mcp.<serverID>.<toolName>", and routes tools.Execute calls back through
// the appropriate connected client.
package mcp

import (
	"fmt"
	"strings"
)

// ServerSpec describes a stdio-launched MCP server. ID is the logical name
// used for namespacing tools; it must not contain dots.
type ServerSpec struct {
	ID      string
	Command string
	Args    []string
	Env     map[string]string
}

func (s ServerSpec) validate() error {
	if s.ID == "" {
		return fmt.Errorf("mcp server: id is required")
	}
	if strings.ContainsAny(s.ID, ". \t\n\r") {
		return fmt.Errorf("mcp server %q: id must not contain dots or whitespace", s.ID)
	}
	if s.Command == "" {
		return fmt.Errorf("mcp server %q: command is required", s.ID)
	}
	return nil
}

func (s ServerSpec) envSlice() []string {
	if len(s.Env) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.Env))
	for k, v := range s.Env {
		out = append(out, k+"="+v)
	}
	return out
}

// NamespacedName returns the registry name used for an MCP tool from this
// server (e.g. "mcp.github.create_issue").
func NamespacedName(serverID, toolName string) string {
	return "mcp." + serverID + "." + toolName
}
