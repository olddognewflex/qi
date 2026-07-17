// This file defines the typed error surface providers report API failures
// through, and the predicate the fallback chain uses to decide whether an
// error is worth failing over on.

package ai

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

// ProviderError is a non-2xx response from a provider's API. Providers wrap
// HTTP-level failures in it so callers can tell quota exhaustion (429/402)
// apart from other failures without string-sniffing.
type ProviderError struct {
	Provider   string // provider label, e.g. "ollama", "anthropic"
	StatusCode int
	Body       string // response body, truncated by the provider
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.StatusCode, e.Body)
}

// ShouldFailover reports whether err is provider-side — an API error status
// or a transport failure — meaning another provider might succeed where this
// one did not. Context cancellation and local translation/marshal errors are
// not failover-worthy: retrying them elsewhere either repeats the bug or
// fights the caller's cancellation.
func ShouldFailover(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		return true
	}
	var ue *url.Error // connection refused, DNS failure, TLS, timeouts
	return errors.As(err, &ue)
}

// IsExhausted reports whether err means the provider's usage allowance is
// spent: rate/usage limit (429) or billing (402).
func IsExhausted(err error) bool {
	var pe *ProviderError
	if !errors.As(err, &pe) {
		return false
	}
	return pe.StatusCode == 429 || pe.StatusCode == 402
}
