package approval

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Status is where a queue entry sits in its lifecycle.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusDenied   Status = "denied"
	StatusExecuted Status = "executed"
	StatusFailed   Status = "failed"
)

// Pending is the in-memory record for one queued call.
type Pending struct {
	ID         string          `json:"id"`
	Caller     string          `json:"caller"`
	CallID     string          `json:"call_id,omitempty"`
	ToolName   string          `json:"tool"`
	Params     json.RawMessage `json:"params,omitempty"`
	Status     Status          `json:"status"`
	Reason     string          `json:"reason,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	DecidedAt  *time.Time      `json:"decided_at,omitempty"`
	ExecutedAt *time.Time      `json:"executed_at,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Err        string          `json:"err,omitempty"`
}

// ErrUnknownID is returned when an operation targets an unknown approval id.
var ErrUnknownID = errors.New("approval id not found")

// ErrIllegalTransition is returned when a state change is not valid for the
// entry's current status.
var ErrIllegalTransition = errors.New("illegal approval state transition")

var errTerminalResultNotDurable = errors.New("tool may have executed; terminal result was not durably recorded")

// Queue holds pending approval entries in memory and emits an audit record
// on every transition.
type Queue struct {
	mu      sync.Mutex
	items   map[string]*Pending
	history map[string]*Pending
	audit   *Audit
	now     func() time.Time
}

// NewQueue builds a queue that writes audit records to a. Audit may be nil
// in tests.
func NewQueue(a *Audit) *Queue {
	return &Queue{
		items:   make(map[string]*Pending),
		history: make(map[string]*Pending),
		audit:   a,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Enqueue adds a pending entry and returns its id. The audit log gets an
// "enqueue" event.
func (q *Queue) Enqueue(caller, toolName string, params json.RawMessage, reason string) (string, error) {
	return q.EnqueueWithCallID(caller, "", toolName, params, reason)
}

// EnqueueWithCallID adds a pending entry bound to an optional immutable
// planner tool-call ID.
func (q *Queue) EnqueueWithCallID(caller, callID, toolName string, params json.RawMessage, reason string) (string, error) {
	if caller == "" || toolName == "" {
		return "", fmt.Errorf("enqueue: caller and tool name are required")
	}

	q.mu.Lock()
	id := newID()
	for {
		_, live := q.items[id]
		_, historical := q.history[id]
		if !live && !historical {
			break
		}
		id = newID()
	}
	p := &Pending{
		ID:        id,
		Caller:    caller,
		CallID:    callID,
		ToolName:  toolName,
		Params:    cloneRaw(params),
		Status:    StatusPending,
		Reason:    reason,
		CreatedAt: q.now(),
	}
	q.items[id] = p
	err := q.appendAudit(AuditEntry{
		Time:   p.CreatedAt,
		Event:  EventEnqueue,
		ID:     id,
		Caller: caller,
		CallID: callID,
		Tool:   toolName,
		Reason: reason,
		Params: cloneRaw(params),
	})
	if err != nil {
		delete(q.items, id)
		q.mu.Unlock()
		return "", fmt.Errorf("enqueue audit: %w", err)
	}
	q.mu.Unlock()
	return id, nil
}

// Approve marks the entry approved. Caller is expected to execute the tool
// afterwards and call RecordResult.
func (q *Queue) Approve(id string) (Pending, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, err := q.pendingForTransition(id, StatusApproved)
	if err != nil {
		return Pending{}, err
	}
	t := q.now()
	if err := q.appendAudit(AuditEntry{Time: t, Event: EventApprove, ID: id, Caller: p.Caller, CallID: p.CallID, Tool: p.ToolName}); err != nil {
		return Pending{}, fmt.Errorf("approve audit: %w", err)
	}
	p.Status = StatusApproved
	p.DecidedAt = &t
	return clonePending(p), nil
}

// Deny refuses the entry. Reason is recorded in audit.
func (q *Queue) Deny(id, reason string) (Pending, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, err := q.pendingForTransition(id, StatusDenied)
	if err != nil {
		return Pending{}, err
	}
	t := q.now()
	outcome := &TerminalOutcome{Status: StatusDenied, Reason: reason, DecidedAt: &t}
	if err := q.appendAudit(AuditEntry{Time: t, Event: EventDeny, ID: id, Caller: p.Caller, CallID: p.CallID, Tool: p.ToolName, Reason: reason, Params: cloneRaw(p.Params), Outcome: outcome}); err != nil {
		return Pending{}, fmt.Errorf("deny audit: %w", err)
	}
	p.Status = StatusDenied
	p.Reason = reason
	p.DecidedAt = &t
	return clonePending(p), nil
}

// RecordResult stores the tool's output after execution. If execErr is
// non-nil, the entry is marked failed and the error string is captured.
func (q *Queue) RecordResult(id string, result json.RawMessage, execErr error) (Pending, error) {
	if execErr == nil && len(result) > MaxTerminalResultBytes {
		return Pending{}, fmt.Errorf("terminal result exceeds %d bytes", MaxTerminalResultBytes)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	p, ok := q.items[id]
	if !ok {
		if historical, exists := q.history[id]; exists {
			return Pending{}, fmt.Errorf("%w: cannot record result on %s entry", ErrIllegalTransition, historical.Status)
		}
		return Pending{}, fmt.Errorf("%w: %s", ErrUnknownID, id)
	}
	if p.Status != StatusApproved {
		return Pending{}, fmt.Errorf("%w: cannot record result on %s entry", ErrIllegalTransition, p.Status)
	}
	t := q.now()
	snap := clonePending(p)
	snap.ExecutedAt = &t
	event := EventExecute
	if execErr != nil {
		snap.Status = StatusFailed
		snap.Err = execErr.Error()
		event = EventFail
	} else {
		snap.Status = StatusExecuted
		snap.Result = cloneRaw(result)
	}
	outcome := &TerminalOutcome{Status: snap.Status, Result: cloneRaw(snap.Result), Err: snap.Err, DecidedAt: snap.DecidedAt, ExecutedAt: snap.ExecutedAt}
	if err := q.appendAudit(AuditEntry{Time: t, Event: event, ID: id, Caller: snap.Caller, CallID: snap.CallID, Tool: snap.ToolName, Err: snap.Err, Params: cloneRaw(snap.Params), Outcome: outcome}); err != nil {
		return Pending{}, fmt.Errorf("%w: %v", errTerminalResultNotDurable, err)
	}
	*p = snap
	return clonePending(p), nil
}

func (q *Queue) pendingForTransition(id string, next Status) (*Pending, error) {
	p, ok := q.items[id]
	if !ok {
		if historical, exists := q.history[id]; exists {
			return nil, fmt.Errorf("%w: %s → %s", ErrIllegalTransition, historical.Status, next)
		}
		return nil, fmt.Errorf("%w: %s", ErrUnknownID, id)
	}
	if p.Status != StatusPending {
		return nil, fmt.Errorf("%w: %s → %s", ErrIllegalTransition, p.Status, next)
	}
	return p, nil
}

// Get returns a snapshot of the entry with the given id.
func (q *Queue) Get(id string) (Pending, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, ok := q.items[id]
	if ok {
		return clonePending(p), true
	}
	p, ok = q.history[id]
	if !ok {
		return Pending{}, false
	}
	return clonePending(p), true
}

// List returns a snapshot of every entry, sorted by creation time
// (oldest first). Pass filter == "" to include all statuses.
func (q *Queue) List(filter Status) []Pending {
	q.mu.Lock()
	out := make([]Pending, 0, len(q.items))
	for _, p := range q.items {
		if filter != "" && p.Status != filter {
			continue
		}
		out = append(out, clonePending(p))
	}
	q.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func clonePending(p *Pending) Pending {
	copy := *p
	copy.Params = cloneRaw(p.Params)
	copy.Result = cloneRaw(p.Result)
	copy.DecidedAt = cloneTime(p.DecidedAt)
	copy.ExecutedAt = cloneTime(p.ExecutedAt)
	return copy
}

func (q *Queue) appendAudit(e AuditEntry) error {
	if q.audit == nil {
		return nil
	}
	return q.audit.Append(e)
}
