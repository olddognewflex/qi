// Package qimcp bridges Qi's qid daemon to AI clients via the Model Context
// Protocol. The bridge discovers tools registered in qid, re-publishes them
// on an mcp-go server, and forwards each call back through the daemon
// JSON-RPC client with caller="mcp:<sessionID>" so policy can gate
// mutations behind the approval queue.
package qimcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	mcpwire "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"qi/internal/daemon/client"
)

// CallerPrefix is prepended to each session id when forwarding calls to qid.
// The full caller string looks like "mcp:abcd1234".
const CallerPrefix = "mcp:"

// defaultSchema is used when a qid tool reports no input schema. mcp-go
// requires every tool to declare a schema; an empty object is the loosest
// valid choice.
var defaultSchema = json.RawMessage(`{"type":"object"}`)

// Bridge is a single qi-mcp session. One Bridge wraps one open daemon
// client connection and one mcp-go server; it is not reusable after Close.
type Bridge struct {
	client    *client.Client
	server    *mcpserver.MCPServer
	sessionID string
}

// NewBridge constructs a Bridge bound to an already-dialed daemon client.
// name and version identify the bridge to the MCP client in the
// initialization handshake.
func NewBridge(c *client.Client, name, version string) (*Bridge, error) {
	if c == nil {
		return nil, errors.New("qimcp: client is nil")
	}
	srv := mcpserver.NewMCPServer(name, version,
		mcpserver.WithToolCapabilities(true),
	)
	return &Bridge{
		client:    c,
		server:    srv,
		sessionID: newSessionID(),
	}, nil
}

// SessionID returns the caller suffix used when forwarding tool calls. The
// full caller string is CallerPrefix + SessionID.
func (b *Bridge) SessionID() string { return b.sessionID }

// Caller returns the caller string sent to qid for every forwarded call.
func (b *Bridge) Caller() string { return CallerPrefix + b.sessionID }

// Server returns the underlying mcp-go server. Pass it to ServeStdio or to
// mcp-go's in-process client for testing.
func (b *Bridge) Server() *mcpserver.MCPServer { return b.server }

// Close shuts down the daemon client. The mcp-go server has no Close.
func (b *Bridge) Close() error {
	if b.client == nil {
		return nil
	}
	return b.client.Close()
}

// Discover fetches tools.list from qid and registers each tool on the mcp
// server with a handler that forwards calls back through the daemon client.
func (b *Bridge) Discover(ctx context.Context) (int, error) {
	list, err := b.client.ListTools(ctx)
	if err != nil {
		return 0, fmt.Errorf("qimcp discover: %w", err)
	}
	for _, t := range list {
		schema := t.Schema
		if len(schema) == 0 {
			schema = defaultSchema
		}
		mcpTool := mcpwire.NewToolWithRawSchema(t.Name, t.Description, schema)
		b.server.AddTool(mcpTool, b.makeHandler(t.Name))
	}
	return len(list), nil
}

func (b *Bridge) makeHandler(toolName string) mcpserver.ToolHandlerFunc {
	caller := b.Caller()
	return func(ctx context.Context, req mcpwire.CallToolRequest) (*mcpwire.CallToolResult, error) {
		var args json.RawMessage
		if req.Params.Arguments != nil {
			b, err := json.Marshal(req.Params.Arguments)
			if err != nil {
				return mcpwire.NewToolResultError("encode arguments: " + err.Error()), nil
			}
			args = b
		}
		raw, err := b.client.CallToolAs(ctx, caller, toolName, args)
		if err != nil {
			return mcpwire.NewToolResultError(err.Error()), nil
		}
		if pending, ok := client.IsPending(raw); ok {
			msg := fmt.Sprintf(
				"approval required (id=%s). The user must run: qi ai approve %s",
				pending.ApprovalID, pending.ApprovalID,
			)
			if pending.Reason != "" {
				msg += "\nreason: " + pending.Reason
			}
			return mcpwire.NewToolResultError(msg), nil
		}
		return mcpwire.NewToolResultText(string(raw)), nil
	}
}

func newSessionID() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
