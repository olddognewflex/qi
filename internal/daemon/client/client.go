// Package client is the qi-side JSON-RPC 2.0 client for qid. It owns a single
// net.Conn (typically a unix-domain socket), serializes Call requests, and
// maps wire error codes back into typed Go errors so callers can match with
// errors.Is.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"qi/internal/approval"
	"qi/internal/daemon"
	"qi/internal/tools"
)

// ErrDaemonUnavailable is returned when the qid socket cannot be reached.
var ErrDaemonUnavailable = errors.New("qid daemon unavailable")

// CallError wraps a JSON-RPC error response that did not match a typed
// sentinel. Code holds the JSON-RPC error code.
type CallError struct {
	Code    int
	Message string
}

func (e *CallError) Error() string {
	return fmt.Sprintf("qid rpc error %d: %s", e.Code, e.Message)
}

// Client is a single-conn JSON-RPC client. Safe for concurrent use; Call is
// serialized via an internal mutex so request/response pairing is correct
// without ID routing.
type Client struct {
	conn   net.Conn
	br     *bufio.Reader
	mu     sync.Mutex
	nextID atomic.Uint64
}

// Dial connects to the qid socket. timeout bounds the initial connection
// only; per-call deadlines are set via the context passed to Call.
func Dial(socketPath string, timeout time.Duration) (*Client, error) {
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDaemonUnavailable, err)
	}
	return NewFromConn(conn), nil
}

// NewFromConn wraps an already-open net.Conn in a Client. Useful for tests
// that drive the daemon through net.Pipe and for higher-level bridges
// (qi-mcp) that share a daemon connection without going through unix
// sockets.
func NewFromConn(conn net.Conn) *Client {
	return &Client{conn: conn, br: bufio.NewReader(conn)}
}

// Close shuts down the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Call invokes a JSON-RPC method and returns its raw result. If ctx has a
// deadline, it is applied to the conn's read/write deadlines for the
// duration of the call. Wire errors map to ErrNotFound / ErrNotImplemented
// when possible, otherwise *CallError.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var paramsRaw json.RawMessage
	if params != nil {
		switch p := params.(type) {
		case json.RawMessage:
			paramsRaw = p
		case []byte:
			paramsRaw = p
		default:
			b, err := json.Marshal(params)
			if err != nil {
				return nil, fmt.Errorf("marshal params: %w", err)
			}
			paramsRaw = b
		}
	}

	id := c.nextID.Add(1)
	idRaw := json.RawMessage(strconv.FormatUint(id, 10))
	req := daemon.Request{
		JSONRPC: daemon.JSONRPCVersion,
		Method:  method,
		Params:  paramsRaw,
		ID:      idRaw,
	}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
		defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	}

	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	respLine, err := c.br.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var resp daemon.Response
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if string(resp.ID) != string(idRaw) {
		return nil, fmt.Errorf("id mismatch: sent %s got %s", idRaw, resp.ID)
	}
	if resp.Error != nil {
		return nil, mapError(resp.Error)
	}
	return resp.Result, nil
}

// ErrPolicyDenied is returned when qid's policy refused a tool call.
var ErrPolicyDenied = errors.New("policy denied")

// ErrApprovalUnknown is returned when an approval id is unknown to qid.
var ErrApprovalUnknown = errors.New("approval id not found")

func mapError(e *daemon.RPCError) error {
	switch e.Code {
	case daemon.CodeToolNotFound:
		return fmt.Errorf("%w: %s", tools.ErrNotFound, trimPrefix(e.Message, tools.ErrNotFound.Error()))
	case daemon.CodeToolNotImplemented:
		return fmt.Errorf("%w: %s", tools.ErrNotImplemented, trimPrefix(e.Message, tools.ErrNotImplemented.Error()))
	case daemon.CodePolicyDenied:
		return fmt.Errorf("%w: %s", ErrPolicyDenied, trimPrefix(e.Message, "policy denied"))
	case daemon.CodeApprovalUnknown:
		return fmt.Errorf("%w: %s", ErrApprovalUnknown, e.Message)
	default:
		return &CallError{Code: e.Code, Message: e.Message}
	}
}

// trimPrefix strips an "<prefix>: " segment from msg if present. Used to
// avoid double-labeling when the wire message already starts with the
// sentinel's own text.
func trimPrefix(msg, prefix string) string {
	return strings.TrimSpace(strings.TrimPrefix(msg, prefix+":"))
}

// ListTools is a typed wrapper around the tools.list method.
func (c *Client) ListTools(ctx context.Context) ([]tools.Tool, error) {
	raw, err := c.Call(ctx, "tools.list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tools []tools.Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode tools.list: %w", err)
	}
	return resp.Tools, nil
}

// CallerCLI is the default caller identity sent by this client. It maps to
// policy.CallerCLI on the server side and bypasses the approval queue.
const CallerCLI = "cli"

// CallTool is shorthand for CallToolAs(ctx, CallerCLI, name, arguments).
// Use CallToolAs when invoking on behalf of a non-cli caller (qi-mcp, ai
// planner) so policy can route mutating calls through the approval queue.
func (c *Client) CallTool(ctx context.Context, name string, arguments any) (json.RawMessage, error) {
	return c.CallToolAs(ctx, CallerCLI, name, arguments)
}

// PendingResult is returned when qid's policy routes a mutating tool call
// through the approval queue instead of executing immediately. The qi CLI
// surfaces ApprovalID to the user so they can call ApproveAndRun later.
type PendingResult struct {
	Status     string `json:"status"`
	ApprovalID string `json:"approval_id"`
	Reason     string `json:"reason,omitempty"`
}

// IsPending reports whether a raw tool result is a pending-approval payload.
// Use this on results from CallToolAs to decide whether to surface an
// approval prompt or treat the response as the tool's actual output.
func IsPending(raw json.RawMessage) (PendingResult, bool) {
	var p PendingResult
	if err := json.Unmarshal(raw, &p); err != nil {
		return PendingResult{}, false
	}
	if p.Status == "pending" && p.ApprovalID != "" {
		return p, true
	}
	return PendingResult{}, false
}

// CallToolAs invokes a tool with an explicit caller identity. arguments is
// JSON-marshaled if non-nil.
func (c *Client) CallToolAs(ctx context.Context, caller, name string, arguments any) (json.RawMessage, error) {
	return c.callToolAs(ctx, caller, "", name, arguments)
}

// CallToolAsWithID invokes a tool with an explicit caller and immutable
// planner tool-call ID for approval correlation.
func (c *Client) CallToolAsWithID(ctx context.Context, caller, callID, name string, arguments any) (json.RawMessage, error) {
	return c.callToolAs(ctx, caller, callID, name, arguments)
}

func (c *Client) callToolAs(ctx context.Context, caller, callID, name string, arguments any) (json.RawMessage, error) {
	var argsRaw json.RawMessage
	if arguments != nil {
		switch a := arguments.(type) {
		case json.RawMessage:
			argsRaw = a
		case []byte:
			argsRaw = a
		default:
			b, err := json.Marshal(arguments)
			if err != nil {
				return nil, fmt.Errorf("marshal arguments: %w", err)
			}
			argsRaw = b
		}
	}
	return c.Call(ctx, "tools.call", struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
		Caller    string          `json:"caller"`
		CallID    string          `json:"call_id,omitempty"`
	}{Name: name, Arguments: argsRaw, Caller: caller, CallID: callID})
}

// ListApprovals returns queued approvals. Pass status="" for all entries.
func (c *Client) ListApprovals(ctx context.Context, status string) ([]approval.Pending, error) {
	raw, err := c.Call(ctx, "approval.list", struct {
		Status string `json:"status,omitempty"`
	}{Status: status})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Approvals []approval.Pending `json:"approvals"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode approval.list: %w", err)
	}
	return resp.Approvals, nil
}

// GetApproval fetches a single approval entry by id.
func (c *Client) GetApproval(ctx context.Context, id string) (approval.Pending, error) {
	raw, err := c.Call(ctx, "approval.get", struct {
		ID string `json:"id"`
	}{ID: id})
	if err != nil {
		return approval.Pending{}, err
	}
	var p approval.Pending
	if err := json.Unmarshal(raw, &p); err != nil {
		return approval.Pending{}, fmt.Errorf("decode approval.get: %w", err)
	}
	return p, nil
}

// ApproveAndRun marks the approval approved and executes the tool. On
// success, returns the final Pending entry containing the tool's Result.
func (c *Client) ApproveAndRun(ctx context.Context, id string) (approval.Pending, error) {
	raw, err := c.Call(ctx, "approval.approve", struct {
		ID string `json:"id"`
	}{ID: id})
	if err != nil {
		return approval.Pending{}, err
	}
	var p approval.Pending
	if err := json.Unmarshal(raw, &p); err != nil {
		return approval.Pending{}, fmt.Errorf("decode approval.approve: %w", err)
	}
	return p, nil
}

// DenyApproval refuses the approval. Reason is optional.
func (c *Client) DenyApproval(ctx context.Context, id, reason string) (approval.Pending, error) {
	raw, err := c.Call(ctx, "approval.deny", struct {
		ID     string `json:"id"`
		Reason string `json:"reason,omitempty"`
	}{ID: id, Reason: reason})
	if err != nil {
		return approval.Pending{}, err
	}
	var p approval.Pending
	if err := json.Unmarshal(raw, &p); err != nil {
		return approval.Pending{}, fmt.Errorf("decode approval.deny: %w", err)
	}
	return p, nil
}

// WatcherStatus is the watcher slice of the qid `status` RPC: which lifecycle
// state qid's vault watcher is in ("disabled", "starting", "blocked",
// "running", "failed" — watcher.State values) and a human-readable detail.
// "blocked" is the diagnostic `qi doctor` exists to catch: dir enumeration
// stuck (macOS TCC grant missing under launchd) while the socket still dials
// fine.
type WatcherStatus struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// DaemonStatus is the full `status` RPC payload. Shared by qid (marshal) and
// this client (unmarshal) so the wire shape cannot drift. Exe, Pid and
// StartedAt are absent from older daemons and decode to zero values — callers
// must render those as "unknown" rather than treating them as an error.
type DaemonStatus struct {
	Watcher   WatcherStatus `json:"watcher"`
	Exe       string        `json:"exe"`
	Pid       int           `json:"pid"`
	StartedAt time.Time     `json:"started_at"`
}

// Status is a typed wrapper around the `status` method. A CodeMethodNotFound
// error means the daemon predates the status RPC — callers should treat that
// as "unknown", not unhealthy.
func (c *Client) Status(ctx context.Context) (DaemonStatus, error) {
	raw, err := c.Call(ctx, "status", nil)
	if err != nil {
		return DaemonStatus{}, err
	}
	var s DaemonStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		return DaemonStatus{}, fmt.Errorf("decode status: %w", err)
	}
	return s, nil
}

// Shutdown asks qid to stop gracefully. It sends the cli caller identity: qid
// hard-denies the method for anyone else, so an AI planner or MCP session can
// never kill the daemon. A CodeMethodNotFound error means the daemon predates
// the RPC.
//
// The daemon cancels its serve context shortly after replying, which closes
// this connection. That write/close ordering is a race the server cannot fully
// close, so a truncated reply is treated as success here — the request was
// delivered and shutdown is what it asked for. Callers confirm the outcome by
// polling the socket (daemon.WaitGone), not by this reply.
func (c *Client) Shutdown(ctx context.Context) error {
	_, err := c.Call(ctx, "daemon.shutdown", struct {
		Caller string `json:"caller"`
	}{Caller: CallerCLI})
	if err != nil && isConnClosed(err) {
		return nil
	}
	return err
}

// isConnClosed reports whether err is the connection going away mid-call
// rather than a real protocol or handler failure.
func isConnClosed(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

// WaitReady polls the status RPC until the daemon answers or timeout elapses.
// Waiting for the socket file, or even for a dial to succeed, is not enough: a
// listener accepts from the kernel backlog before qid reaches Serve. Only an
// answered RPC proves readiness. A daemon that predates the status RPC answers
// method-not-found, which counts as ready and yields a zero DaemonStatus.
func WaitReady(ctx context.Context, socketPath string, timeout time.Duration) (DaemonStatus, error) {
	deadline := time.Now().Add(timeout)
	lastErr := errors.New("no probe completed")
	for {
		// Check the budget BEFORE probing, and cap each probe by what is left
		// of it: a dial plus a status call can cost over a second, so probing
		// first would overrun the caller's timeout by that much.
		if err := ctx.Err(); err != nil {
			return DaemonStatus{}, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return DaemonStatus{}, fmt.Errorf("qid not ready after %s: %w", timeout, lastErr)
		}

		st, err := probeStatus(ctx, socketPath, remaining)
		if err == nil {
			return st, nil
		}
		var ce *CallError
		if errors.As(err, &ce) && ce.Code == daemon.CodeMethodNotFound {
			return DaemonStatus{}, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return DaemonStatus{}, ctx.Err()
		case <-time.After(min(50*time.Millisecond, time.Until(deadline))):
		}
	}
}

func probeStatus(ctx context.Context, socketPath string, budget time.Duration) (DaemonStatus, error) {
	c, err := Dial(socketPath, min(200*time.Millisecond, budget))
	if err != nil {
		return DaemonStatus{}, err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, min(time.Second, budget))
	defer cancel()
	return c.Status(ctx)
}
