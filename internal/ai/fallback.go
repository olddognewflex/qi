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
	Name  string // provider label for switch notices, e.g. "ollama"
	LLM   LLM
	Model string // may be empty for providers with a built-in default
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
	return &FallbackLLM{entries: entries, onSwitch: onSwitch}, nil
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
