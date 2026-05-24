package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNotFound is returned by Execute when no tool with the requested name is
// registered.
var ErrNotFound = errors.New("tool not found")

// ErrNotImplemented is returned when a tool's source has no dispatcher
// available (e.g. MCP tools registered without an attached MCPDispatcher).
var ErrNotImplemented = errors.New("tool dispatch not implemented")

// MCPDispatcher dispatches a call to a tool hosted on a connected MCP
// server. The dispatcher is owned by the MCP client manager; the registry
// holds an optional reference so Execute can route SourceMCP tools without
// importing the mcp package (which would create a cycle).
type MCPDispatcher interface {
	CallMCPTool(ctx context.Context, serverID, toolName string, params json.RawMessage) (json.RawMessage, error)
}

// SetMCPDispatcher attaches a dispatcher for SourceMCP tools. Pass nil to
// detach (e.g. on manager shutdown).
func (r *Registry) SetMCPDispatcher(d MCPDispatcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mcpDisp = d
}

func (r *Registry) mcpDispatcher() MCPDispatcher {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mcpDisp
}

// UpstreamName recovers the upstream MCP tool name from a registered name of
// the form "mcp.<serverID>.<upstream>". If name does not match the prefix
// for source, the original name is returned.
func UpstreamName(name string, source Source) string {
	if source.Kind != SourceMCP {
		return name
	}
	prefix := "mcp." + source.ID + "."
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		return name[len(prefix):]
	}
	return name
}

// Execute dispatches a tool call by name. Local tools run their bound
// handler. MCP tools route through the registry's attached MCPDispatcher.
// Skill dispatch is not yet implemented.
func Execute(ctx context.Context, r *Registry, name string, params json.RawMessage) (json.RawMessage, error) {
	tool, ok := r.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	switch tool.Source.Kind {
	case SourceLocal, SourceSkill:
		h, ok := r.handler(name)
		if !ok {
			return nil, fmt.Errorf("tool %q: %s tool has no handler", name, tool.Source.Kind)
		}
		return h(ctx, params)
	case SourceMCP:
		d := r.mcpDispatcher()
		if d == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotImplemented, tool.Source)
		}
		return d.CallMCPTool(ctx, tool.Source.ID, UpstreamName(name, tool.Source), params)
	default:
		return nil, fmt.Errorf("tool %q: unknown source kind %d", name, tool.Source.Kind)
	}
}
