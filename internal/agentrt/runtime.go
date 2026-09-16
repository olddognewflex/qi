// Package agentrt defines qi's view of externally managed coding agents
// (Claude Code, Codex, ...) and the Runtime interface through which qi
// discovers, addresses, and observes them.
//
// qi reasons about agent *instances*, never bare agent types: two Claude
// Code processes in the same workspace are two distinct AgentInstances with
// distinct IDs, panes, and session identities.
//
// The runtime authority for discovery and state is whichever Runtime
// implementation is wired (see Detect): HerdrRuntime when qi runs under
// Herdr, LocalRuntime otherwise. qi never maintains its own registry of
// agent processes when a runtime can answer the question.
package agentrt

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Kind is an agent type as reported by the runtime, lowercased
// ("claude", "codex"). It is deliberately an open string: new kinds appear
// as the runtime learns to detect them, and qi must not enumerate them.
type Kind string

const (
	KindClaude Kind = "claude"
	KindCodex  Kind = "codex"
)

// State is an agent's lifecycle state as classified by the runtime. qi
// never re-derives these from terminal output.
type State string

const (
	StateWorking State = "working"
	StateBlocked State = "blocked" // waiting on an approval / question UI
	StateDone    State = "done"    // finished a turn, completion not yet seen
	StateIdle    State = "idle"    // ready for input
	StateUnknown State = "unknown" // present but unclassified; not proof of completion
)

// ReadyStates are the settled states in which an agent accepts a prompt.
var ReadyStates = []State{StateIdle, StateDone}

// SettledStates are the states a wait returns on by default: the agent has
// either finished or needs a human.
var SettledStates = []State{StateIdle, StateDone, StateBlocked}

// AgentID is the runtime's opaque handle for one agent instance. For Herdr
// it is the pane ID currently hosting the agent (e.g. "w9:p1"); callers must
// treat it as opaque and re-discover rather than construct it.
type AgentID string

// Workspace is a runtime-owned context boundary: a label, a working
// directory, and the agents inside it.
type Workspace struct {
	ID      string // runtime handle, e.g. "w9"
	Number  int    // runtime ordinal, e.g. 5; 0 when unknown
	Label   string // human label, e.g. "qi"
	Cwd     string // best-effort: derived from the workspace's panes; "" when unknown
	Focused bool
}

// AgentInstance is one running agent as the runtime sees it.
type AgentInstance struct {
	ID        AgentID
	Kind      Kind
	Name      string // runtime-assigned unique name, "" when unnamed
	State     State
	SessionID string // the agent's own session identity (e.g. Claude Code session UUID); "" when unknown
	Workspace Workspace
	TabID     string
	PaneID    string // runtime pane handle, e.g. "w9:p1"
	Cwd       string
	Focused   bool
	Title     string // terminal title with status glyphs stripped, "" when unknown
}

// PaneLabel renders a short human/speakable pane reference such as "5-1"
// (workspace number, pane ordinal). It falls back to the raw PaneID when the
// runtime's ID scheme is not the expected "<ws>:p<N>" form.
func (a AgentInstance) PaneLabel() string {
	if a.Workspace.Number > 0 {
		if i := strings.LastIndex(a.PaneID, ":p"); i >= 0 {
			if n := a.PaneID[i+2:]; n != "" && isDigits(n) {
				return fmt.Sprintf("%d-%s", a.Workspace.Number, n)
			}
		}
	}
	return a.PaneID
}

// DisplayKind renders the agent kind for humans: "Claude", "Codex", or the
// capitalised raw kind for anything else.
func (a AgentInstance) DisplayKind() string {
	switch a.Kind {
	case KindClaude:
		return "Claude"
	case KindCodex:
		return "Codex"
	case "":
		return "agent"
	}
	s := string(a.Kind)
	return strings.ToUpper(s[:1]) + s[1:]
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Focus is where the user's attention is, as the runtime reports it.
type Focus struct {
	WorkspaceID string
	PaneID      string
	AgentID     AgentID // "" when the focused pane hosts no agent
}

// OutputSource selects which terminal snapshot to read.
type OutputSource string

const (
	SourceVisible         OutputSource = "visible"
	SourceRecent          OutputSource = "recent"
	SourceRecentUnwrapped OutputSource = "recent-unwrapped" // soft wraps joined; best for transcripts
)

// OutputOptions bounds a ReadOutput call.
type OutputOptions struct {
	Lines  int          // 0 = runtime default
	Source OutputSource // "" = SourceRecentUnwrapped
}

// AgentOutput is a snapshot of an agent's terminal text.
type AgentOutput struct {
	Text      string
	Truncated bool
}

// Runtime is the discovery/control surface qi uses to address agents.
// Implementations own *where* agents are and *what* they are doing; qi owns
// who the user means and what to do next.
//
// Every method takes a context; implementations must honour cancellation,
// since Send/WaitForState can block on a remote agent.
type Runtime interface {
	// Name identifies the implementation ("herdr", "local") for diagnostics.
	Name() string
	// ListWorkspaces returns every workspace the runtime knows.
	ListWorkspaces(ctx context.Context) ([]Workspace, error)
	// ListAgents returns every live agent instance across all workspaces.
	ListAgents(ctx context.Context) ([]AgentInstance, error)
	// GetAgent returns one instance, or ErrAgentNotFound.
	GetAgent(ctx context.Context, id AgentID) (AgentInstance, error)
	// Focused reports the UI-focused workspace/pane (and its agent, if any).
	Focused(ctx context.Context) (Focus, error)
	// Send submits message as a prompt to the agent and returns once the
	// runtime has accepted it. It returns ErrAgentBlocked, without sending,
	// when the agent is waiting on an approval/question UI.
	Send(ctx context.Context, id AgentID, message string) error
	// ReadOutput returns a snapshot of the agent's terminal output.
	ReadOutput(ctx context.Context, id AgentID, opts OutputOptions) (AgentOutput, error)
	// WaitForState blocks until the agent reaches one of states (or
	// SettledStates when none are given) and returns the state reached. It
	// returns ErrTimeout when ctx expires first.
	WaitForState(ctx context.Context, id AgentID, states ...State) (State, error)
}

// Sentinel errors. Implementations wrap these (errors.Is) so callers can
// branch without knowing the runtime.
var (
	ErrUnavailable   = errors.New("agent runtime unavailable")
	ErrAgentNotFound = errors.New("agent not found")
	ErrAgentBlocked  = errors.New("agent is blocked on an approval or question")
	ErrTimeout       = errors.New("timed out waiting for agent")
	ErrStalled       = errors.New("agent did not start working after the prompt")
)

// RuntimeError carries a runtime-specific error code alongside the mapped
// sentinel (Unwrap), e.g. Herdr's "agent_not_found".
type RuntimeError struct {
	Code    string
	Message string
	Err     error // mapped sentinel, may be nil
}

func (e *RuntimeError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Code
}

func (e *RuntimeError) Unwrap() error { return e.Err }
