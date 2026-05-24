package qimcp

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpwire "github.com/mark3labs/mcp-go/mcp"

	"qi/internal/approval"
	"qi/internal/daemon"
	"qi/internal/daemon/client"
	"qi/internal/policy"
	"qi/internal/tools"
	"qi/internal/tools/builtin"
)

// pipeDaemon spawns an in-pipe qid server backed by registry r and queue q
// and returns a daemon client connected to it.
func pipeDaemon(t *testing.T, r *tools.Registry, q *approval.Queue) *client.Client {
	t.Helper()
	server := daemon.NewServer(r, policy.DefaultDecider{}, q, nil)
	cliConn, srvConn := net.Pipe()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		server.ServeConn(context.Background(), srvConn)
	}()
	c := client.NewFromConn(cliConn)
	t.Cleanup(func() {
		_ = cliConn.Close()
		wg.Wait()
	})
	return c
}

func TestBridgeForwardsReadOnlyCall(t *testing.T) {
	r := tools.NewRegistry()
	const toolName = "ro.echo"
	echo := func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(params, &p)
		out, _ := json.Marshal(map[string]string{"echoed": p.Text})
		return out, nil
	}
	if err := r.RegisterLocal(tools.Tool{
		Name:        toolName,
		Description: "Echoes input",
		Mutating:    false,
		Schema:      json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
	}, echo); err != nil {
		t.Fatalf("register: %v", err)
	}

	c := pipeDaemon(t, r, approval.NewQueue(nil))
	bridge, err := NewBridge(c, "qi-mcp-test", "0")
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if !strings.HasPrefix(bridge.Caller(), CallerPrefix) {
		t.Fatalf("caller = %q", bridge.Caller())
	}
	if _, err := bridge.Discover(context.Background()); err != nil {
		t.Fatalf("discover: %v", err)
	}

	mc, err := mcpclient.NewInProcessClient(bridge.Server())
	if err != nil {
		t.Fatalf("inproc client: %v", err)
	}
	defer mc.Close()
	if err := mc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := mc.Initialize(context.Background(), initRequest()); err != nil {
		t.Fatalf("init: %v", err)
	}
	listOut, err := mc.ListTools(context.Background(), mcpwire.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listOut.Tools) != 1 || listOut.Tools[0].Name != toolName {
		t.Fatalf("listed = %+v", listOut.Tools)
	}

	callReq := mcpwire.CallToolRequest{}
	callReq.Params.Name = toolName
	callReq.Params.Arguments = map[string]any{"text": "ping"}
	res, err := mc.CallTool(context.Background(), callReq)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	body := textContent(res)
	if !strings.Contains(body, `"echoed":"ping"`) {
		t.Fatalf("result content = %q", body)
	}
}

func TestBridgeReturnsPendingForMutatingTool(t *testing.T) {
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	q := approval.NewQueue(nil)
	c := pipeDaemon(t, r, q)

	bridge, err := NewBridge(c, "qi-mcp-test", "0")
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if _, err := bridge.Discover(context.Background()); err != nil {
		t.Fatalf("discover: %v", err)
	}

	mc, err := mcpclient.NewInProcessClient(bridge.Server())
	if err != nil {
		t.Fatalf("inproc client: %v", err)
	}
	defer mc.Close()
	if err := mc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := mc.Initialize(context.Background(), initRequest()); err != nil {
		t.Fatalf("init: %v", err)
	}

	callReq := mcpwire.CallToolRequest{}
	callReq.Params.Name = builtin.CaptureToolName
	callReq.Params.Arguments = map[string]any{"text": "from-mcp"}
	res, err := mc.CallTool(context.Background(), callReq)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for pending approval, got %s", textContent(res))
	}
	if !strings.Contains(textContent(res), "approval required") {
		t.Fatalf("expected approval message, got %q", textContent(res))
	}

	pending := q.List(approval.StatusPending)
	if len(pending) != 1 {
		t.Fatalf("queue has %d, want 1", len(pending))
	}
	if pending[0].Caller != bridge.Caller() {
		t.Fatalf("caller = %q, want %q", pending[0].Caller, bridge.Caller())
	}
	if pending[0].ToolName != builtin.CaptureToolName {
		t.Fatalf("tool = %q", pending[0].ToolName)
	}
}

func TestSessionIDDistinct(t *testing.T) {
	r := tools.NewRegistry()
	c1 := pipeDaemon(t, r, approval.NewQueue(nil))
	c2 := pipeDaemon(t, r, approval.NewQueue(nil))
	b1, _ := NewBridge(c1, "x", "0")
	b2, _ := NewBridge(c2, "x", "0")
	if b1.SessionID() == b2.SessionID() {
		t.Fatalf("session ids collided: %s", b1.SessionID())
	}
}

func initRequest() mcpwire.InitializeRequest {
	return mcpwire.InitializeRequest{
		Params: mcpwire.InitializeParams{
			ProtocolVersion: mcpwire.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcpwire.ClientCapabilities{},
			ClientInfo:      mcpwire.Implementation{Name: "qimcp-test", Version: "0"},
		},
	}
}

func textContent(res *mcpwire.CallToolResult) string {
	var out strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcpwire.TextContent); ok {
			out.WriteString(tc.Text)
		}
	}
	return out.String()
}

