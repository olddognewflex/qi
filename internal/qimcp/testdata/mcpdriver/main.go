// Command mcpdriver is a smoke-test fixture: it spawns qi-mcp via stdio,
// runs the MCP handshake, lists tools, and calls vault.capture. Used by
// the stage-8 end-to-end script, not part of the shipped product.
//
//	go run ./internal/qimcp/testdata/mcpdriver <qi-mcp-binary> <qid-socket>
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpwire "github.com/mark3labs/mcp-go/mcp"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatalf("usage: mcpdriver <qi-mcp-binary> <qid-socket>")
	}
	bin := os.Args[1]
	sock := os.Args[2]

	c, err := mcpclient.NewStdioMCPClient(bin, nil, "-socket", sock)
	if err != nil {
		log.Fatalf("spawn qi-mcp: %v", err)
	}
	defer c.Close()

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		log.Fatalf("start: %v", err)
	}
	init := mcpwire.InitializeRequest{}
	init.Params.ProtocolVersion = mcpwire.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpwire.Implementation{Name: "mcpdriver", Version: "0"}
	if _, err := c.Initialize(ctx, init); err != nil {
		log.Fatalf("init: %v", err)
	}

	list, err := c.ListTools(ctx, mcpwire.ListToolsRequest{})
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	fmt.Println("--- tools exposed by qi-mcp ---")
	for _, t := range list.Tools {
		fmt.Printf("  %s — %s\n", t.Name, t.Description)
	}

	fmt.Println("--- CallTool vault.capture ---")
	call := mcpwire.CallToolRequest{}
	call.Params.Name = "vault.capture"
	call.Params.Arguments = map[string]any{"text": "hello from Claude via qi-mcp"}
	res, err := c.CallTool(ctx, call)
	if err != nil {
		log.Fatalf("call: %v", err)
	}
	for _, content := range res.Content {
		if tc, ok := content.(mcpwire.TextContent); ok {
			fmt.Println("  ", tc.Text)
		}
	}
	if res.IsError {
		fmt.Println("  (IsError=true — expected: mutation routed to approval queue)")
	}
}
