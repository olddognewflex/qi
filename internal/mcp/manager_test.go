package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpwire "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"qi/internal/tools"
)

const testServerID = "testsrv"

func newEchoServer(t *testing.T) *mcpserver.MCPServer {
	t.Helper()
	srv := mcpserver.NewMCPServer("echo-server", "0.1.0")
	tool := mcpwire.NewTool(
		"echo",
		mcpwire.WithDescription("Returns the supplied text unchanged."),
		mcpwire.WithString("text", mcpwire.Required(), mcpwire.Description("Text to echo back")),
	)
	srv.AddTool(tool, func(ctx context.Context, req mcpwire.CallToolRequest) (*mcpwire.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		text, _ := args["text"].(string)
		return mcpwire.NewToolResultText("echo:" + text), nil
	})
	return srv
}

func newAttachedManager(t *testing.T) (*Manager, *tools.Registry) {
	t.Helper()
	registry := tools.NewRegistry()
	mgr := NewManager(registry, nil)

	c, err := mcpclient.NewInProcessClient(newEchoServer(t))
	if err != nil {
		t.Fatalf("new in-process client: %v", err)
	}
	if err := mgr.Attach(context.Background(), testServerID, c); err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr, registry
}

func TestAttachRegistersTools(t *testing.T) {
	_, registry := newAttachedManager(t)

	want := NamespacedName(testServerID, "echo")
	got, ok := registry.Lookup(want)
	if !ok {
		t.Fatalf("expected %q in registry; got %+v", want, registry.List())
	}
	if got.Source.Kind != tools.SourceMCP || got.Source.ID != testServerID {
		t.Fatalf("source = %+v, want mcp:%s", got.Source, testServerID)
	}
	if !got.Mutating {
		t.Fatal("MCP tools default to mutating=true")
	}
	if len(got.Schema) == 0 {
		t.Fatal("schema should be populated from upstream tool")
	}
}

func TestExecuteRoutesToMCP(t *testing.T) {
	_, registry := newAttachedManager(t)

	params := json.RawMessage(`{"text":"hi"}`)
	out, err := tools.Execute(context.Background(), registry, NamespacedName(testServerID, "echo"), params)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(string(out), "echo:hi") {
		t.Fatalf("result did not include echoed text: %s", out)
	}
}

func TestDisconnectRemovesTools(t *testing.T) {
	mgr, registry := newAttachedManager(t)
	if err := mgr.Disconnect(testServerID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if _, ok := registry.Lookup(NamespacedName(testServerID, "echo")); ok {
		t.Fatal("tool should be gone after disconnect")
	}
	_, err := tools.Execute(context.Background(), registry, NamespacedName(testServerID, "echo"), nil)
	if !errors.Is(err, tools.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDispatchFailsWhenServerGone(t *testing.T) {
	mgr, registry := newAttachedManager(t)

	c2, err := mcpclient.NewInProcessClient(newEchoServer(t))
	if err != nil {
		t.Fatalf("client 2: %v", err)
	}
	if err := mgr.Attach(context.Background(), "other", c2); err != nil {
		t.Fatalf("attach other: %v", err)
	}

	_ = mgr.Disconnect("other")
	if _, ok := registry.Lookup(NamespacedName("other", "echo")); ok {
		t.Fatal("tools for disconnected server should be gone")
	}
}

func TestAttachRejectsDuplicateID(t *testing.T) {
	mgr, _ := newAttachedManager(t)
	c, err := mcpclient.NewInProcessClient(newEchoServer(t))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer c.Close()
	if err := mgr.Attach(context.Background(), testServerID, c); err == nil {
		t.Fatal("expected duplicate-id error")
	}
}

func TestServerSpecValidation(t *testing.T) {
	cases := []ServerSpec{
		{ID: "", Command: "x"},
		{ID: "has.dot", Command: "x"},
		{ID: "spaced id", Command: "x"},
		{ID: "ok", Command: ""},
	}
	for _, c := range cases {
		if err := c.validate(); err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
	good := ServerSpec{ID: "github", Command: "/bin/echo"}
	if err := good.validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpstreamName(t *testing.T) {
	src := tools.Source{Kind: tools.SourceMCP, ID: "github"}
	if got := tools.UpstreamName("mcp.github.create_issue", src); got != "create_issue" {
		t.Fatalf("got %q", got)
	}
	if got := tools.UpstreamName("mcp.github.list.repos", src); got != "list.repos" {
		t.Fatalf("expected upstream name to keep trailing dots, got %q", got)
	}
	local := tools.Source{Kind: tools.SourceLocal}
	if got := tools.UpstreamName("vault.capture", local); got != "vault.capture" {
		t.Fatalf("local source should be returned unchanged, got %q", got)
	}
}

func TestMCPNotImplementedWithoutDispatcher(t *testing.T) {
	r := tools.NewRegistry()
	src := tools.Source{Kind: tools.SourceMCP, ID: "ghost"}
	if err := r.RegisterDynamic(src, []tools.Tool{{Name: NamespacedName("ghost", "thing"), Source: src}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := tools.Execute(context.Background(), r, NamespacedName("ghost", "thing"), nil)
	if !errors.Is(err, tools.ErrNotImplemented) {
		t.Fatalf("err = %v, want ErrNotImplemented", err)
	}
}
