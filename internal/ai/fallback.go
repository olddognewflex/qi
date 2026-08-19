// This file implements provider failover: an LLM decorator that walks an
// ordered chain of providers, advancing to the next when the current one
// fails provider-side (usage limit hit, endpoint down). The planner stays
// unaware — it sees one LLM.

package ai

import (
	"context"
	"errors"
	"fmt"
)

// FallbackEntry is one provider in a failover chain, carrying its own model
// id: model names are provider-specific, so the chain stamps the entry's
// model into each request rather than sharing the planner's.
type FallbackEntry struct {
	Name     string // provider label for switch notices, e.g. "ollama"
	Provider Provider
	LLM      LLM
	Model    string // may be empty for providers with a built-in default
	ConfigID string // canonical secret-free connection identity
}

// FallbackLLM tries each entry in order. Failover is sticky for the life of
// the value: once an entry fails over, later Generate calls start at the
// next entry, so one conversation doesn't re-probe a dead provider every
// turn. Construct per process/run — a fresh CLI invocation re-probes the
// primary. Not safe for concurrent use, matching the planner's single
// conversation loop.
type FallbackLLM struct {
	entries  []FallbackEntry
	active   int
	onSwitch func(from, to FallbackEntry, err error)
}

// NewFallbackLLM builds a chain over entries, in priority order. onSwitch,
// when non-nil, is called before each failover — surface it to the user; a
// silent switch from a free local model to a paid API is a bad surprise.
func NewFallbackLLM(entries []FallbackEntry, onSwitch func(from, to FallbackEntry, err error)) (*FallbackLLM, error) {
	if len(entries) == 0 {
		return nil, errors.New("ai: fallback chain needs at least one provider")
	}
	return &FallbackLLM{entries: append([]FallbackEntry(nil), entries...), onSwitch: onSwitch}, nil
}

// NewFallbackLLMFromState reconstructs a chain at the saved sticky entry.
// Entries are built by the caller, which remains responsible for resolving
// current credentials and matching them against the saved identities.
func NewFallbackLLMFromState(entries []FallbackEntry, state ProviderState, onSwitch func(from, to FallbackEntry, err error)) (*FallbackLLM, error) {
	if err := state.Validate(); err != nil {
		return nil, err
	}
	entryState, err := providerStateFromEntries(entries)
	if err != nil {
		return nil, err
	}
	if len(entryState.Entries) != len(state.Entries) {
		return nil, fmt.Errorf("%w: saved provider entries do not match reconstructed chain", ErrInvalidProviderState)
	}
	for index, entry := range entryState.Entries {
		if entry != state.Entries[index] {
			return nil, fmt.Errorf("%w: saved provider entry %d does not match reconstructed chain", ErrInvalidProviderState, index)
		}
	}
	return &FallbackLLM{entries: append([]FallbackEntry(nil), entries...), active: state.Active, onSwitch: onSwitch}, nil
}

// ProviderState exports the current sticky failover position without exposing
// an LLM implementation, URL, API key, or environment value.
func (f *FallbackLLM) ProviderState() (ProviderState, error) {
	state, err := providerStateFromEntries(f.entries)
	if err != nil {
		return ProviderState{}, err
	}
	state.Active = f.active
	return state, nil
}

func providerStateFromEntries(entries []FallbackEntry) (ProviderState, error) {
	state := ProviderState{Version: ProviderStateVersion, Entries: make([]ProviderStateEntry, 0, len(entries))}
	for _, entry := range entries {
		if entry.LLM == nil {
			return ProviderState{}, fmt.Errorf("%w: provider %q has no LLM", ErrInvalidProviderState, entry.Provider)
		}
		state.Entries = append(state.Entries, ProviderStateEntry{
			Provider: entry.Provider,
			Model:    entry.Model,
			ConfigID: entry.ConfigID,
		})
	}
	if err := state.Validate(); err != nil {
		return ProviderState{}, err
	}
	return state, nil
}

// Generate implements LLM. Provider-side failures (ShouldFailover) advance
// the chain; anything else — context cancellation, translation bugs — is
// returned immediately from the entry that produced it.
func (f *FallbackLLM) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	var errs []error
	for f.active < len(f.entries) {
		e := f.entries[f.active]
		r := req
		r.Model = e.Model
		resp, err := e.LLM.Generate(ctx, r)
		if err == nil {
			return resp, nil
		}
		if !ShouldFailover(err) {
			return nil, err
		}
		// Providers already prefix errors with their own name; no re-wrap.
		errs = append(errs, err)
		f.active++
		if f.active < len(f.entries) && f.onSwitch != nil {
			f.onSwitch(e, f.entries[f.active], err)
		}
	}
	if len(errs) == 0 {
		// The chain was exhausted by an earlier Generate call.
		return nil, errors.New("ai: all providers exhausted")
	}
	return nil, fmt.Errorf("ai: all providers failed: %w", errors.Join(errs...))
}
