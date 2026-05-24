// Command qi-mcp is a Model Context Protocol server that re-publishes the
// tools registered in qid to MCP-speaking AI clients (e.g. Claude Desktop).
// It runs as a stdio subprocess of the AI client; on startup it dials qid,
// fetches the live tool catalog, and proxies CallTool requests back through
// the daemon JSON-RPC client.
//
// Mutating tools surface as MCP errors carrying the qid approval id, so the
// human user can run `qi ai approve <id>` from a separate terminal.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"qi/internal/daemon"
	"qi/internal/daemon/client"
	"qi/internal/qimcp"
)

const (
	dialTimeout    = 1 * time.Second
	discoverTimout = 5 * time.Second
	mcpName        = "qi-mcp"
	mcpVersion     = "0"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "qi-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	var socketFlag string
	flag.StringVar(&socketFlag, "socket", "", "override qid socket path (defaults to XDG location)")
	flag.Parse()

	// MCP framing uses stdout; route logs to stderr so they don't corrupt
	// the JSON-RPC stream.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	socketPath := socketFlag
	if socketPath == "" {
		p, err := daemon.SocketPath()
		if err != nil {
			return err
		}
		socketPath = p
	}

	c, err := client.Dial(socketPath, dialTimeout)
	if err != nil {
		return fmt.Errorf("dial qid: %w", err)
	}

	bridge, err := qimcp.NewBridge(c, mcpName, mcpVersion)
	if err != nil {
		return err
	}
	defer func() { _ = bridge.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	discoverCtx, cancel := context.WithTimeout(ctx, discoverTimout)
	n, err := bridge.Discover(discoverCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("discover tools: %w", err)
	}

	log.Info("qi-mcp ready", "session", bridge.SessionID(), "tools", n, "qid", socketPath)
	if err := mcpserver.ServeStdio(bridge.Server()); err != nil {
		return fmt.Errorf("serve stdio: %w", err)
	}
	return nil
}
