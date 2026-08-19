package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ProviderStateVersion identifies the persisted provider-state schema.
const ProviderStateVersion = 1

var ErrInvalidProviderState = errors.New("invalid ai provider state")

// ProviderState is the secret-free provider selection persisted with a
// planner session. Credentials and endpoint URLs are deliberately absent.
type ProviderState struct {
	Version int                  `json:"version"`
	Entries []ProviderStateEntry `json:"entries"`
	Active  int                  `json:"active"`
}

// ProviderStateEntry identifies one configured provider without retaining the
// connection details that produced ConfigID.
type ProviderStateEntry struct {
	Provider Provider `json:"provider"`
	Model    string   `json:"model"`
	ConfigID string   `json:"config_id"`
}

// ConfigIDForDescriptor returns the canonical lowercase SHA-256 identity for
// a caller-normalized, non-secret connection descriptor.
func ConfigIDForDescriptor(descriptor string) string {
	sum := sha256.Sum256([]byte(descriptor))
	return hex.EncodeToString(sum[:])
}

// Validate checks that persisted state can be reconstructed without guessing
// at providers or selecting an invalid fallback entry.
func (s ProviderState) Validate() error {
	if s.Version != ProviderStateVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidProviderState, s.Version)
	}
	if len(s.Entries) == 0 {
		return fmt.Errorf("%w: no provider entries", ErrInvalidProviderState)
	}
	if s.Active < 0 || s.Active >= len(s.Entries) {
		return fmt.Errorf("%w: active index %d out of range", ErrInvalidProviderState, s.Active)
	}

	identities := make(map[string]struct{}, len(s.Entries))
	for _, entry := range s.Entries {
		provider, err := ParseProvider(string(entry.Provider))
		if err != nil || provider == "" || provider != entry.Provider {
			return fmt.Errorf("%w: provider %q is not normalized", ErrInvalidProviderState, entry.Provider)
		}
		if !isCanonicalConfigID(entry.ConfigID) {
			return fmt.Errorf("%w: config id for provider %q is not a lowercase SHA-256 digest", ErrInvalidProviderState, entry.Provider)
		}
		identity := string(entry.Provider) + "\x00" + entry.Model + "\x00" + entry.ConfigID
		if _, exists := identities[identity]; exists {
			return fmt.Errorf("%w: duplicate provider identity %q", ErrInvalidProviderState, entry.Provider)
		}
		identities[identity] = struct{}{}
	}
	return nil
}

func isCanonicalConfigID(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
