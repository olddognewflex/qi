package ai

import (
	"errors"
	"fmt"
	"io"
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
	Version   int                   `json:"version"`
	SessionID SessionID             `json:"session_id"`
	Model     string                `json:"model,omitempty"`
	Messages  []Message             `json:"messages"`
	Results   map[string]ToolResult `json:"results,omitempty"`
	Pending   []PendingCall         `json:"pending"`
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
	return nil
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
