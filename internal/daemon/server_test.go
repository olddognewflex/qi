package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"qi/internal/approval"
	"qi/internal/policy"
	"qi/internal/tools"
	"qi/internal/tools/builtin"
)

// session drives a Server over an in-memory pipe so tests don't need a real
// unix socket. Each call to Send writes one JSON-RPC line; ReadResp reads one
// line back.
type session struct {
	t      *testing.T
	server *Server
	client net.Conn
	r      *bufio.Reader
	done   chan struct{}
}

func newSession(t *testing.T, r *tools.Registry) *session {
	t.Helper()
	return newSessionWith(t, r, nil)
}

func newSessionWith(t *testing.T, r *tools.Registry, q *approval.Queue) *session {
	t.Helper()
	server := NewServer(r, policy.DefaultDecider{}, q, nil)
	cli, srv := net.Pipe()
	done := make(chan struct{})
	go func() {
		server.ServeConn(context.Background(), srv)
		close(done)
	}()
	s := &session{
		t:      t,
		server: server,
		client: cli,
		r:      bufio.NewReader(cli),
		done:   done,
	}
	t.Cleanup(func() {
		_ = cli.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("ServeConn did not return after client close")
		}
	})
	return s
}

func (s *session) send(line string) {
	s.t.Helper()
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	if _, err := s.client.Write([]byte(line)); err != nil {
		s.t.Fatalf("send: %v", err)
	}
}

func (s *session) readResp() Response {
	s.t.Helper()
	_ = s.client.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = s.client.SetReadDeadline(time.Time{}) }()
	line, err := s.r.ReadBytes('\n')
	if err != nil && err != io.EOF {
		s.t.Fatalf("readResp: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		s.t.Fatalf("decode resp: %v (line=%q)", err, line)
	}
	return resp
}

// expectNoResp asserts that the server does not write a response within a
// short window. Used for notifications.
func (s *session) expectNoResp() {
	s.t.Helper()
	_ = s.client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	defer func() { _ = s.client.SetReadDeadline(time.Time{}) }()
	if _, err := s.r.ReadBytes('\n'); err == nil {
		s.t.Fatal("expected no response, got one")
	}
}

func TestToolsListEmpty(t *testing.T) {
	s := newSession(t, tools.NewRegistry())
	s.send(`{"jsonrpc":"2.0","method":"tools.list","id":1}`)
	resp := s.readResp()
	if resp.Error != nil {
		t.Fatalf("error: %+v", resp.Error)
	}
	var got struct {
		Tools []tools.Tool `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 0 {
		t.Fatalf("expected empty list, got %d", len(got.Tools))
	}
}

func TestToolsListReturnsRegistered(t *testing.T) {
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	s := newSession(t, r)
	s.send(`{"jsonrpc":"2.0","method":"tools.list","id":1}`)
	resp := s.readResp()
	if resp.Error != nil {
		t.Fatalf("error: %+v", resp.Error)
	}
	var got struct {
		Tools []tools.Tool `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != builtin.CaptureToolName {
		t.Fatalf("got %+v", got.Tools)
	}
	if got.Tools[0].Source.Kind != tools.SourceLocal {
		t.Fatalf("source kind = %v, want local", got.Tools[0].Source.Kind)
	}
}

func TestToolsCallDispatchesCapture(t *testing.T) {
	inbox := t.TempDir()
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, inbox); err != nil {
		t.Fatalf("register: %v", err)
	}
	s := newSession(t, r)
	s.send(`{"jsonrpc":"2.0","method":"tools.call","id":2,"params":{"name":"vault.capture","arguments":{"text":"hello"},"caller":"cli"}}`)
	resp := s.readResp()
	if resp.Error != nil {
		t.Fatalf("error: %+v", resp.Error)
	}
	var res struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(res.Path) != inbox {
		t.Fatalf("path %q not under inbox %q", res.Path, inbox)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("captured file missing: %v", err)
	}
}

func TestParseError(t *testing.T) {
	s := newSession(t, tools.NewRegistry())
	s.send(`{not-json`)
	resp := s.readResp()
	if resp.Error == nil || resp.Error.Code != CodeParseError {
		t.Fatalf("err = %+v, want parse error", resp.Error)
	}
}

func TestInvalidJSONRPCVersion(t *testing.T) {
	s := newSession(t, tools.NewRegistry())
	s.send(`{"jsonrpc":"1.0","method":"tools.list","id":1}`)
	resp := s.readResp()
	if resp.Error == nil || resp.Error.Code != CodeInvalidRequest {
		t.Fatalf("err = %+v, want invalid request", resp.Error)
	}
}

func TestMethodNotFound(t *testing.T) {
	s := newSession(t, tools.NewRegistry())
	s.send(`{"jsonrpc":"2.0","method":"ghost","id":1}`)
	resp := s.readResp()
	if resp.Error == nil || resp.Error.Code != CodeMethodNotFound {
		t.Fatalf("err = %+v, want method not found", resp.Error)
	}
}

func TestToolNotFound(t *testing.T) {
	s := newSession(t, tools.NewRegistry())
	s.send(`{"jsonrpc":"2.0","method":"tools.call","id":1,"params":{"name":"ghost","arguments":{}}}`)
	resp := s.readResp()
	if resp.Error == nil || resp.Error.Code != CodeToolNotFound {
		t.Fatalf("err = %+v, want tool not found", resp.Error)
	}
}

func TestToolsCallMissingName(t *testing.T) {
	s := newSession(t, tools.NewRegistry())
	s.send(`{"jsonrpc":"2.0","method":"tools.call","id":1,"params":{"arguments":{}}}`)
	resp := s.readResp()
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("err = %+v, want invalid params", resp.Error)
	}
}

func TestNotificationGetsNoResponse(t *testing.T) {
	s := newSession(t, tools.NewRegistry())
	s.send(`{"jsonrpc":"2.0","method":"tools.list"}`)
	s.expectNoResp()
}

func TestServeListenerLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qid.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	server := NewServer(r, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = server.Serve(ctx, ln)
	}()

	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","method":"tools.list","id":1}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	line, err := br.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("err: %+v", resp.Error)
	}

	cancel()
	wg.Wait()
}

func TestPolicyDeniesEmptyCaller(t *testing.T) {
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	s := newSession(t, r)
	s.send(`{"jsonrpc":"2.0","method":"tools.call","id":1,"params":{"name":"vault.capture","arguments":{"text":"x"}}}`)
	resp := s.readResp()
	if resp.Error == nil || resp.Error.Code != CodePolicyDenied {
		t.Fatalf("err = %+v, want CodePolicyDenied", resp.Error)
	}
}

func TestApprovalFlow(t *testing.T) {
	inbox := t.TempDir()
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, inbox); err != nil {
		t.Fatalf("register: %v", err)
	}
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	audit, err := approval.OpenAudit(auditPath)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer audit.Close()
	q := approval.NewQueue(audit)
	s := newSessionWith(t, r, q)

	// Non-cli mutating call → pending
	s.send(`{"jsonrpc":"2.0","method":"tools.call","id":1,"params":{"name":"vault.capture","arguments":{"text":"queued"},"caller":"ai"}}`)
	pendResp := s.readResp()
	if pendResp.Error != nil {
		t.Fatalf("err = %+v", pendResp.Error)
	}
	var pending struct {
		Status     string `json:"status"`
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal(pendResp.Result, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" || pending.ApprovalID == "" {
		t.Fatalf("pending = %+v", pending)
	}

	// Approve runs the tool
	s.send(`{"jsonrpc":"2.0","method":"approval.approve","id":2,"params":{"id":"` + pending.ApprovalID + `"}}`)
	approveResp := s.readResp()
	if approveResp.Error != nil {
		t.Fatalf("approve err = %+v", approveResp.Error)
	}
	var final approval.Pending
	if err := json.Unmarshal(approveResp.Result, &final); err != nil {
		t.Fatal(err)
	}
	if final.Status != approval.StatusExecuted {
		t.Fatalf("status = %s", final.Status)
	}
	var inner struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(final.Result, &inner); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(inner.Path) != inbox {
		t.Fatalf("path %q not under %q", inner.Path, inbox)
	}
	if _, err := os.Stat(inner.Path); err != nil {
		t.Fatalf("captured file missing: %v", err)
	}
}

func TestApprovalDenyPath(t *testing.T) {
	r := tools.NewRegistry()
	if err := builtin.RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	q := approval.NewQueue(nil)
	s := newSessionWith(t, r, q)

	s.send(`{"jsonrpc":"2.0","method":"tools.call","id":1,"params":{"name":"vault.capture","arguments":{"text":"x"},"caller":"ai"}}`)
	resp := s.readResp()
	var pending struct {
		ApprovalID string `json:"approval_id"`
	}
	_ = json.Unmarshal(resp.Result, &pending)

	s.send(`{"jsonrpc":"2.0","method":"approval.deny","id":2,"params":{"id":"` + pending.ApprovalID + `","reason":"nope"}}`)
	deny := s.readResp()
	if deny.Error != nil {
		t.Fatalf("err = %+v", deny.Error)
	}
	var p approval.Pending
	if err := json.Unmarshal(deny.Result, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != approval.StatusDenied {
		t.Fatalf("status = %s, want denied", p.Status)
	}
	if p.Reason != "nope" {
		t.Fatalf("reason = %q", p.Reason)
	}
}

func TestApprovalUnknownID(t *testing.T) {
	q := approval.NewQueue(nil)
	s := newSessionWith(t, tools.NewRegistry(), q)
	s.send(`{"jsonrpc":"2.0","method":"approval.approve","id":1,"params":{"id":"ghost"}}`)
	resp := s.readResp()
	if resp.Error == nil || resp.Error.Code != CodeApprovalUnknown {
		t.Fatalf("err = %+v, want CodeApprovalUnknown", resp.Error)
	}
}

func TestApprovalGetAfterRestore(t *testing.T) {
	// Given: an executed approval exists only in a durable audit log.
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	audit, err := approval.OpenAudit(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	source := approval.NewQueue(audit)
	id, err := source.EnqueueWithCallID("ai-planner:s1", "call-1", "mutate", json.RawMessage(`{"x":1}`), "confirm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Approve(id); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RecordResult(id, json.RawMessage(`{"ok":true}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := audit.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := approval.ReadAuditLog(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	restored := approval.NewQueue(nil)
	restored.Restore(entries)
	s := newSessionWith(t, tools.NewRegistry(), restored)

	// When: a client fetches the terminal approval after restart.
	s.send(`{"jsonrpc":"2.0","method":"approval.get","id":1,"params":{"id":"` + id + `"}}`)
	resp := s.readResp()

	// Then: the persisted result and planner correlation are returned.
	if resp.Error != nil {
		t.Fatalf("approval.get error = %+v", resp.Error)
	}
	var got approval.Pending
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != approval.StatusExecuted || got.CallID != "call-1" || string(got.Result) != `{"ok":true}` {
		t.Fatalf("approval = %+v", got)
	}
}

func TestRestoredTerminalCannotExecuteAgain(t *testing.T) {
	// Given: an executed approval is restored into lookup-only history.
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	audit, err := approval.OpenAudit(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	source := approval.NewQueue(audit)
	id, err := source.Enqueue("ai-planner:s1", "mutate", nil, "confirm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Approve(id); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RecordResult(id, json.RawMessage(`{"ok":true}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := audit.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := approval.ReadAuditLog(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	restored := approval.NewQueue(nil)
	restored.Restore(entries)
	executions := 0
	r := tools.NewRegistry()
	if err := r.RegisterLocal(tools.Tool{Name: "mutate", Mutating: true}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions++
		return json.RawMessage(`{"ok":true}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	s := newSessionWith(t, r, restored)

	// When: the restored terminal approval is approved again.
	s.send(`{"jsonrpc":"2.0","method":"approval.approve","id":1,"params":{"id":"` + id + `"}}`)
	resp := s.readResp()

	// Then: the transition is rejected before registry execution.
	if resp.Error == nil || !strings.Contains(resp.Error.Message, approval.ErrIllegalTransition.Error()) {
		t.Fatalf("response error = %+v", resp.Error)
	}
	if executions != 0 {
		t.Fatalf("executions = %d, want 0", executions)
	}
}

func TestApprovalApproveSurfacesRecordResultAuditFailure(t *testing.T) {
	// Given: the audit closes during execution, after approval was persisted.
	audit, err := approval.OpenAudit(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	q := approval.NewQueue(audit)
	r := tools.NewRegistry()
	if err := r.RegisterLocal(tools.Tool{Name: "mutate", Mutating: true}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		if err := audit.Close(); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"ok":true}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	s := newSessionWith(t, r, q)
	s.send(`{"jsonrpc":"2.0","method":"tools.call","id":1,"params":{"name":"mutate","arguments":{},"caller":"ai"}}`)
	pendingResp := s.readResp()
	var pending struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal(pendingResp.Result, &pending); err != nil {
		t.Fatal(err)
	}

	// When: the user approves and execution succeeds but terminal persistence fails.
	s.send(`{"jsonrpc":"2.0","method":"approval.approve","id":2,"params":{"id":"` + pending.ApprovalID + `"}}`)
	resp := s.readResp()

	// Then: JSON-RPC reports the durability ambiguity instead of success.
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "tool may have executed; terminal result was not durably recorded") {
		t.Fatalf("response error = %+v", resp.Error)
	}
}

func TestApprovalMethodsAbsentWithoutQueue(t *testing.T) {
	s := newSession(t, tools.NewRegistry()) // queue=nil
	s.send(`{"jsonrpc":"2.0","method":"approval.list","id":1,"params":{}}`)
	resp := s.readResp()
	if resp.Error == nil || resp.Error.Code != CodeMethodNotFound {
		t.Fatalf("err = %+v, want CodeMethodNotFound", resp.Error)
	}
}

func TestListenRejectsRunningDaemon(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qid.sock")
	ln1, err := Listen(path)
	if err != nil {
		t.Fatalf("listen 1: %v", err)
	}
	defer ln1.Close()
	go func() {
		for {
			c, err := ln1.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if _, err := Listen(path); err == nil {
		t.Fatal("expected error when daemon already running")
	}
}

func TestListenRemovesStaleSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qid.sock")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
}
