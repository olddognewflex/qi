package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpwire "github.com/mark3labs/mcp-go/mcp"

	"qi/internal/tools"
)

// initializeRequest is the standard handshake payload qid presents to every
// MCP server. Kept here so all transports share one shape.
func initializeRequest() mcpwire.InitializeRequest {
	return mcpwire.InitializeRequest{
		Params: mcpwire.InitializeParams{
			ProtocolVersion: mcpwire.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcpwire.ClientCapabilities{},
			ClientInfo: mcpwire.Implementation{
				Name:    "qid",
				Version: "0",
			},
		},
	}
}

// Manager owns the set of live MCP client connections. It implements
// tools.MCPDispatcher so tools.Execute can route SourceMCP calls without
// importing this package.
type Manager struct {
	registry *tools.Registry
	log      *slog.Logger

	mu      sync.Mutex
	clients map[string]*mcpclient.Client
}

// NewManager constructs a Manager bound to the given registry. The manager
// attaches itself as the registry's MCPDispatcher; pass nil log to use the
// default slog logger.
func NewManager(r *tools.Registry, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		registry: r,
		log:      log,
		clients:  make(map[string]*mcpclient.Client),
	}
	r.SetMCPDispatcher(m)
	return m
}

// Connect launches an external MCP server via stdio, performs the protocol
// handshake, and registers its tools into the catalog. If the server was
// already connected under the same ID, an error is returned; call Disconnect
// first.
func (m *Manager) Connect(ctx context.Context, spec ServerSpec) error {
	if err := spec.validate(); err != nil {
		return err
	}
	c, err := mcpclient.NewStdioMCPClient(spec.Command, spec.envSlice(), spec.Args...)
	if err != nil {
		return fmt.Errorf("mcp %q: spawn: %w", spec.ID, err)
	}
	if err := m.attach(ctx, spec.ID, c); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

// Attach binds an already-constructed mcp-go client to the manager under the
// given serverID. Useful for in-process clients in tests and for transports
// other than stdio. The manager assumes ownership of the client and will
// close it on Disconnect.
func (m *Manager) Attach(ctx context.Context, serverID string, c *mcpclient.Client) error {
	if serverID == "" {
		return fmt.Errorf("mcp attach: serverID is required")
	}
	return m.attach(ctx, serverID, c)
}

func (m *Manager) attach(ctx context.Context, serverID string, c *mcpclient.Client) error {
	m.mu.Lock()
	if _, exists := m.clients[serverID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("mcp %q: already connected", serverID)
	}
	m.mu.Unlock()

	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("mcp %q: start: %w", serverID, err)
	}
	if _, err := c.Initialize(ctx, initializeRequest()); err != nil {
		return fmt.Errorf("mcp %q: initialize: %w", serverID, err)
	}
	list, err := c.ListTools(ctx, mcpwire.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("mcp %q: list tools: %w", serverID, err)
	}

	source := tools.Source{Kind: tools.SourceMCP, ID: serverID}
	registered := make([]tools.Tool, 0, len(list.Tools))
	for _, t := range list.Tools {
		schema, err := json.Marshal(t.InputSchema)
		if err != nil {
			return fmt.Errorf("mcp %q: marshal schema for %q: %w", serverID, t.Name, err)
		}
		registered = append(registered, tools.Tool{
			Name:        NamespacedName(serverID, t.Name),
			Source:      source,
			Mutating:    true,
			Description: t.Description,
			Schema:      schema,
		})
	}
	if err := m.registry.RegisterDynamic(source, registered); err != nil {
		return fmt.Errorf("mcp %q: register: %w", serverID, err)
	}

	m.mu.Lock()
	m.clients[serverID] = c
	m.mu.Unlock()

	m.log.Info("mcp connected", "server", serverID, "tools", len(registered))
	return nil
}

// Disconnect closes the client for serverID and removes its tools from the
// registry. Returns nil if no such server is connected.
func (m *Manager) Disconnect(serverID string) error {
	m.mu.Lock()
	c, ok := m.clients[serverID]
	if ok {
		delete(m.clients, serverID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	m.registry.Unregister(tools.Source{Kind: tools.SourceMCP, ID: serverID})
	if err := c.Close(); err != nil {
		return fmt.Errorf("mcp %q: close: %w", serverID, err)
	}
	m.log.Info("mcp disconnected", "server", serverID)
	return nil
}

// Close shuts down every connected client and detaches from the registry.
func (m *Manager) Close() error {
	m.mu.Lock()
	ids := make([]string, 0, len(m.clients))
	for id := range m.clients {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	var firstErr error
	for _, id := range ids {
		if err := m.Disconnect(id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.registry.SetMCPDispatcher(nil)
	return firstErr
}

// CallMCPTool implements tools.MCPDispatcher. params, if non-empty, must be
// a JSON object — mcp-go's CallTool API expects map[string]any arguments.
func (m *Manager) CallMCPTool(ctx context.Context, serverID, toolName string, params json.RawMessage) (json.RawMessage, error) {
	m.mu.Lock()
	c, ok := m.clients[serverID]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("mcp %q: not connected", serverID)
	}

	var args map[string]any
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, fmt.Errorf("mcp %q.%s: parse arguments: %w", serverID, toolName, err)
		}
	}

	req := mcpwire.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args

	res, err := c.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp %q.%s: %w", serverID, toolName, err)
	}
	if res.IsError {
		return nil, fmt.Errorf("mcp %q.%s: tool reported error: %s", serverID, toolName, callResultErrorText(res))
	}
	return json.Marshal(res)
}

func callResultErrorText(res *mcpwire.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(mcpwire.TextContent); ok {
			return tc.Text
		}
	}
	return "(no detail)"
}
