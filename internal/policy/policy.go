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
