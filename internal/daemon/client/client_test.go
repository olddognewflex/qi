package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"qi/internal/daemon"
	"qi/internal/tools"
	"qi/internal/tools/builtin"
)

// newPipeClient wires a Client to an in-memory server-side Server via net.Pipe.
func newPipeClient(t *testing.T, r *tools.Registry) *Client {
	t.Helper()
	server := daemon.NewServer(r, nil, nil, nil)
	cli, srv := net.Pipe()
	go server.ServeConn(context.Background(), srv)
	c := &Client{conn: cli, br: bufio.NewReader(cli)}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestListToolsEmpty(t *testing.T) {
	c := newPipeClient(t, tools.NewRegistry())
	got, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %d", len(got))
	}
}

func TestListToolsReturnsCapture(t *testing.T) {
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	c := newPipeClient(t, r)
	got, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Name != builtin.CaptureToolName {
		t.Fatalf("got %+v", got)
	}
	if got[0].Source.Kind != tools.SourceLocal {
		t.Fatalf("source = %v", got[0].Source.Kind)
	}
}

func TestCallToolCapture(t *testing.T) {
	inbox := t.TempDir()
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, inbox); err != nil {
		t.Fatalf("register: %v", err)
	}
	c := newPipeClient(t, r)

	raw, err := c.CallTool(context.Background(), builtin.CaptureToolName, map[string]string{"text": "hi"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var res struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(res.Path) != inbox {
		t.Fatalf("path %q not under %q", res.Path, inbox)
	}
}

func TestCallToolMapsNotFound(t *testing.T) {
	c := newPipeClient(t, tools.NewRegistry())
	_, err := c.CallTool(context.Background(), "ghost", nil)
	if !errors.Is(err, tools.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCallToolMapsNotImplemented(t *testing.T) {
	r := tools.NewRegistry()
	src := tools.Source{Kind: tools.SourceMCP, ID: "github"}
	if err := r.RegisterDynamic(src, []tools.Tool{{Name: "x", Source: src}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	c := newPipeClient(t, r)
	_, err := c.CallTool(context.Background(), "x", nil)
	if !errors.Is(err, tools.ErrNotImplemented) {
		t.Fatalf("err = %v, want ErrNotImplemented", err)
	}
}

func TestCallReturnsCallErrorForOtherCodes(t *testing.T) {
	c := newPipeClient(t, tools.NewRegistry())
	_, err := c.Call(context.Background(), "ghost.method", nil)
	var ce *CallError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *CallError", err)
	}
	if ce.Code != daemon.CodeMethodNotFound {
		t.Fatalf("code = %d, want %d", ce.Code, daemon.CodeMethodNotFound)
	}
}

func TestDialUnavailable(t *testing.T) {
	_, err := Dial("/nonexistent/path/qid.sock", 100*time.Millisecond)
	if !errors.Is(err, ErrDaemonUnavailable) {
		t.Fatalf("err = %v, want ErrDaemonUnavailable", err)
	}
}

func TestDialOverRealSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "qid.sock")
	ln, err := daemon.Listen(sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	server := daemon.NewServer(r, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = server.Serve(ctx, ln)
	}()

	c, err := Dial(sock, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	got, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d tools, want 1", len(got))
	}
	_ = c.Close()
	cancel()
	wg.Wait()
}

func TestCallDeadline(t *testing.T) {
	// Server that never replies.
	c, srv := net.Pipe()
	defer c.Close()
	defer srv.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := srv.Read(buf); err != nil {
				return
			}
		}
	}()

	cli := &Client{conn: c, br: bufio.NewReader(c)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := cli.Call(ctx, "tools.list", nil)
	if err == nil {
		t.Fatal("expected deadline error")
	}
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "i/o") {
		t.Fatalf("err = %v, want timeout-related", err)
	}
}
