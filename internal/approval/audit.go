// Package approval queues mutating tool calls that policy did not auto-allow
// and records every state transition to an append-only JSONL audit log. The
// queue lives in memory; the audit log on disk is the durable record.
package approval

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
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
	Time   time.Time       `json:"time"`
	Event  AuditEvent      `json:"event"`
	ID     string          `json:"id"`
	Caller string          `json:"caller,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Reason string          `json:"reason,omitempty"`
	Err    string          `json:"err,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
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
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("audit write: %w", err)
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
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			// Truncated trailing line from an interrupted write; stop here.
			break
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return entries, fmt.Errorf("audit scan: %w", err)
	}
	return entries, nil
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
