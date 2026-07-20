package client

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"qi/internal/daemon"
)

// scriptedConn wires a Client to a hand-written server side so a test can
// reply — or refuse to reply — exactly as it likes.
func scriptedConn(t *testing.T, serve func(srv net.Conn)) *Client {
	t.Helper()
	cli, srv := net.Pipe()
	go serve(srv)
	c := &Client{conn: cli, br: bufio.NewReader(cli)}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestShutdownSendsCLICaller(t *testing.T) {
	got := make(chan string, 1)
	c := scriptedConn(t, func(srv net.Conn) {
		defer srv.Close()
		br := bufio.NewReader(srv)
		line, err := br.ReadBytes('\n')
		if err != nil {
			return
		}
		var req daemon.Request
		_ = json.Unmarshal(line, &req)
		var p struct {
			Caller string `json:"caller"`
		}
		_ = json.Unmarshal(req.Params, &p)
		got <- p.Caller
		resp := daemon.Response{JSONRPC: daemon.JSONRPCVersion, Result: json.RawMessage(`{"status":"stopping"}`), ID: req.ID}
		b, _ := json.Marshal(resp)
		_, _ = srv.Write(append(b, '\n'))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case caller := <-got:
		if caller != CallerCLI {
			t.Fatalf("caller = %q, want %q", caller, CallerCLI)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never saw the request")
	}
}

// TestShutdownToleratesTruncatedReply pins the back-compat contract documented
// on Shutdown: the daemon closing the conn before its reply lands is success,
// not an error — the request was delivered and shutdown is what it asked for.
func TestShutdownToleratesTruncatedReply(t *testing.T) {
	c := scriptedConn(t, func(srv net.Conn) {
		br := bufio.NewReader(srv)
		_, _ = br.ReadBytes('\n')
		_ = srv.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown should tolerate a closed conn, got %v", err)
	}
}

func TestShutdownSurfacesHandlerErrors(t *testing.T) {
	c := scriptedConn(t, func(srv net.Conn) {
		defer srv.Close()
		br := bufio.NewReader(srv)
		line, _ := br.ReadBytes('\n')
		var req daemon.Request
		_ = json.Unmarshal(line, &req)
		resp := daemon.Response{
			JSONRPC: daemon.JSONRPCVersion,
			Error:   &daemon.RPCError{Code: daemon.CodeHandlerError, Message: "restricted to the cli caller"},
			ID:      req.ID,
		}
		b, _ := json.Marshal(resp)
		_, _ = srv.Write(append(b, '\n'))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Shutdown(ctx); err == nil {
		t.Fatal("expected the handler error to surface")
	}
}

// TestStatusDecodesOlderDaemonPayload is the omitted-safe guarantee: a daemon
// that predates exe/started_at reporting decodes to zero values, which callers
// render as "unknown" rather than failing.
func TestStatusDecodesOlderDaemonPayload(t *testing.T) {
	var st DaemonStatus
	if err := json.Unmarshal([]byte(`{"watcher":{"state":"running"}}`), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Exe != "" || !st.StartedAt.IsZero() {
		t.Fatalf("expected zero exe/started_at, got %+v", st)
	}
	if st.Watcher.State != "running" {
		t.Fatalf("watcher = %+v", st.Watcher)
	}
}

func TestStatusRoundTripsExeAndStartedAt(t *testing.T) {
	want := DaemonStatus{
		Watcher:   WatcherStatus{State: "running", Detail: "watching 3 dirs"},
		Exe:       "/usr/local/bin/qid",
		StartedAt: time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC),
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DaemonStatus
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Exe != want.Exe || !got.StartedAt.Equal(want.StartedAt) || got.Watcher != want.Watcher {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestWaitReadyTimesOutWithoutDaemon(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := WaitReady(ctx, t.TempDir()+"/absent.sock", 200*time.Millisecond); err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("WaitReady took %s, expected it to honor the timeout", elapsed)
	}
}
