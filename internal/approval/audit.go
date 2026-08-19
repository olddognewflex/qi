// Package approval queues mutating tool calls that policy did not auto-allow
// and records every state transition to an append-only JSONL audit log. The
// queue lives in memory; the audit log on disk is the durable record.
package approval

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	MaxTerminalResultBytes = 4 << 20
	MaxAuditRecordBytes    = MaxTerminalResultBytes + (64 << 10)
)

// AuditEvent identifies what happened to a queue entry.
type AuditEvent string

const (
	EventEnqueue AuditEvent = "enqueue"
	EventApprove AuditEvent = "approve"
	EventDeny    AuditEvent = "deny"
	EventExecute AuditEvent = "execute"
	EventFail    AuditEvent = "fail"
)

// AuditEntry is a single line in the audit log.
type AuditEntry struct {
	Time    time.Time        `json:"time"`
	Event   AuditEvent       `json:"event"`
	ID      string           `json:"id"`
	Caller  string           `json:"caller,omitempty"`
	CallID  string           `json:"call_id,omitempty"`
	Tool    string           `json:"tool,omitempty"`
	Reason  string           `json:"reason,omitempty"`
	Err     string           `json:"err,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Outcome *TerminalOutcome `json:"outcome,omitempty"`
}

// TerminalOutcome is present only on durable terminal records. Its pointer
// presence distinguishes a new empty/null result from a legacy terminal event.
type TerminalOutcome struct {
	Status     Status          `json:"status"`
	Result     json.RawMessage `json:"result"`
	Reason     string          `json:"reason,omitempty"`
	Err        string          `json:"err,omitempty"`
	DecidedAt  *time.Time      `json:"decided_at,omitempty"`
	ExecutedAt *time.Time      `json:"executed_at,omitempty"`
}

// Audit is an append-only JSONL writer. Safe for concurrent use.
type Audit struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// OpenAudit opens (or creates) the audit log at path with 0600 perms.
func OpenAudit(path string) (*Audit, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("audit mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit open: %w", err)
	}
	return &Audit{f: f, path: path}, nil
}

// Append writes one entry to the log. The entry's Time is set if zero.
func (a *Audit) Append(e AuditEntry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("audit marshal: %w", err)
	}
	if len(b) > MaxAuditRecordBytes {
		return fmt.Errorf("audit record exceeds %d bytes", MaxAuditRecordBytes)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return errors.New("audit write: audit is closed")
	}
	line := append(b, '\n')
	n, err := a.f.Write(line)
	if err != nil {
		return fmt.Errorf("audit write: %w", err)
	}
	if n != len(line) {
		return fmt.Errorf("audit write: short write: wrote %d of %d bytes", n, len(line))
	}
	if err := a.f.Sync(); err != nil {
		return fmt.Errorf("audit sync: %w", err)
	}
	return nil
}

// Path returns the audit log location.
func (a *Audit) Path() string { return a.path }

// ReadAuditLog parses every entry in the JSONL audit log at path. A missing
// file yields a nil slice (nothing has happened yet). A truncated final line
// — the signature of a crash mid-write — is tolerated: parsing stops at the
// first unreadable line and returns the entries read so far. Used on startup
// to reconstruct queue state from the durable record.
func ReadAuditLog(path string) ([]AuditEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("audit open: %w", err)
	}
	defer f.Close()

	var entries []AuditEntry
	reader := bufio.NewReaderSize(f, MaxAuditRecordBytes+1)
	for {
		line, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			return entries, fmt.Errorf("audit record exceeds %d bytes", MaxAuditRecordBytes)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return entries, fmt.Errorf("audit read: %w", readErr)
		}
		terminated := len(line) > 0 && line[len(line)-1] == '\n'
		if terminated {
			line = line[:len(line)-1]
		}
		if len(line) == 0 {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			if errors.Is(readErr, io.EOF) && !terminated {
				break
			}
			return entries, fmt.Errorf("audit decode: %w", err)
		}
		if e.Outcome != nil && len(e.Outcome.Result) > MaxTerminalResultBytes {
			return entries, fmt.Errorf("terminal result exceeds %d bytes", MaxTerminalResultBytes)
		}
		entries = append(entries, e)
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return entries, nil
}

// RestoreStats reports how many live and lookup-only entries replay restored.
type RestoreStats struct {
	Pending  int
	Terminal int
}

// Restore rebuilds live pending entries and lookup-only terminal history from
// a replayed audit log. Approved entries without a durable terminal outcome
// are restored as pending and require another explicit approval. It never
// executes tools or emits new audit events.
func (q *Queue) Restore(entries []AuditEntry) RestoreStats {
	type accumulator struct {
		enqueue *AuditEntry
		latest  AuditEntry
	}
	byID := make(map[string]*accumulator)
	order := make([]string, 0)
	for i := range entries {
		e := entries[i]
		if e.ID == "" {
			continue
		}
		a, ok := byID[e.ID]
		if !ok {
			a = &accumulator{}
			byID[e.ID] = a
			order = append(order, e.ID)
		}
		if e.Event == EventEnqueue {
			copy := e
			a.enqueue = &copy
		}
		a.latest = e
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	stats := RestoreStats{}
	for _, id := range order {
		a := byID[id]
		if a.enqueue == nil || q.hasID(id) {
			continue
		}
		p := &Pending{
			ID: id, Caller: a.enqueue.Caller, CallID: a.enqueue.CallID,
			ToolName: a.enqueue.Tool, Params: cloneRaw(a.enqueue.Params),
			Status: StatusPending, Reason: a.enqueue.Reason, CreatedAt: a.enqueue.Time,
		}
		switch a.latest.Event {
		case EventDeny, EventExecute, EventFail:
			outcome := a.latest.Outcome
			if outcome == nil || !terminalOutcomeMatches(a.latest.Event, outcome.Status) {
				continue
			}
			p.Status, p.Result, p.Reason, p.Err = outcome.Status, cloneRaw(outcome.Result), outcome.Reason, outcome.Err
			p.DecidedAt, p.ExecutedAt = cloneTime(outcome.DecidedAt), cloneTime(outcome.ExecutedAt)
			q.history[id] = p
			stats.Terminal++
		case EventEnqueue, EventApprove:
			q.items[id] = p
			stats.Pending++
		}
	}
	return stats
}

func (q *Queue) hasID(id string) bool {
	if _, exists := q.items[id]; exists {
		return true
	}
	_, exists := q.history[id]
	return exists
}

func terminalOutcomeMatches(event AuditEvent, status Status) bool {
	switch event {
	case EventDeny:
		return status == StatusDenied
	case EventExecute:
		return status == StatusExecuted
	case EventFail:
		return status == StatusFailed
	default:
		return false
	}
}

// Close flushes and closes the underlying file.
func (a *Audit) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return nil
	}
	err := a.f.Close()
	a.f = nil
	return err
}
