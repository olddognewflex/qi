package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qi/internal/daemon"
	daemonclient "qi/internal/daemon/client"
	"qi/internal/policy"
	"qi/internal/tools"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestShutdownHandlerCallerGate is the control-plane equivalent of invariant
// #3: only the cli caller may stop the daemon. An ai-planner or mcp session
// must be refused, and the serve context must survive the attempt.
func TestShutdownHandlerCallerGate(t *testing.T) {
	tests := []struct {
		name       string
		params     string
		wantCancel bool
	}{
		{"cli caller stops the daemon", `{"caller":"cli"}`, true},
		{"ai planner is refused", `{"caller":"ai-planner:abc"}`, false},
		{"mcp session is refused", `{"caller":"mcp:abc"}`, false},
		{"empty caller is refused", `{"caller":""}`, false},
		{"missing caller is refused", `{}`, false},
		{"absent params are refused", ``, false},
		{"cli-lookalike caller is refused", `{"caller":"clix"}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canceled := make(chan struct{})
			handler := makeShutdownHandler(func() { close(canceled) }, discardLogger())

			res, err := handler(context.Background(), json.RawMessage(tt.params))
			if !tt.wantCancel {
				if err == nil {
					t.Fatalf("expected refusal, got result %s", res)
				}
				if !strings.Contains(err.Error(), "restricted to the cli caller") {
					t.Fatalf("error = %v, want a caller-gate refusal", err)
				}
				select {
				case <-canceled:
					t.Fatal("serve context was canceled by a non-cli caller")
				case <-time.After(2 * shutdownGrace):
				}
				return
			}

			if err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			var payload struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(res, &payload); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			if payload.Status != "stopping" {
				t.Fatalf("status = %q, want %q", payload.Status, "stopping")
			}
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("serve context was not canceled")
			}
		})
	}
}

func TestShutdownHandlerRejectsMalformedParams(t *testing.T) {
	handler := makeShutdownHandler(func() { t.Fatal("canceled on malformed params") }, discardLogger())
	if _, err := handler(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Fatal("expected a parse error")
	}
}

// TestShutdownOverSocket exercises the whole lifecycle path end to end over a
// real unix socket: Serve, the client's Shutdown wrapper, the graceful drain,
// and daemon.WaitGone observing the socket go quiet.
func TestShutdownOverSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "qid.sock")
	ln, err := daemon.Listen(sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := daemon.NewServer(tools.NewRegistry(), policy.DefaultDecider{}, nil, discardLogger())
	server.Register("daemon.shutdown", makeShutdownHandler(cancel, discardLogger()))

	server.Register("status", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(daemonclient.DaemonStatus{
			Watcher:   daemonclient.WatcherStatus{State: "disabled"},
			Exe:       "/tmp/qid",
			Pid:       os.Getpid(),
			StartedAt: time.Now(),
		})
	})

	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, ln) }()

	st, err := daemonclient.WaitReady(ctx, sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("wait ready: %v", err)
	}
	if st.Pid != os.Getpid() {
		t.Fatalf("status pid = %d, want %d", st.Pid, os.Getpid())
	}

	cl, err := daemonclient.Dial(sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()

	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()
	if err := cl.Shutdown(callCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}
	if !daemon.WaitGone(sockPath, 2*time.Second) {
		t.Fatal("socket still answering after shutdown")
	}
	// Closing the listener is the whole teardown: net.Listen("unix", …) unlinks
	// on Close. Nothing in qid's exit path may remove the socket path a second
	// time — by then a successor daemon may have bound it (issue: restart
	// unlinking its own successor's socket). This test must not remove the file
	// itself; that the file is already gone IS the invariant.
	if _, err := os.Stat(sockPath); err == nil {
		t.Fatal("socket file still present after Serve returned; Close should have unlinked it")
	}
}

// TestShutdownRejectedOverSocket confirms the gate holds across the wire, not
// just at the handler: a non-cli caller gets an error and the daemon keeps
// serving.
func TestShutdownRejectedOverSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "qid.sock")
	ln, err := daemon.Listen(sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := daemon.NewServer(tools.NewRegistry(), policy.DefaultDecider{}, nil, discardLogger())
	server.Register("daemon.shutdown", makeShutdownHandler(
		func() { t.Error("non-cli caller canceled the serve context") }, discardLogger()))
	go func() { _ = server.Serve(ctx, ln) }()

	if _, err := daemonclient.WaitReady(ctx, sockPath, 2*time.Second); err != nil {
		t.Fatalf("wait ready: %v", err)
	}

	cl, err := daemonclient.Dial(sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()

	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()
	_, err = cl.Call(callCtx, "daemon.shutdown", struct {
		Caller string `json:"caller"`
	}{Caller: "mcp:test"})
	if err == nil {
		t.Fatal("expected the daemon to refuse an mcp caller")
	}
	if !strings.Contains(err.Error(), "restricted to the cli caller") {
		t.Fatalf("error = %v, want a caller-gate refusal", err)
	}
	if !daemon.Alive(sockPath) {
		t.Fatal("daemon stopped serving after a refused shutdown")
	}
}
