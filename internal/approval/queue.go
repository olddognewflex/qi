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

// Queue holds pending approval entries in memory and emits an audit record
// on every transition.
type Queue struct {
	mu    sync.Mutex
	items map[string]*Pending
	audit *Audit
	now   func() time.Time
}

// NewQueue builds a queue that writes audit records to a. Audit may be nil
// in tests.
func NewQueue(a *Audit) *Queue {
	return &Queue{
		items: make(map[string]*Pending),
		audit: a,
		now:   func() time.Time { return time.Now().UTC() },
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
	if caller == "" || toolName == "" {
		return "", fmt.Errorf("enqueue: caller and tool name are required")
	}

	q.mu.Lock()
	id := newID()
	for {
		if _, exists := q.items[id]; !exists {
			break
		}
		id = newID()
	}
	p := &Pending{
		ID:        id,
		Caller:    caller,
		ToolName:  toolName,
		Params:    params,
		Status:    StatusPending,
		Reason:    reason,
		CreatedAt: q.now(),
	}
	q.items[id] = p
	q.mu.Unlock()

	q.audited(AuditEntry{
		Event:  EventEnqueue,
		ID:     id,
		Caller: caller,
		Tool:   toolName,
		Reason: reason,
		Params: params,
	})
	return id, nil
}

// Approve marks the entry approved. Caller is expected to execute the tool
// afterwards and call RecordResult.
func (q *Queue) Approve(id string) (Pending, error) {
	p, err := q.transition(id, StatusApproved, "", "")
	if err != nil {
		return Pending{}, err
	}
	q.audited(AuditEntry{Event: EventApprove, ID: id, Caller: p.Caller, Tool: p.ToolName})
	return p, nil
}

// Deny refuses the entry. Reason is recorded in audit.
func (q *Queue) Deny(id, reason string) (Pending, error) {
	p, err := q.transition(id, StatusDenied, reason, "")
	if err != nil {
		return Pending{}, err
	}
	q.audited(AuditEntry{Event: EventDeny, ID: id, Caller: p.Caller, Tool: p.ToolName, Reason: reason})
	return p, nil
}

// RecordResult stores the tool's output after execution. If execErr is
// non-nil, the entry is marked failed and the error string is captured.
func (q *Queue) RecordResult(id string, result json.RawMessage, execErr error) (Pending, error) {
	q.mu.Lock()
	p, ok := q.items[id]
	if !ok {
		q.mu.Unlock()
		return Pending{}, fmt.Errorf("%w: %s", ErrUnknownID, id)
	}
	if p.Status != StatusApproved {
		q.mu.Unlock()
		return Pending{}, fmt.Errorf("%w: cannot record result on %s entry", ErrIllegalTransition, p.Status)
	}
	t := q.now()
	p.ExecutedAt = &t
	if execErr != nil {
		p.Status = StatusFailed
		p.Err = execErr.Error()
	} else {
		p.Status = StatusExecuted
		p.Result = result
	}
	snap := *p
	q.mu.Unlock()

	if execErr != nil {
		q.audited(AuditEntry{Event: EventFail, ID: id, Caller: snap.Caller, Tool: snap.ToolName, Err: execErr.Error()})
	} else {
		q.audited(AuditEntry{Event: EventExecute, ID: id, Caller: snap.Caller, Tool: snap.ToolName})
	}
	return snap, nil
}

func (q *Queue) transition(id string, next Status, reason, _ string) (Pending, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, ok := q.items[id]
	if !ok {
		return Pending{}, fmt.Errorf("%w: %s", ErrUnknownID, id)
	}
	if p.Status != StatusPending {
		return Pending{}, fmt.Errorf("%w: %s → %s", ErrIllegalTransition, p.Status, next)
	}
	t := q.now()
	p.Status = next
	p.DecidedAt = &t
	if reason != "" {
		p.Reason = reason
	}
	return *p, nil
}

// Get returns a snapshot of the entry with the given id.
func (q *Queue) Get(id string) (Pending, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, ok := q.items[id]
	if !ok {
		return Pending{}, false
	}
	return *p, true
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
		out = append(out, *p)
	}
	q.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (q *Queue) audited(e AuditEntry) {
	if q.audit == nil {
		return
	}
	_ = q.audit.Append(e)
}
