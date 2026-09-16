package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PendingCall links one tool proposal to the qid approval it awaits.
type PendingCall struct {
	CallID     string `json:"call_id"`
	ApprovalID string `json:"approval_id"`
	ToolName   string `json:"tool_name"`
	Reason     string `json:"reason,omitempty"`
}

// Session is the planner conversation persisted at an approval boundary.
type Session struct {
	Version   int           `json:"version"`
	SessionID SessionID     `json:"session_id"`
	Model     string        `json:"model,omitempty"`
	Provider  ProviderState `json:"provider_state"`
	Messages  []Message     `json:"messages"`
	Results   []ToolResult  `json:"results,omitempty"`
	Pending   []PendingCall `json:"pending"`
}

func (s Session) Save() error {
	store, err := DefaultSessionStore()
	if err != nil {
		return err
	}
	return errors.Join(store.Save(s), store.Close())
}

func LoadSession(rawID string) (Session, error) {
	id, err := ParseSessionID(rawID)
	if err != nil {
		return Session{}, err
	}
	store, err := DefaultSessionStore()
	if err != nil {
		return Session{}, err
	}
	session, loadErr := store.Load(id)
	return session, errors.Join(loadErr, store.Close())
}

func DeleteSession(id SessionID) error {
	store, err := DefaultSessionStore()
	if err != nil {
		return err
	}
	return errors.Join(store.Delete(id), store.Close())
}

func validateStoredSession(session Session) error {
	if session.Version != SessionVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidSession, session.Version)
	}
	if _, err := ParseSessionID(session.SessionID.String()); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSession, err)
	}
	if err := session.Provider.Validate(); err != nil {
		return fmt.Errorf("%w: provider state: %v", ErrInvalidSession, err)
	}
	if len(session.Messages) == 0 {
		return fmt.Errorf("%w: empty conversation", ErrInvalidSession)
	}
	last := session.Messages[len(session.Messages)-1]
	if last.Role != RoleAssistant || len(last.ToolCalls) == 0 {
		return fmt.Errorf("%w: conversation must end at an assistant tool-call turn", ErrInvalidSession)
	}

	calls := make(map[string]ToolCall, len(last.ToolCalls))
	for _, call := range last.ToolCalls {
		if err := validateCallID(call.ID); err != nil {
			return fmt.Errorf("%w: tool call id: %v", ErrInvalidSession, err)
		}
		if _, exists := calls[call.ID]; exists {
			return fmt.Errorf("%w: duplicate tool call id %q", ErrInvalidSession, call.ID)
		}
		if strings.TrimSpace(call.Name) == "" {
			return fmt.Errorf("%w: tool call %q has empty tool name", ErrInvalidSession, call.ID)
		}
		if _, err := canonicalJSON(call.Input); err != nil {
			return fmt.Errorf("%w: tool call %q input: %v", ErrInvalidSession, call.ID, err)
		}
		calls[call.ID] = call
	}

	partition := make(map[string]string, len(calls))
	for _, result := range session.Results {
		if err := validateCallID(result.CallID); err != nil {
			return fmt.Errorf("%w: result call id: %v", ErrInvalidSession, err)
		}
		if _, exists := calls[result.CallID]; !exists {
			return fmt.Errorf("%w: result references unknown call %q", ErrInvalidSession, result.CallID)
		}
		if prior := partition[result.CallID]; prior != "" {
			return fmt.Errorf("%w: call %q appears in both/duplicate %s and result partitions", ErrInvalidSession, result.CallID, prior)
		}
		partition[result.CallID] = "result"
	}
	approvalIDs := make(map[string]struct{}, len(session.Pending))
	for _, pending := range session.Pending {
		if err := validateCallID(pending.CallID); err != nil {
			return fmt.Errorf("%w: pending call id: %v", ErrInvalidSession, err)
		}
		if _, exists := calls[pending.CallID]; !exists {
			return fmt.Errorf("%w: pending references unknown call %q", ErrInvalidSession, pending.CallID)
		}
		if prior := partition[pending.CallID]; prior != "" {
			return fmt.Errorf("%w: call %q appears in both/duplicate %s and pending partitions", ErrInvalidSession, pending.CallID, prior)
		}
		if strings.TrimSpace(pending.ApprovalID) == "" || strings.TrimSpace(pending.ToolName) == "" {
			return fmt.Errorf("%w: pending call %q has empty approval id or tool name", ErrInvalidSession, pending.CallID)
		}
		if _, exists := approvalIDs[pending.ApprovalID]; exists {
			return fmt.Errorf("%w: duplicate approval id %q", ErrInvalidSession, pending.ApprovalID)
		}
		if sanitizeToolName(pending.ToolName) != calls[pending.CallID].Name {
			return fmt.Errorf("%w: pending tool %q does not match call %q", ErrInvalidSession, pending.ToolName, calls[pending.CallID].Name)
		}
		partition[pending.CallID] = "pending"
		approvalIDs[pending.ApprovalID] = struct{}{}
	}
	for callID := range calls {
		if partition[callID] == "" {
			return fmt.Errorf("%w: call %q is missing from result/pending partitions", ErrInvalidSession, callID)
		}
	}
	return nil
}

func validateCallID(callID string) error {
	if callID == "" || len(callID) > 256 || !utf8.ValidString(callID) {
		return fmt.Errorf("must be valid UTF-8 between 1 and 256 bytes")
	}
	for _, r := range callID {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("must not contain whitespace or control characters")
		}
	}
	return nil
}

func canonicalJSON(input json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("decode JSON: multiple values")
		}
		return nil, fmt.Errorf("decode trailing JSON: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical JSON: %w", err)
	}
	return canonical, nil
}

func sessionFileName(id SessionID) string { return id.String() + ".json" }

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

type limitedBytesReader struct{ data []byte }

func (r *limitedBytesReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}
