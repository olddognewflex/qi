// Package policy decides whether a tool call should be executed
// immediately, queued for explicit user confirmation, or refused outright.
// The default rules are deterministic and conservative: anything a non-cli
// caller wants to mutate routes through the approval queue. AI-driven
// callers and qi-mcp share that gate.
package policy

import (
	"context"
	"encoding/json"

	"qi/internal/tools"
)

// Decision is the outcome of a policy check.
type Decision int

const (
	Allow Decision = iota + 1
	Confirm
	Deny
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Confirm:
		return "confirm"
	case Deny:
		return "deny"
	default:
		return "unknown"
	}
}

// Verdict bundles the decision with a human-readable reason. The reason is
// surfaced in audit log entries and CLI output.
type Verdict struct {
	Decision Decision
	Reason   string
}

// Request is what a Decider inspects.
type Request struct {
	Caller string
	Tool   tools.Tool
	Params json.RawMessage
}

// Decider produces a Verdict for each tool call. Implementations must be
// safe for concurrent use.
type Decider interface {
	Decide(ctx context.Context, req Request) Verdict
}

// CallerCLI is the identity used by direct qi CLI invocations. CLI calls
// reflect the user's explicit intent and bypass the approval queue.
const CallerCLI = "cli"

// CallerRemote is the identity stamped on calls arriving through qid's
// token-authenticated HTTP endpoint (e.g. an iPhone Shortcut). It is trusted
// only because the transport already verified a shared secret, and even then
// only for the narrow allowlist a RemoteDecider permits — see RemoteDecider.
const CallerRemote = "remote"

// DefaultDecider applies built-in rules. It has no configurable state yet;
// future loaders can read deny lists or per-tool overrides from config and
// compose with this decider.
type DefaultDecider struct{}

// Decide implements the baseline policy:
//   - empty caller        → Deny (callers must identify themselves)
//   - caller == cli       → Allow
//   - non-mutating tool   → Allow
//   - mutating + non-cli  → Confirm
func (DefaultDecider) Decide(_ context.Context, req Request) Verdict {
	if req.Caller == "" {
		return Verdict{Decision: Deny, Reason: "missing caller identity"}
	}
	if req.Caller == CallerCLI {
		return Verdict{Decision: Allow, Reason: "cli call is user-driven"}
	}
	if !req.Tool.Mutating {
		return Verdict{Decision: Allow, Reason: "read-only tool"}
	}
	return Verdict{Decision: Confirm, Reason: "mutation by non-cli caller requires approval"}
}

// RemoteDecider extends a base Decider with a narrow allowlist for the
// CallerRemote identity. A token-authenticated remote caller may invoke an
// allowlisted tool directly (Allow); any other tool — or any other caller —
// falls through to the base decider, so the approval gate still governs every
// non-allowlisted mutation. This keeps the invariant "no caller silently
// bypasses the gate" intact: the bypass is an explicit, audited, tool-scoped
// exception, not a blanket trust of the caller.
type RemoteDecider struct {
	// Base decides every request that the remote allowlist does not match.
	Base Decider
	// Allowed is the set of tool names a remote caller may run directly.
	Allowed map[string]struct{}
}

// NewRemoteDecider builds a RemoteDecider over DefaultDecider allowing the
// named tools for CallerRemote.
func NewRemoteDecider(allowed ...string) RemoteDecider {
	set := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		set[name] = struct{}{}
	}
	return RemoteDecider{Base: DefaultDecider{}, Allowed: set}
}

// Decide implements Decider.
func (d RemoteDecider) Decide(ctx context.Context, req Request) Verdict {
	if req.Caller == CallerRemote {
		if _, ok := d.Allowed[req.Tool.Name]; ok {
			return Verdict{Decision: Allow, Reason: "remote caller, allowlisted tool"}
		}
		// Not allowlisted: refuse outright rather than queue. The remote
		// transport has no human at the keyboard to clear an approval.
		return Verdict{Decision: Deny, Reason: "remote caller may not invoke " + req.Tool.Name}
	}
	base := d.Base
	if base == nil {
		base = DefaultDecider{}
	}
	return base.Decide(ctx, req)
}
