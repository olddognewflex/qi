// Package approval queues mutating tool calls that policy did not auto-allow
// and records every state transition to an append-only JSONL audit log. The
// queue lives in memory; the audit log on disk is the durable record.
package approval

import (
	"bufio"
	"crypto/sha256"
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
	MaxAuditRecordBytes    = MaxTerminalResultBytes + (512 << 10)
	MaxAuditLogBytes       = 64 << 20
	MaxAuditReplayEntries  = 10_000
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
	Time       time.Time        `json:"time"`
	Event      AuditEvent       `json:"event"`
	ID         string           `json:"id"`
	Caller     string           `json:"caller,omitempty"`
	CallID     string           `json:"call_id,omitempty"`
	Tool       string           `json:"tool,omitempty"`
	Reason     string           `json:"reason,omitempty"`
	Err        string           `json:"err,omitempty"`
	Params     json.RawMessage  `json:"params,omitempty"`
	ParamsHash string           `json:"params_hash,omitempty"`
	Outcome    *TerminalOutcome `json:"outcome,omitempty"`
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
	mu            sync.Mutex
	f             *os.File
	path          string
	reserved      map[string]int64
	reservedBytes int64
	recordCount   int
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
	info, err := f.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("audit inspect: %w", err), f.Close())
	}
	if info.Size() > MaxAuditLogBytes {
		return nil, errors.Join(fmt.Errorf("audit log exceeds %d bytes", MaxAuditLogBytes), f.Close())
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("audit chmod: %w", err), f.Close())
	}
	entries, err := ReadAuditLog(path)
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return &Audit{f: f, path: path, reserved: make(map[string]int64), recordCount: len(entries)}, nil
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
	info, err := a.f.Stat()
	if err != nil {
		return fmt.Errorf("audit inspect before write: %w", err)
	}
	reservedAfter := a.reservedBytes
	reservation := a.reserved[e.ID]
	reservedEntriesAfter := len(a.reserved)
	if e.Event == EventExecute || e.Event == EventFail {
		reservedAfter -= reservation
		if reservation != 0 {
			reservedEntriesAfter--
		}
	}
	required := int64(len(line))
	requiredEntries := 1
	if e.Event == EventApprove {
		if reservation != 0 {
			return fmt.Errorf("audit terminal capacity already reserved for %s", e.ID)
		}
		required += MaxAuditRecordBytes
		requiredEntries++
	}
	if a.recordCount+reservedEntriesAfter+requiredEntries > MaxAuditReplayEntries {
		return fmt.Errorf("audit replay exceeds %d entries", MaxAuditReplayEntries)
	}
	if info.Size()+reservedAfter+required > MaxAuditLogBytes {
		return fmt.Errorf("audit log exceeds %d bytes", MaxAuditLogBytes)
	}
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
	switch e.Event {
	case EventApprove:
		a.reserved[e.ID] = MaxAuditRecordBytes
		a.reservedBytes += MaxAuditRecordBytes
	case EventExecute, EventFail:
		if reservation != 0 {
			delete(a.reserved, e.ID)
			a.reservedBytes -= reservation
		}
	}
	a.recordCount++
	return nil
}

func auditParamsHash(params json.RawMessage) string {
	return fmt.Sprintf("%x", sha256.Sum256(params))
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
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("audit inspect: %w", err)
	}
	if info.Size() > MaxAuditLogBytes {
		return nil, fmt.Errorf("audit log exceeds %d bytes", MaxAuditLogBytes)
	}

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
		if len(entries) == MaxAuditReplayEntries {
			return entries, fmt.Errorf("audit replay exceeds %d entries", MaxAuditReplayEntries)
		}
		entries = append(entries, e)
		if errors.Is(readErr, io.EOF) {
			break
		}
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
