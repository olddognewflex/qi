package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Handler executes a Local tool. Params and result are JSON-encoded so the
// same shape carries across the JSON-RPC wire boundary later.
type Handler func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

// Registry is the in-memory tool catalog. Safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	tools    map[string]Tool
	handlers map[string]Handler
	mcpDisp  MCPDispatcher
}

func NewRegistry() *Registry {
	return &Registry{
		tools:    make(map[string]Tool),
		handlers: make(map[string]Handler),
	}
}

// Register adds a single tool. Returns an error if a tool with the same name
// is already registered or if the tool fails validation. Local tools should
// be registered with RegisterLocal instead so a handler is bound.
func (r *Registry) Register(t Tool) error {
	if err := t.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("tool %q already registered", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// RegisterLocal adds a Local tool and binds its handler. The tool's
// Source.Kind is set to SourceLocal regardless of input.
func (r *Registry) RegisterLocal(t Tool, h Handler) error {
	t.Source = Source{Kind: SourceLocal}
	return r.registerWithHandler(t, h)
}

// RegisterSkill adds a Skill tool (deterministic composed workflow) and
// binds its handler. Skill tools are dispatched the same way as Local tools
// but carry a distinct SourceKind for display and policy purposes.
func (r *Registry) RegisterSkill(t Tool, h Handler) error {
	t.Source = Source{Kind: SourceSkill}
	return r.registerWithHandler(t, h)
}

func (r *Registry) registerWithHandler(t Tool, h Handler) error {
	if h == nil {
		return fmt.Errorf("tool %q: handler is nil", t.Name)
	}
	if err := t.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("tool %q already registered", t.Name)
	}
	r.tools[t.Name] = t
	r.handlers[t.Name] = h
	return nil
}

// handler returns the bound handler for a Local tool, if any.
func (r *Registry) handler(name string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[name]
	return h, ok
}

// RegisterDynamic replaces all tools currently registered under the given
// source with the supplied set. Used by the MCP client manager when a server
// connects or its tool list changes. All-or-nothing: if any tool in the batch
// is invalid or conflicts with a tool from a different source, no changes are
// applied.
func (r *Registry) RegisterDynamic(source Source, tools []Tool) error {
	for _, t := range tools {
		if t.Source != source {
			return fmt.Errorf("tool %q: source %s does not match batch source %s", t.Name, t.Source, source)
		}
		if err := t.validate(); err != nil {
			return err
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, t := range tools {
		if existing, ok := r.tools[t.Name]; ok && existing.Source != source {
			return fmt.Errorf("tool %q already registered by source %s", t.Name, existing.Source)
		}
	}

	for name, existing := range r.tools {
		if existing.Source == source {
			delete(r.tools, name)
			delete(r.handlers, name)
		}
	}
	for _, t := range tools {
		r.tools[t.Name] = t
	}
	return nil
}

// Unregister removes all tools belonging to the given source. Any bound
// handlers for those tools are dropped. Returns the number of tools removed.
func (r *Registry) Unregister(source Source) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for name, t := range r.tools {
		if t.Source == source {
			delete(r.tools, name)
			delete(r.handlers, name)
			n++
		}
	}
	return n
}

// Lookup returns the tool with the given name. The bool is false if no such
// tool is registered.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools sorted by name.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}
