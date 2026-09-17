package voice

import (
	"fmt"
	"os"
	"strings"

	"qi/internal/agentrt"
	"qi/internal/service"
)

// Env is what qi knows about where it is running. When qi runs inside a Herdr
// pane the runtime exports the workspace and pane it lives in, which is how
// "in this workspace" gets an answer without asking the user.
type Env struct {
	WorkspaceID string // HERDR_WORKSPACE_ID
	PaneID      string // HERDR_PANE_ID
	Cwd         string // working directory
}

// EnvFromOS reads the environment qi was started in. A missing variable is
// simply absent: nothing here fails, and an empty Env degrades to "ask the
// user which workspace", never to a guess.
func EnvFromOS() Env {
	e := Env{
		WorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"),
		PaneID:      os.Getenv("HERDR_PANE_ID"),
	}
	if cwd, err := os.Getwd(); err == nil {
		e.Cwd = cwd
	}
	return e
}

// Context is the conversational memory of one voice session: who was last
// addressed, what they were asked, and whether a clarification question is
// outstanding.
//
// It exists so that a follow-up sentence can be short. "Have Claude review
// that." means the Claude we were just talking to, about the thing we just
// asked for. Everything it does is deterministic and inspectable; there is no
// model resolving these references.
//
// A Context belongs to one loop and is not safe for concurrent use.
type Context struct {
	// LastAgent is the agent most recently addressed, nil at the start of a
	// session.
	LastAgent *agentrt.AgentInstance
	// LastInstruction is the text most recently relayed to LastAgent.
	LastInstruction string
	// LastResult is the summary of what came back from it.
	LastResult string
	// Pending is an outstanding clarification question; nil when none.
	Pending *service.AmbiguousAgentError
	// PendingIntent is the instruction that could not be addressed until the
	// user answers Pending.
	PendingIntent *Intent
	// Env is where qi runs, so a clarification answer of "the one in this
	// workspace" can be resolved. Set by the loop from Options.Env.
	Env Env
}

// pronouns are the back-references [Context.Expand] annotates. An agent
// receiving "review that" in a fresh session has no idea what "that" is; the
// annotation is what gives it one.
var pronouns = map[string]bool{
	"that":  true,
	"it":    true,
	"its":   true,
	"this":  true,
	"those": true,
	"these": true,
	"them":  true,
	"they":  true,
}

// Expand annotates back-references in an instruction with what the session
// knows, so the receiving agent gets a self-contained prompt:
//
//	Expand("review that") with LastAgent Codex and LastInstruction
//	"run the tests" becomes
//	`review that (the result of the previous request to Codex: "run the tests")`
//
// It is deliberately additive: the user's own words are never rewritten or
// dropped, only followed by a parenthetical. Without a pronoun, or without a
// previous request to point at, the instruction is returned unchanged.
func (c *Context) Expand(instruction string) string {
	if c == nil || c.LastInstruction == "" {
		return instruction
	}
	if !hasPronoun(instruction) {
		return instruction
	}
	who := "the previous agent"
	if c.LastAgent != nil {
		who = c.LastAgent.DisplayKind()
	}
	return fmt.Sprintf("%s (the result of the previous request to %s: %q)", instruction, who, c.LastInstruction)
}

// hasPronoun reports whether the instruction contains a standalone
// back-reference. Matching is per word, so "commit" is not a hit for "it".
func hasPronoun(instruction string) bool {
	for _, w := range strings.Fields(strings.ToLower(instruction)) {
		if pronouns[strings.Trim(w, ".,!?;:\"'")] {
			return true
		}
	}
	return false
}

// Query turns a stated [Target] into a [service.AgentQuery], filling in what
// the user left implicit from the environment and the conversation.
//
// Three rules, in order:
//
//   - "The agent I'm looking at" becomes a focus query; the runtime, not qi,
//     decides which pane that is.
//   - "In this workspace" becomes the workspace qi itself runs in when Herdr
//     exported one, and otherwise the working directory, which the service
//     matches against each agent's own directory.
//   - A bare kind with nothing else ("Have Claude review that") addresses the
//     same instance the session was just talking to, when that instance is of
//     the named kind. This is the conversational continuity that keeps a
//     follow-up from re-asking which Claude was meant.
//
// Anything left unconstrained stays unconstrained: the query goes to Resolve
// as a wildcard, and Resolve asks. Under-specifying produces a question, never
// a pick.
func (c *Context) Query(t Target, env Env) service.AgentQuery {
	q := service.AgentQuery{
		Kind:      t.Kind,
		Workspace: t.Workspace,
		Name:      t.Name,
	}
	if t.Focused {
		q.Focused = true
	}
	q.State = t.State
	if t.ThisWorkspace {
		if env.WorkspaceID != "" {
			q.Workspace = env.WorkspaceID
		} else {
			q.Cwd = env.Cwd
		}
	}
	if c != nil && c.LastAgent != nil && isConversational(t) && c.LastAgent.Kind == t.Kind {
		// A soft preference, not a hard handle: Resolve still checks the
		// kind and session identity, so a pane that now hosts a different
		// agent (or a restarted one) is asked about rather than assumed.
		q.PreferID = c.LastAgent.ID
		q.PreferSessionID = c.LastAgent.SessionID
	}
	return q
}

// isConversational reports whether the target names only a kind, which is the
// shape that inherits the session's last agent.
func isConversational(t Target) bool {
	return t.Kind != "" && t.Workspace == "" && t.Name == "" && !t.ThisWorkspace && !t.Focused
}

// Choose applies a clarification answer to the pending question and returns
// the chosen agent. On success the pending question is cleared, including
// [Context.PendingIntent]: a caller that needs the deferred instruction must
// read it before calling.
//
// Every error is a sentence meant to be spoken back to the user, and an
// unusable answer leaves the question pending so they can answer again.
func (c *Context) Choose(t Target) (agentrt.AgentInstance, error) {
	if c == nil || c.Pending == nil {
		return agentrt.AgentInstance{}, fmt.Errorf("I'm not waiting on a choice")
	}
	cands := c.Pending.Candidates

	var chosen agentrt.AgentInstance
	switch {
	case t.Ordinal > 0:
		if t.Ordinal > len(cands) {
			return agentrt.AgentInstance{}, fmt.Errorf("I only have %s to choose from", pluralCount(len(cands), "option"))
		}
		chosen = cands[t.Ordinal-1]

	case t.Pane != "":
		matches := make([]agentrt.AgentInstance, 0, 1)
		for _, a := range cands {
			if strings.EqualFold(a.PaneLabel(), t.Pane) || strings.EqualFold(string(a.PaneID), t.Pane) {
				matches = append(matches, a)
			}
		}
		if len(matches) != 1 {
			return agentrt.AgentInstance{}, fmt.Errorf("I don't have exactly one candidate in pane %s", t.Pane)
		}
		chosen = matches[0]

	case t.Workspace != "" || t.ThisWorkspace:
		matches := make([]agentrt.AgentInstance, 0, 1)
		for _, a := range cands {
			if t.ThisWorkspace {
				if c.Env.WorkspaceID != "" && a.Workspace.ID == c.Env.WorkspaceID {
					matches = append(matches, a)
				}
			} else if service.WorkspaceMatches(a.Workspace, t.Workspace) {
				matches = append(matches, a)
			}
		}
		switch len(matches) {
		case 0:
			return agentrt.AgentInstance{}, fmt.Errorf("none of them is in that workspace")
		case 1:
			chosen = matches[0]
		default:
			return agentrt.AgentInstance{}, fmt.Errorf("%s of them are in that workspace, so tell me the pane instead", pluralCount(len(matches), "one"))
		}

	case t.State != "":
		matches := make([]agentrt.AgentInstance, 0, 1)
		for _, a := range cands {
			if a.State == t.State {
				matches = append(matches, a)
			}
		}
		switch len(matches) {
		case 0:
			return agentrt.AgentInstance{}, fmt.Errorf("none of them is %s", t.State)
		case 1:
			chosen = matches[0]
		default:
			return agentrt.AgentInstance{}, fmt.Errorf("%s of them are %s, so tell me the pane instead", pluralCount(len(matches), "one"), t.State)
		}

	default:
		return agentrt.AgentInstance{}, fmt.Errorf("I didn't catch which one you meant")
	}

	c.Pending = nil
	c.PendingIntent = nil
	return chosen, nil
}

// countWords spells small numbers so a reply reads naturally when spoken.
var countWords = [...]string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}

// pluralCount renders "two options" / "one option".
func pluralCount(n int, noun string) string {
	word := fmt.Sprintf("%d", n)
	if n >= 0 && n < len(countWords) {
		word = countWords[n]
	}
	if n == 1 {
		return word + " " + noun
	}
	return word + " " + noun + "s"
}
