// Command echoserver is a minimal stdio MCP server used as a test fixture
// for qid's MCP wiring. It exposes a single "echo" tool that returns its
// "text" argument prefixed with "echo:". Not built by default — invoked
// only by go run from tests or smoke scripts.
package main

import (
	"context"
	"log"

	mcpwire "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func main() {
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
	if err := mcpserver.ServeStdio(srv); err != nil {
		log.Fatalf("serve stdio: %v", err)
	}
}
