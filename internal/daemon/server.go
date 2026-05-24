package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"qi/internal/approval"
	"qi/internal/policy"
	"qi/internal/tools"
)

// Method handles a single JSON-RPC method call.
type Method func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

// Server speaks JSON-RPC 2.0 over any net.Conn. One Server instance can serve
// many concurrent connections; methods registered on it are shared across all
// of them.
type Server struct {
	registry *tools.Registry
	policy   policy.Decider
	queue    *approval.Queue
	methods  map[string]Method
	log      *slog.Logger
}

// NewServer builds a Server bound to the given tool registry, policy decider,
// and approval queue. The default method set is wired: tools.list, tools.call,
// approval.list, approval.get, approval.approve, approval.deny.
func NewServer(r *tools.Registry, decider policy.Decider, queue *approval.Queue, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if decider == nil {
		decider = policy.DefaultDecider{}
	}
	s := &Server{
		registry: r,
		policy:   decider,
		queue:    queue,
		methods:  make(map[string]Method),
		log:      log,
	}
	s.methods["tools.list"] = s.toolsList
	s.methods["tools.call"] = s.toolsCall
	if queue != nil {
		s.methods["approval.list"] = s.approvalList
		s.methods["approval.get"] = s.approvalGet
		s.methods["approval.approve"] = s.approvalApprove
		s.methods["approval.deny"] = s.approvalDeny
	}
	return s
}

// Register adds or replaces a method handler. Useful for tests and for layers
// (policy, approval) that will bolt their own RPCs onto the same server.
func (s *Server) Register(method string, h Method) {
	s.methods[method] = h
}

// Serve accepts connections on the listener until ctx is canceled or the
// listener returns an error. Each connection is handled in its own goroutine.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.ServeConn(ctx, conn)
		}()
	}
}

// ServeConn reads newline-delimited JSON-RPC messages from conn until EOF or
// ctx is canceled, dispatching each request and writing the response back.
// Responses on the same connection are serialized via writeMu. The conn is
// closed when ctx is canceled so the read loop unblocks promptly.
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopWatch:
		}
	}()
	defer close(stopWatch)

	var writeMu sync.Mutex
	br := bufio.NewReader(conn)

	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			s.handleLine(ctx, conn, &writeMu, line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.log.Debug("conn read", "err", err)
			}
			return
		}
	}
}

func (s *Server) handleLine(ctx context.Context, w io.Writer, mu *sync.Mutex, line []byte) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(w, mu, errorResponse(nil, CodeParseError, "parse error: "+err.Error()))
		return
	}
	if req.JSONRPC != JSONRPCVersion {
		if !req.IsNotification() {
			writeResponse(w, mu, errorResponse(req.ID, CodeInvalidRequest, "jsonrpc must be \"2.0\""))
		}
		return
	}
	if req.Method == "" {
		if !req.IsNotification() {
			writeResponse(w, mu, errorResponse(req.ID, CodeInvalidRequest, "method is required"))
		}
		return
	}

	handler, ok := s.methods[req.Method]
	if !ok {
		if !req.IsNotification() {
			writeResponse(w, mu, errorResponse(req.ID, CodeMethodNotFound, "method not found: "+req.Method))
		}
		return
	}

	result, err := handler(ctx, req.Params)
	if req.IsNotification() {
		return
	}
	if err != nil {
		writeResponse(w, mu, errorFromMethod(req.ID, err))
		return
	}
	if result == nil {
		result = json.RawMessage("null")
	}
	writeResponse(w, mu, successResponse(req.ID, result))
}

func errorFromMethod(id json.RawMessage, err error) Response {
	switch {
	case errors.Is(err, tools.ErrNotFound):
		return errorResponse(id, CodeToolNotFound, err.Error())
	case errors.Is(err, tools.ErrNotImplemented):
		return errorResponse(id, CodeToolNotImplemented, err.Error())
	case errors.Is(err, approval.ErrUnknownID):
		return errorResponse(id, CodeApprovalUnknown, err.Error())
	}
	var pde *policyDeniedError
	if errors.As(err, &pde) {
		return errorResponse(id, CodePolicyDenied, pde.Error())
	}
	var ie *invalidParamsError
	if errors.As(err, &ie) {
		return errorResponse(id, CodeInvalidParams, ie.Error())
	}
	return errorResponse(id, CodeHandlerError, err.Error())
}

func writeResponse(w io.Writer, mu *sync.Mutex, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	_, _ = w.Write(append(data, '\n'))
}

// invalidParamsError tags errors that should map to JSON-RPC -32602.
type invalidParamsError struct{ err error }

func (e *invalidParamsError) Error() string { return e.err.Error() }
func (e *invalidParamsError) Unwrap() error { return e.err }

func badParams(format string, args ...any) error {
	return &invalidParamsError{err: fmt.Errorf(format, args...)}
}

// policyDeniedError is returned when policy refuses a tool call. Maps to
// JSON-RPC -32004.
type policyDeniedError struct{ reason string }

func (e *policyDeniedError) Error() string { return "policy denied: " + e.reason }

// --- built-in methods ---

func (s *Server) toolsList(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	list := s.registry.List()
	return json.Marshal(struct {
		Tools []tools.Tool `json:"tools"`
	}{Tools: list})
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Caller    string          `json:"caller"`
}

type pendingResult struct {
	Status      string `json:"status"`
	ApprovalID  string `json:"approval_id"`
	Reason      string `json:"reason,omitempty"`
}

func (s *Server) toolsCall(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p toolsCallParams
	if len(raw) == 0 {
		return nil, badParams("missing params")
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, badParams("parse params: %w", err)
	}
	if p.Name == "" {
		return nil, badParams("name is required")
	}

	tool, ok := s.registry.Lookup(p.Name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", tools.ErrNotFound, p.Name)
	}

	verdict := s.policy.Decide(ctx, policy.Request{
		Caller: p.Caller,
		Tool:   tool,
		Params: p.Arguments,
	})

	switch verdict.Decision {
	case policy.Allow:
		return tools.Execute(ctx, s.registry, p.Name, p.Arguments)
	case policy.Deny:
		return nil, &policyDeniedError{reason: verdict.Reason}
	case policy.Confirm:
		if s.queue == nil {
			return nil, &policyDeniedError{reason: "approval queue is not available"}
		}
		id, err := s.queue.Enqueue(p.Caller, p.Name, p.Arguments, verdict.Reason)
		if err != nil {
			return nil, fmt.Errorf("enqueue: %w", err)
		}
		return json.Marshal(pendingResult{
			Status:     "pending",
			ApprovalID: id,
			Reason:     verdict.Reason,
		})
	default:
		return nil, fmt.Errorf("policy returned unknown decision %v", verdict.Decision)
	}
}

func (s *Server) approvalList(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Status string `json:"status"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, badParams("parse params: %w", err)
		}
	}
	entries := s.queue.List(approval.Status(p.Status))
	return json.Marshal(struct {
		Approvals []approval.Pending `json:"approvals"`
	}{Approvals: entries})
}

func (s *Server) approvalGet(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	id, err := parseIDParam(raw)
	if err != nil {
		return nil, err
	}
	p, ok := s.queue.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", approval.ErrUnknownID, id)
	}
	return json.Marshal(p)
}

func (s *Server) approvalApprove(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	id, err := parseIDParam(raw)
	if err != nil {
		return nil, err
	}
	p, err := s.queue.Approve(id)
	if err != nil {
		return nil, err
	}
	result, execErr := tools.Execute(ctx, s.registry, p.ToolName, p.Params)
	final, recordErr := s.queue.RecordResult(id, result, execErr)
	if recordErr != nil {
		s.log.Error("approval record result", "id", id, "err", recordErr)
	}
	if execErr != nil {
		return nil, execErr
	}
	return json.Marshal(final)
}

func (s *Server) approvalDeny(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, badParams("parse params: %w", err)
	}
	if p.ID == "" {
		return nil, badParams("id is required")
	}
	out, err := s.queue.Deny(p.ID, p.Reason)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func parseIDParam(raw json.RawMessage) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", badParams("parse params: %w", err)
	}
	if p.ID == "" {
		return "", badParams("id is required")
	}
	return p.ID, nil
}
