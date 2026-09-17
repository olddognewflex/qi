package service

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"qi/internal/agentrt"
)

// AgentService is qi's side of the agent-runtime boundary. The runtime owns
// *where* agents are and *what state* they are in; this service owns *who the
// user means* and *what happens next*.
//
// The central rule: when a request matches more than one live agent, Resolve
// never guesses. It returns an *AmbiguousAgentError carrying a speakable
// clarification question, because two Claude Code instances in one workspace
// are two different agents doing two different things.
type AgentService struct {
	rt agentrt.Runtime
}

// NewAgentService builds an AgentService over rt.
func NewAgentService(rt agentrt.Runtime) *AgentService {
	return &AgentService{rt: rt}
}

// Runtime exposes the underlying runtime for callers that need a capability
// this service does not wrap (e.g. raw output reads).
func (s *AgentService) Runtime() agentrt.Runtime { return s.rt }

// AgentQuery describes who the user means. Every field is a filter and an
// empty field is a wildcard, so the zero AgentQuery matches every live agent.
//
// Matching rules (all applied together, i.e. AND):
//
//   - Kind:      case-insensitive equality against AgentInstance.Kind.
//   - Workspace: case-insensitive equality against Workspace.Label, OR exact
//     equality against Workspace.ID (so "qi", "QI" and "w9" all work).
//   - Name:      exact equality against the runtime-assigned agent name.
//   - Cwd:       path-boundary-aware containment either way: the query dir may
//     be the agent's dir, inside it, or an ancestor of it. The agent's own Cwd
//     is used, falling back to its workspace Cwd.
//   - Focused:   restricts to the agent in the UI-focused pane. Combined with
//     Kind/Workspace it fails cleanly ("the Codex I'm looking at" when the
//     focused agent is Claude) rather than silently widening.
//   - ID:        an explicit runtime handle. It wins over every other field and
//     is resolved straight through the runtime.
type AgentQuery struct {
	Kind      agentrt.Kind
	Workspace string
	Name      string
	ID        agentrt.AgentID
	Focused   bool
	Cwd       string

	// PreferID is a *soft* handle from conversational context ("Have Claude
	// review that" → the Claude we were just talking to). Unlike ID it never
	// bypasses the filters: the preferred agent wins only if it is still among
	// the candidates matching Kind/Workspace/... and, when PreferSessionID is
	// set, still carries that session identity. A pane whose occupant changed
	// (Claude exited, Codex started there) therefore falls back to the normal
	// ask-when-ambiguous path instead of receiving someone else's follow-up.
	PreferID        agentrt.AgentID
	PreferSessionID string

	// State is a *soft* lifecycle filter from speech ("the Claude working in
	// qi"). It narrows when it can; when no candidate is in that state the
	// filter is dropped rather than failing, because people use "working"
	// loosely for "the one over there" and the state may have changed
	// between their glance and their sentence.
	State agentrt.State
}

// isEmpty reports whether the query constrains nothing at all.
func (q AgentQuery) isEmpty() bool {
	return q.Kind == "" && q.Workspace == "" && q.Name == "" && q.ID == "" && !q.Focused && q.Cwd == ""
}

// AmbiguousAgentError reports that a query matched several live agents. qi
// asks rather than picking: Candidates is the full match set in List order.
type AmbiguousAgentError struct {
	Query      AgentQuery
	Candidates []agentrt.AgentInstance
}

func (e *AmbiguousAgentError) Error() string {
	descs := make([]string, 0, len(e.Candidates))
	for _, a := range e.Candidates {
		descs = append(descs, DescribeAgent(a))
	}
	return fmt.Sprintf("ambiguous agent query: %d candidates: %s", len(e.Candidates), strings.Join(descs, "; "))
}

// Prompt renders the clarification question to put to the user. It is written
// to be spoken aloud: number words for small counts, pane labels rather than
// raw pane IDs, and one or two sentences.
//
//	"I have two Claude agents in the qi workspace. The first is working in
//	 pane 5-1 and the second is idle in pane 5-2. Which one do you mean?"
//
// When the candidates span workspaces the workspace moves into each clause
// ("... in pane 5-1 in the qi workspace and ... in pane 6-1 in the ai-map
// workspace"); when they span kinds each clause names its kind.
func (e *AmbiguousAgentError) Prompt() string {
	n := len(e.Candidates)
	switch n {
	case 0:
		return "I'm not sure which agent you mean."
	case 1:
		a := e.Candidates[0]
		return fmt.Sprintf("Do you mean %s?", DescribeAgent(a))
	}

	sameKind := true
	sameWorkspace := true
	kind := e.Candidates[0].Kind
	ws := workspaceName(e.Candidates[0].Workspace)
	for _, a := range e.Candidates[1:] {
		if a.Kind != kind {
			sameKind = false
		}
		if workspaceName(a.Workspace) != ws {
			sameWorkspace = false
		}
	}
	// An unknown kind or an unlabelled workspace cannot carry the shared
	// phrasing, so fall back to the per-clause form.
	if kind == "" {
		sameKind = false
	}
	if ws == "" {
		sameWorkspace = false
	}

	var lead strings.Builder
	lead.WriteString("I have ")
	lead.WriteString(countWord(n))
	if sameKind {
		lead.WriteString(" ")
		lead.WriteString(e.Candidates[0].DisplayKind())
	}
	lead.WriteString(" agents")
	if sameWorkspace {
		lead.WriteString(" in the ")
		lead.WriteString(ws)
		lead.WriteString(" workspace")
	}
	lead.WriteString(".")

	clauses := make([]string, 0, n)
	for i, a := range e.Candidates {
		var c strings.Builder
		c.WriteString("the ")
		c.WriteString(ordinalWord(i + 1))
		c.WriteString(" is ")
		if !sameKind {
			c.WriteString(a.DisplayKind())
			c.WriteString(", ")
		}
		c.WriteString(statePhrase(a.State))
		if label := a.PaneLabel(); label != "" {
			c.WriteString(" in pane ")
			c.WriteString(label)
		}
		if !sameWorkspace {
			if w := workspaceName(a.Workspace); w != "" {
				c.WriteString(" in the ")
				c.WriteString(w)
				c.WriteString(" workspace")
			}
		}
		clauses = append(clauses, c.String())
	}

	return lead.String() + " " + capitalize(joinClauses(clauses)) + ". Which one do you mean?"
}

// NoAgentError reports that nothing live matched the query. Its message is
// phrased to be spoken back to the user.
type NoAgentError struct {
	Query AgentQuery
}

func (e *NoAgentError) Error() string {
	q := e.Query

	// "The agent I'm looking at" gets its own phrasing: the pane, not the
	// vault, is what came up empty.
	if q.Focused && q.Kind == "" && q.Workspace == "" && q.Name == "" && q.Cwd == "" {
		return "No agent is in the focused pane."
	}
	if q.isEmpty() {
		return "I can't find any agents."
	}

	var b strings.Builder
	b.WriteString("I can't find ")
	if q.Kind != "" {
		b.WriteString(indefiniteArticle(displayKind(q.Kind)))
		b.WriteString(" ")
		b.WriteString(displayKind(q.Kind))
		b.WriteString(" agent")
	} else {
		b.WriteString("an agent")
	}
	if q.Name != "" {
		b.WriteString(" named ")
		b.WriteString(q.Name)
	}
	if q.Workspace != "" {
		b.WriteString(" in the ")
		b.WriteString(q.Workspace)
		b.WriteString(" workspace")
	}
	if q.Cwd != "" {
		b.WriteString(" working in ")
		b.WriteString(filepath.Clean(q.Cwd))
	}
	if q.Focused {
		b.WriteString(" in the focused pane")
	}
	if q.ID != "" && q.Kind == "" && q.Name == "" && q.Workspace == "" && q.Cwd == "" && !q.Focused {
		b.WriteString(" with the handle ")
		b.WriteString(string(q.ID))
	}
	b.WriteString(".")
	return b.String()
}

// Unwrap lets callers branch on the runtime's vocabulary: a query that
// resolved to nothing is a not-found condition.
func (e *NoAgentError) Unwrap() error { return agentrt.ErrAgentNotFound }

// List returns every live agent, sorted by workspace number, then workspace
// label, then pane ID, so repeated calls render in a stable order.
func (s *AgentService) List(ctx context.Context) ([]agentrt.AgentInstance, error) {
	agents, err := s.rt.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(agents, func(i, j int) bool {
		a, b := agents[i], agents[j]
		if a.Workspace.Number != b.Workspace.Number {
			return a.Workspace.Number < b.Workspace.Number
		}
		if a.Workspace.Label != b.Workspace.Label {
			return a.Workspace.Label < b.Workspace.Label
		}
		return a.PaneID < b.PaneID
	})
	return agents, nil
}

// Workspaces returns the runtime's workspaces sorted by number, then label.
func (s *AgentService) Workspaces(ctx context.Context) ([]agentrt.Workspace, error) {
	wss, err := s.rt.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(wss, func(i, j int) bool {
		if wss[i].Number != wss[j].Number {
			return wss[i].Number < wss[j].Number
		}
		return wss[i].Label < wss[j].Label
	})
	return wss, nil
}

// Resolve narrows the live agents by q and insists on exactly one answer.
// Zero matches yield *NoAgentError, several yield *AmbiguousAgentError. It
// never picks a candidate on the user's behalf.
func (s *AgentService) Resolve(ctx context.Context, q AgentQuery) (agentrt.AgentInstance, error) {
	// An explicit runtime handle is already unambiguous; ask the runtime
	// directly so a stale handle surfaces as ErrAgentNotFound.
	if q.ID != "" {
		return s.rt.GetAgent(ctx, q.ID)
	}

	var focusID agentrt.AgentID
	if q.Focused {
		focus, err := s.rt.Focused(ctx)
		if err != nil {
			return agentrt.AgentInstance{}, err
		}
		if focus.AgentID == "" {
			return agentrt.AgentInstance{}, &NoAgentError{Query: q}
		}
		focusID = focus.AgentID
	}

	agents, err := s.List(ctx)
	if err != nil {
		return agentrt.AgentInstance{}, err
	}

	filter := func(withState bool) []agentrt.AgentInstance {
		out := make([]agentrt.AgentInstance, 0, len(agents))
		for _, a := range agents {
			if q.Focused && a.ID != focusID {
				continue
			}
			if !matchesQuery(a, q) {
				continue
			}
			if withState && q.State != "" && a.State != q.State {
				continue
			}
			out = append(out, a)
		}
		return out
	}
	candidates := filter(true)
	if len(candidates) == 0 && q.State != "" {
		candidates = filter(false)
	}

	if q.PreferID != "" {
		for _, a := range candidates {
			if a.ID == q.PreferID && (q.PreferSessionID == "" || a.SessionID == q.PreferSessionID) {
				return a, nil
			}
		}
		// The remembered instance is gone or changed; continue as if the
		// user had named only the hard constraints.
	}

	switch len(candidates) {
	case 0:
		return agentrt.AgentInstance{}, &NoAgentError{Query: q}
	case 1:
		return candidates[0], nil
	default:
		return agentrt.AgentInstance{}, &AmbiguousAgentError{Query: q, Candidates: candidates}
	}
}

// Instruct sends text to the agent as a prompt. The runtime's error is
// returned unchanged so callers can branch with errors.Is on
// agentrt.ErrAgentBlocked and friends.
func (s *AgentService) Instruct(ctx context.Context, id agentrt.AgentID, text string) error {
	return s.rt.Send(ctx, id, text)
}

// AgentResult is what an agent settled on plus the tail of its output.
type AgentResult struct {
	State  agentrt.State
	Output string
}

// AwaitResult blocks until the agent settles (the runtime's default wait set,
// agentrt.SettledStates: idle, done, or blocked on a human) and then reads the
// tail of its terminal output. lines of 0 means the default of 40. The ctx
// deadline governs the wait, so a caller bounds the whole call by bounding ctx.
func (s *AgentService) AwaitResult(ctx context.Context, id agentrt.AgentID, lines int) (AgentResult, error) {
	state, err := s.rt.WaitForState(ctx, id)
	if err != nil {
		return AgentResult{}, err
	}
	if lines <= 0 {
		lines = defaultOutputLines
	}
	// The caller's deadline bounds the *wait*. Once the agent has settled,
	// reading its output is a quick local snapshot that must not fail just
	// because the wait consumed the budget — otherwise a finish that lands
	// near the deadline is reported as a timeout. Detach from the deadline
	// (but not from cancellation intent: a hard cancel still stops us via
	// the short read timeout) and give the read its own small budget.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readOutputTimeout)
	defer cancel()
	out, err := s.rt.ReadOutput(rctx, id, agentrt.OutputOptions{Lines: lines, Source: agentrt.SourceRecentUnwrapped})
	if err != nil {
		// The wait succeeded, so keep the state the caller paid for.
		return AgentResult{State: state}, err
	}
	return AgentResult{State: state, Output: out.Text}, nil
}

// readOutputTimeout bounds the post-settle output read in AwaitResult.
const readOutputTimeout = 15 * time.Second

// defaultOutputLines is how much transcript AwaitResult reads when the caller
// does not say.
const defaultOutputLines = 40

// DescribeAgent renders one agent for a human: "Codex in qi, pane 1-3", or
// "Codex (reviewer) in qi, pane 1-3" when the runtime assigned it a name.
func DescribeAgent(a agentrt.AgentInstance) string {
	var b strings.Builder
	b.WriteString(a.DisplayKind())
	if a.Name != "" {
		b.WriteString(" (")
		b.WriteString(a.Name)
		b.WriteString(")")
	}
	if ws := workspaceName(a.Workspace); ws != "" {
		b.WriteString(" in ")
		b.WriteString(ws)
	}
	if label := a.PaneLabel(); label != "" {
		b.WriteString(", pane ")
		b.WriteString(label)
	}
	return b.String()
}

// SummarizeAgents renders one speakable sentence per agent, in the order
// given (List order at the call site):
//
//	"Claude is working on qi."   "Codex is blocked on handyman."
//
// When two agents of the same kind share a workspace the pane is added, since
// "Claude is working on qi" twice tells the user nothing:
//
//	"Claude in pane 5-1 is working on qi."
//
// An empty slice reports that plainly rather than returning nothing.
func SummarizeAgents(agents []agentrt.AgentInstance) []string {
	if len(agents) == 0 {
		return []string{"No agents are running."}
	}

	type slot struct {
		kind agentrt.Kind
		ws   string
	}
	counts := make(map[slot]int, len(agents))
	for _, a := range agents {
		counts[slot{a.Kind, workspaceName(a.Workspace)}]++
	}

	lines := make([]string, 0, len(agents))
	for _, a := range agents {
		var b strings.Builder
		b.WriteString(a.DisplayKind())
		if counts[slot{a.Kind, workspaceName(a.Workspace)}] > 1 {
			if label := a.PaneLabel(); label != "" {
				b.WriteString(" in pane ")
				b.WriteString(label)
			}
		}
		b.WriteString(" is ")
		b.WriteString(statePhrase(a.State))
		if ws := workspaceName(a.Workspace); ws != "" {
			b.WriteString(" on ")
			b.WriteString(ws)
		}
		b.WriteString(".")
		lines = append(lines, b.String())
	}
	return lines
}

// ParseKind maps what a user says or types onto a runtime Kind. Unrecognised
// input is lowercased and passed through: Kind is deliberately open, and a
// runtime may know a kind qi has never heard of.
func ParseKind(s string) agentrt.Kind {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "":
		return ""
	case "claude", "claude code", "claude-code", "claudecode", "claude_code":
		return agentrt.KindClaude
	case "codex", "openai codex", "openai-codex", "opencodex":
		return agentrt.KindCodex
	}
	return agentrt.Kind(s)
}

// matchesQuery applies the non-focus, non-ID filters of q to one agent.
func matchesQuery(a agentrt.AgentInstance, q AgentQuery) bool {
	if q.Kind != "" && !strings.EqualFold(string(a.Kind), string(q.Kind)) {
		return false
	}
	if q.Workspace != "" && !matchesWorkspace(a.Workspace, q.Workspace) {
		return false
	}
	if q.Name != "" && a.Name != q.Name {
		return false
	}
	if q.Cwd != "" && !matchesCwd(a, q.Cwd) {
		return false
	}
	return true
}

// matchesWorkspace accepts the human label case-insensitively or the runtime
// handle exactly: the user says "qi", a script passes "w9".
// WorkspaceMatches reports whether a spoken or typed workspace reference
// names ws: its id, or its label case-insensitively with separators folded.
func WorkspaceMatches(ws agentrt.Workspace, want string) bool { return matchesWorkspace(ws, want) }

func matchesWorkspace(ws agentrt.Workspace, want string) bool {
	if ws.Label != "" && (strings.EqualFold(ws.Label, want) || foldLabel(ws.Label) == foldLabel(want)) {
		return true
	}
	return ws.ID != "" && ws.ID == want
}

// foldLabel normalises a workspace label for spoken matching: speech-to-text
// renders "ai-map" as "ai map" (or "ai_map"), so case and the separator
// characters are ignored. Only used after an exact case-insensitive miss.
func foldLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '-', '_', '.':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// matchesCwd compares the query directory against the agent's directory
// (falling back to its workspace's). Containment counts in both directions:
// asking from a subdirectory of the agent's tree still means "this one", and
// naming a parent directory still selects the agents inside it.
func matchesCwd(a agentrt.AgentInstance, want string) bool {
	dir := a.Cwd
	if dir == "" {
		dir = a.Workspace.Cwd
	}
	if dir == "" {
		return false
	}
	dir = filepath.Clean(dir)
	want = filepath.Clean(want)
	return dir == want || pathWithin(want, dir) || pathWithin(dir, want)
}

// pathWithin reports whether child lies under parent, respecting path
// boundaries: "/a/b/c" is within "/a/b", but "/a/bc" is not. Both arguments
// must already be cleaned.
func pathWithin(child, parent string) bool {
	if parent == "" || child == "" {
		return false
	}
	if child == parent {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(parent, sep) {
		parent += sep
	}
	return strings.HasPrefix(child, parent)
}

// workspaceName is the workspace's human label, falling back to its runtime
// handle so an unlabelled workspace is still nameable out loud.
func workspaceName(ws agentrt.Workspace) string {
	if ws.Label != "" {
		return ws.Label
	}
	return ws.ID
}

// statePhrase renders a State as a predicate: "... is <phrase>".
func statePhrase(s agentrt.State) string {
	switch s {
	case agentrt.StateWorking:
		return "working"
	case agentrt.StateBlocked:
		return "blocked"
	case agentrt.StateDone:
		return "done"
	case agentrt.StateIdle:
		return "idle"
	case agentrt.StateUnknown, "":
		return "in an unknown state"
	}
	return string(s)
}

// displayKind renders a bare Kind the way AgentInstance.DisplayKind renders an
// instance's, for error messages that have no instance to hand.
func displayKind(k agentrt.Kind) string {
	return agentrt.AgentInstance{Kind: k}.DisplayKind()
}

func indefiniteArticle(word string) string {
	if word == "" {
		return "a"
	}
	switch strings.ToLower(word[:1]) {
	case "a", "e", "i", "o", "u":
		return "an"
	}
	return "a"
}

var countWords = [...]string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}

// countWord spells out small counts, which read better aloud; larger counts
// stay as digits.
func countWord(n int) string {
	if n >= 0 && n < len(countWords) {
		return countWords[n]
	}
	return strconv.Itoa(n)
}

var ordinalWords = [...]string{"first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth", "tenth"}

// ordinalWord spells out 1..10 and falls back to "11th" style beyond, which
// no realistic candidate set reaches.
func ordinalWord(n int) string {
	if n >= 1 && n <= len(ordinalWords) {
		return ordinalWords[n-1]
	}
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

// joinClauses renders "A and B" for two, "A, B and C" for more.
func joinClauses(cs []string) string {
	switch len(cs) {
	case 0:
		return ""
	case 1:
		return cs[0]
	case 2:
		return cs[0] + " and " + cs[1]
	}
	return strings.Join(cs[:len(cs)-1], ", ") + " and " + cs[len(cs)-1]
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
