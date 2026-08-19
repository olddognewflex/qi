package ai

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const sessionIDBytes = 32

var ErrInvalidSessionID = errors.New("invalid planner session id")

// SessionID is the canonical lowercase hexadecimal encoding of 256 random bits.
type SessionID struct{ raw string }

func ParseSessionID(raw string) (SessionID, error) {
	if len(raw) != hex.EncodedLen(sessionIDBytes) {
		return SessionID{}, fmt.Errorf("%w: must be exactly 64 lowercase hexadecimal characters", ErrInvalidSessionID)
	}
	decoded := make([]byte, sessionIDBytes)
	if _, err := hex.Decode(decoded, []byte(raw)); err != nil || hex.EncodeToString(decoded) != raw {
		return SessionID{}, fmt.Errorf("%w: must be exactly 64 lowercase hexadecimal characters", ErrInvalidSessionID)
	}
	return SessionID{raw: raw}, nil
}

func GenerateSessionID() (SessionID, error) {
	return generateSessionID(rand.Reader)
}

func generateSessionID(reader io.Reader) (SessionID, error) {
	var random [sessionIDBytes]byte
	if _, err := io.ReadFull(reader, random[:]); err != nil {
		return SessionID{}, fmt.Errorf("generate planner session id: %w", err)
	}
	return SessionID{raw: hex.EncodeToString(random[:])}, nil
}

func (id SessionID) String() string { return id.raw }

func (id SessionID) MarshalJSON() ([]byte, error) {
	parsed, err := ParseSessionID(id.String())
	if err != nil {
		return nil, err
	}
	return json.Marshal(parsed.String())
}

func (id *SessionID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode planner session id: %w", err)
	}
	parsed, err := ParseSessionID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
