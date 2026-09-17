// Package voice turns spoken utterances into qi actions against coding-agent
// instances (Claude Code, Codex, ...) discovered through a
// [qi/internal/agentrt.Runtime] and resolved by
// [qi/internal/service.AgentService].
//
// Intent parsing here is deliberately DETERMINISTIC: a hand-written grammar
// over a small set of phrasings, with no LLM anywhere in the package. That is
// invariant #3 ("AI is opt-in and confirmation-gated") applied to the voice
// path: an utterance that the grammar does not recognise becomes
// [IntentUnknown] and the user is asked to rephrase, rather than being handed
// to a model that might guess which agent was meant. Likewise, an utterance
// that names an agent ambiguously produces a clarification question, never a
// guess.
package voice

import (
	"regexp"
	"strings"

	"qi/internal/agentrt"
)

// IntentKind is the top-level action an utterance asks for.
type IntentKind int

const (
	// IntentUnknown means the grammar did not recognise the utterance.
	IntentUnknown IntentKind = iota
	// IntentStatus asks what the agents are doing.
	IntentStatus
	// IntentInstruct sends an instruction to one addressed agent.
	IntentInstruct
	// IntentClarify answers a pending clarification question ("the second
	// one"). It is only actionable while a clarification is outstanding.
	IntentClarify
	// IntentQuit ends the conversation loop.
	IntentQuit
)

// String renders the intent kind for diagnostics and test failures.
func (k IntentKind) String() string {
	switch k {
	case IntentStatus:
		return "status"
	case IntentInstruct:
		return "instruct"
	case IntentClarify:
		return "clarify"
	case IntentQuit:
		return "quit"
	default:
		return "unknown"
	}
}

// Target is who the user addressed, as stated. Every field is optional: an
// empty Target means "some agent, unspecified", which resolution turns into a
// clarification question whenever more than one agent could match.
//
// Target records what was *said*, not what it resolves to. Turning it into a
// [qi/internal/service.AgentQuery] needs conversational context and the
// environment; see [Context.Query].
type Target struct {
	// Kind is the agent type the user named ("claude", "codex"); "" when
	// unspecified.
	Kind agentrt.Kind
	// Workspace is an explicit workspace label ("qi"); "" when unspecified.
	Workspace string
	// Name is an explicit runtime agent name, from "named <x>" / "called <x>".
	Name string
	// ThisWorkspace is set by "in this workspace" / "here".
	ThisWorkspace bool
	// Focused is set by "the agent I'm looking at" / "the focused agent" /
	// "the current agent" / "this agent" / "the agent in front of me".
	Focused bool
	// Ordinal is a 1-based pick from a clarification's candidate list
	// ("the second one"); 0 when absent.
	Ordinal int
	// Pane is a pane reference from a clarification answer ("pane 5-2"). It
	// matches either AgentInstance.PaneLabel() or AgentInstance.PaneID.
	Pane string
	// State is a state-based pick from a clarification answer ("the working
	// one"); "" when absent.
	State agentrt.State
}

// Intent is one parsed utterance.
type Intent struct {
	Kind IntentKind
	// Target is who was addressed (IntentInstruct) or which candidate was
	// chosen (IntentClarify).
	Target Target
	// Instruction is the text to relay to the agent, in the user's own
	// wording and casing (IntentInstruct only).
	Instruction string
	// Raw is the utterance as received, before normalisation.
	Raw string
}

// instructVerbs open an instruction. The verb must be the FIRST word: a
// mid-sentence "have" ("When Codex finishes, have Claude review it") is a
// conditional the grammar deliberately does not understand.
var instructVerbs = map[string]bool{
	"tell":     true,
	"ask":      true,
	"have":     true,
	"get":      true,
	"instruct": true,
}

// statusPhrases are the canonicalised utterances that mean "report on the
// agents". Matching is exact against the canonical form (see canonicalize),
// so the set stays readable and no phrase accidentally swallows an
// instruction.
var statusPhrases = map[string]bool{
	"status":                    true,
	"agent status":              true,
	"agents status":             true,
	"what is the agent status":  true,
	"what are my agents doing":  true,
	"what are the agents doing": true,
	"what are my agents up to":  true,
	"what is everyone doing":    true,
	"what is everybody doing":   true,
	"what agents are working":   true,
	"what agents are running":   true,
	"what agents do i have":     true,
	"who is working":            true,
	"who is busy":               true,
	"list agents":               true,
	"list my agents":            true,
	"show agents":               true,
	"show my agents":            true,
	"how are my agents doing":   true,
}

// quitPhrases end the loop.
var quitPhrases = map[string]bool{
	"quit":           true,
	"exit":           true,
	"stop listening": true,
	"goodbye":        true,
	"bye":            true,
	"good bye":       true,
	"that is all":    true,
	"never mind":     true,
}

// ordinalWords maps a spoken ordinal to its 1-based position.
var ordinalWords = map[string]int{
	"first": 1, "1st": 1, "one": 1, "1": 1,
	"second": 2, "2nd": 2, "two": 2, "2": 2,
	"third": 3, "3rd": 3, "three": 3, "3": 3,
	"fourth": 4, "4th": 4, "four": 4, "4": 4,
	"fifth": 5, "5th": 5, "five": 5, "5": 5,
}

// stateWords maps a spoken state adjective to a runtime state.
var stateWords = map[string]agentrt.State{
	"working": agentrt.StateWorking,
	"busy":    agentrt.StateWorking,
	"idle":    agentrt.StateIdle,
	"free":    agentrt.StateIdle,
	"blocked": agentrt.StateBlocked,
	"stuck":   agentrt.StateBlocked,
	"done":    agentrt.StateDone,
}

// paneRe matches a spoken pane reference: a PaneLabel ("5-3"), a raw runtime
// pane ID ("w9:p3"), or a bare pane ordinal ("3").
var paneRe = regexp.MustCompile(`^(?:[0-9]+-[0-9]+|w[0-9]+:p[0-9]+|[0-9]+)$`)

// Parse turns an utterance into an [Intent]. It never errors: an unrecognised
// utterance is [IntentUnknown], which the loop answers with help rather than a
// guess.
func Parse(utterance string) Intent {
	in := Intent{Kind: IntentUnknown, Raw: utterance}

	toks := tokenize(utterance)
	if len(toks) == 0 {
		return in
	}
	low := lowerTokens(toks)

	if quitPhrases[canonicalize(low)] {
		in.Kind = IntentQuit
		return in
	}
	if statusPhrases[canonicalize(low)] {
		in.Kind = IntentStatus
		return in
	}
	if t, ok := parseClarification(low); ok {
		in.Kind = IntentClarify
		in.Target = t
		return in
	}
	if !instructVerbs[low[0]] {
		// TODO(cross-agent orchestration): conditional/sequenced utterances
		// such as "When Codex finishes, have Claude review its changes."
		// deliberately fall through to IntentUnknown. Recognising them needs a
		// dependency between two agent runs, which qi does not model yet; a
		// partial parse here would silently drop the "when" and send the
		// instruction immediately, which is exactly the guess this package
		// refuses to make.
		return in
	}

	t, n, ok := parseTarget(toks[1:], low[1:])
	if !ok {
		return in
	}
	rest := toks[1+n:]
	restLow := low[1+n:]
	// A second addressee before the instruction ("tell Claude and Codex to
	// ...", "tell Claude then Codex ...") names two agents. qi addresses one
	// instance per instruction, so refuse rather than send the mangled
	// remainder to the first one.
	if namesAnotherAgent(restLow) {
		return in
	}
	// An explicit "to" separates target from instruction ("ask X to run the
	// tests"). Without it the remainder is the instruction verbatim ("ask X
	// whether the build is green").
	// "tell X that I merged the PR" relays "I merged the PR": the "that"
	// belongs to the verb, not the message.
	if len(rest) > 0 && (restLow[0] == "to" || restLow[0] == "that") {
		rest, restLow = rest[1:], restLow[1:]
	}
	for len(rest) > 0 && restLow[0] == "please" {
		rest, restLow = rest[1:], restLow[1:]
	}
	instruction := strings.Join(rest, " ")
	instruction = strings.TrimSpace(strings.TrimRight(instruction, ".!?,;"))
	if instruction == "" {
		// "Tell Claude." names an agent but asks for nothing.
		return in
	}
	in.Kind = IntentInstruct
	in.Target = t
	in.Instruction = instruction
	return in
}

// parseTarget consumes the addressed-agent phrase from the head of toks and
// returns the target plus how many tokens it consumed. ok is false when the
// head names no agent at all, which makes the whole utterance unknown rather
// than an instruction sent to a guessed agent.
//
// toks carries the user's original casing (so "named Reviewer" keeps its
// capital); low is the lowercased parallel slice used for matching.
func parseTarget(toks, low []string) (Target, int, bool) {
	var t Target
	i := 0
	matched := false

	if len(low) == 0 {
		return t, 0, false
	}
	// "this agent" — "this" is not the article, so it is handled first.
	if hasPrefix(low[i:], "this", "agent") {
		t.Focused = true
		return t, i + 2, true
	}
	if low[i] == "the" {
		i++
	}
	if i >= len(low) {
		return t, 0, false
	}
	switch {
	case hasPrefix(low[i:], "focused", "agent"):
		t.Focused = true
		i += 2
		matched = true
	case hasPrefix(low[i:], "current", "agent"):
		t.Focused = true
		i += 2
		matched = true
	case hasPrefix(low[i:], "agent", "in", "front", "of", "me"):
		t.Focused = true
		i += 5
		matched = true
	default:
		// "the idle Claude" / "the working Codex agent": a state word before
		// the kind narrows to agents in that state (softly, see
		// service.AgentQuery.State).
		if st, ok := stateWords[low[i]]; ok && i+1 < len(low) {
			if _, n := matchKind(low[i+1:]); n > 0 {
				t.State = st
				i++
			}
		}
		if k, n := matchKind(low[i:]); n > 0 {
			t.Kind = k
			i += n
			matched = true
		}
		if i < len(low) && low[i] == "agent" {
			i++
			matched = true
			// "the agent named reviewer" / "called reviewer"
			if i+1 < len(low) && (low[i] == "named" || low[i] == "called") {
				t.Name = strings.Trim(toks[i+1], ",.")
				i += 2
			}
		}
		// "Claude working in qi" / "Claude that's idle" / "Claude currently
		// blocked": a state qualifier after the kind.
		if matched {
			if st, n := matchStateQualifier(low[i:]); n > 0 {
				t.State = st
				i += n
			}
		}
		// "... I'm looking at" attaches to whatever was just named, and
		// means the focused agent (optionally constrained by kind).
		if n := matchLookingAt(low[i:]); n > 0 {
			t.Focused = true
			i += n
			matched = true
		}
	}
	if !matched {
		return t, 0, false
	}

	// Optional workspace clause.
	if i < len(low) && low[i] == "here" {
		t.ThisWorkspace = true
		i++
	} else if i < len(low) && low[i] == "in" {
		if ws, this, n := matchWorkspace(toks[i:], low[i:]); n > 0 {
			t.Workspace = ws
			t.ThisWorkspace = this
			i += n
		}
	}
	return t, i, true
}

// namesAnotherAgent reports whether the residual after a parsed target
// starts with a conjunction or mentions a further agent kind before the
// "to" that introduces the instruction. Kinds after "to" are fine: "tell
// Claude to ask Codex for the diff" is one instruction to Claude.
func namesAnotherAgent(restLow []string) bool {
	if len(restLow) == 0 {
		return false
	}
	switch restLow[0] {
	case "and", "&", "plus", "then", "or":
		return true
	}
	for j := 0; j < len(restLow) && restLow[j] != "to" && restLow[j] != "that"; j++ {
		if _, n := matchKind(restLow[j:]); n > 0 {
			return true
		}
	}
	return false
}

// matchKind matches an agent-kind phrase at the head of low and returns the
// kind plus the tokens consumed.
//
// Only the kinds qi can name are accepted. agentrt.Kind is an open string, but
// the grammar cannot treat an arbitrary word as a kind: "Tell everyone the
// build is broken" would otherwise address an agent called "everyone" and
// relay half a sentence.
func matchKind(low []string) (agentrt.Kind, int) {
	if len(low) == 0 {
		return "", 0
	}
	if hasPrefix(low, "claude", "code") {
		return agentrt.KindClaude, 2
	}
	switch low[0] {
	case "claude", "claude-code":
		return agentrt.KindClaude, 1
	case "codex":
		return agentrt.KindCodex, 1
	}
	return "", 0
}

// matchLookingAt matches "I'm looking at" / "I am looking at".
func matchLookingAt(low []string) int {
	if hasPrefix(low, "i'm", "looking", "at") {
		return 3
	}
	if hasPrefix(low, "i", "am", "looking", "at") {
		return 4
	}
	return 0
}

// matchWorkspace consumes a workspace clause beginning at "in": "in the qi
// workspace", "in qi", "in this workspace". It returns the label, whether the
// clause meant the caller's own workspace, and the tokens consumed (0 when the
// "in" turned out to start the instruction instead).
func matchWorkspace(toks, low []string) (label string, thisWS bool, n int) {
	j := 1 // skip "in"
	if j < len(low) && low[j] == "the" {
		j++
	}
	if j >= len(low) {
		return "", false, 0
	}
	if low[j] == "this" && j+1 < len(low) && low[j+1] == "workspace" {
		return "", true, j + 2
	}
	// "in the qi workspace" / "in the ai map workspace": everything up to an
	// explicit "workspace" is the label.
	for k := j; k < len(low) && k < j+3; k++ {
		if low[k] == "workspace" {
			if k == j {
				return "", false, 0
			}
			return strings.Join(low[j:k], " "), false, k + 1
		}
	}
	// "in qi to run the tests": a single bare token is the label ONLY when
	// the instruction separator "to" follows it (or the utterance ends).
	// Without that evidence the "in ..." phrase is left to the instruction:
	// "tell Claude in qi and Codex in ai-map ..." must not consume "qi" and
	// relay the rest, and "ask Claude in which file the bug is" is a question
	// to Claude, not a workspace called "which".
	if low[j] == "to" {
		return "", false, 0
	}
	if j+1 < len(low) && low[j+1] != "to" && low[j+1] != "that" {
		return "", false, 0
	}
	return strings.Trim(low[j], ",."), false, j + 1
}

// parseClarification matches an answer to a pending clarification question.
// These are short, self-contained utterances, so matching is over the whole
// token list; a longer sentence is left to the instruction grammar.
func parseClarification(low []string) (Target, bool) {
	c := trimLeading(low, "the")
	if len(c) == 0 {
		return Target{}, false
	}
	// "pane 5-2" / "the one in pane 5-2" / "in pane 5-2"
	if p, ok := matchPanePhrase(c); ok {
		return Target{Pane: p}, true
	}
	// "number one", "the second one", "first"
	c = trimLeading(c, "number")
	if len(c) == 0 {
		return Target{}, false
	}
	if n, ok := ordinalWords[c[0]]; ok {
		rest := c[1:]
		if len(rest) == 0 || (len(rest) == 1 && rest[0] == "one") {
			return Target{Ordinal: n}, true
		}
		// Not a bare ordinal ("one that is working"): fall through to the
		// remaining clarification shapes.
	}
	// "the working one" / "the one that is working" / "the one that's idle"
	// / "the one which is done" / bare "idle"
	if len(c) == 2 && c[1] == "one" {
		if s, ok := stateWords[c[0]]; ok {
			return Target{State: s}, true
		}
	}
	if len(c) == 1 {
		if s, ok := stateWords[c[0]]; ok {
			return Target{State: s}, true
		}
	}
	if len(c) >= 2 && c[0] == "one" {
		if st, n := matchStateQualifier(c[1:]); n > 0 && 1+n == len(c) {
			return Target{State: st}, true
		}
		// "the one in qi" / "the one in the qi workspace"
		if c[1] == "in" {
			if ws, this, n := matchWorkspaceAnswer(c[1:]); n > 0 && 1+n == len(c) {
				return Target{Workspace: ws, ThisWorkspace: this}, true
			}
		}
	}
	// "the qi one" / "the ai map one"
	if len(c) >= 2 && len(c) <= 4 && c[len(c)-1] == "one" {
		label := strings.Join(c[:len(c)-1], " ")
		if _, isState := stateWords[c[0]]; !isState {
			if _, isOrd := ordinalWords[c[0]]; !isOrd {
				return Target{Workspace: label}, true
			}
		}
	}
	// "in qi" / "in the qi workspace"
	if c[0] == "in" {
		if ws, this, n := matchWorkspaceAnswer(c); n > 0 && n == len(c) {
			return Target{Workspace: ws, ThisWorkspace: this}, true
		}
	}
	return Target{}, false
}

// matchWorkspaceAnswer parses an "in [the] <label...> [workspace]" phrase at
// the head of low for a clarification answer, where the whole remainder is
// the label (no instruction follows). Returns the label, whether it was
// "this workspace", and the tokens consumed (0 when none).
func matchWorkspaceAnswer(low []string) (label string, thisWS bool, n int) {
	if len(low) < 2 || low[0] != "in" {
		return "", false, 0
	}
	j := 1
	if low[j] == "the" {
		j++
	}
	if j >= len(low) {
		return "", false, 0
	}
	if low[j] == "this" && j+1 < len(low) && low[j+1] == "workspace" {
		return "", true, j + 2
	}
	end := len(low)
	if low[end-1] == "workspace" {
		end--
	}
	if end <= j {
		return "", false, 0
	}
	return strings.Join(low[j:end], " "), false, len(low)
}

// matchStateQualifier matches "[that is|that's|who is|who's|which is|
// currently|now] <state>" at the head of low and returns the state plus the
// tokens consumed.
func matchStateQualifier(low []string) (agentrt.State, int) {
	i := 0
	switch {
	case hasPrefix(low, "that", "is"), hasPrefix(low, "who", "is"), hasPrefix(low, "which", "is"):
		i = 2
	case len(low) > 0 && (low[0] == "that's" || low[0] == "who's" || low[0] == "which's"):
		i = 1
	}
	if i < len(low) && (low[i] == "currently" || low[i] == "now") {
		i++
	}
	if i < len(low) {
		if st, ok := stateWords[low[i]]; ok {
			return st, i + 1
		}
	}
	return "", 0
}

// matchPanePhrase matches a pane reference, with or without the "the one in"
// preamble.
func matchPanePhrase(low []string) (string, bool) {
	c := low
	if hasPrefix(c, "one", "in") {
		c = c[2:]
	} else if len(c) > 0 && c[0] == "in" {
		c = c[1:]
	}
	c = trimLeading(c, "the")
	if len(c) == 2 && c[0] == "pane" && paneRe.MatchString(c[1]) {
		return c[1], true
	}
	return "", false
}

// tokenize normalises an utterance into whitespace-separated tokens, dropping
// wake words and trailing sentence punctuation. Token casing is preserved so
// an instruction can be reassembled in the user's own words.
func tokenize(utterance string) []string {
	s := strings.TrimSpace(utterance)
	s = strings.TrimRight(s, " .!?,;")
	toks := strings.Fields(s)
	toks = dropWakeWords(toks)
	return toks
}

// dropWakeWords removes a leading "hey qi" / "qi," / "ok qi" / "please".
func dropWakeWords(toks []string) []string {
	for len(toks) > 0 {
		w := strings.Trim(strings.ToLower(toks[0]), ",.")
		switch w {
		case "hey", "ok", "okay", "hi", "hello", "please":
			toks = toks[1:]
			continue
		case "qi":
			// Only a leading vocative; "qi" elsewhere is a workspace label.
			toks = toks[1:]
			continue
		}
		return toks
	}
	return toks
}

func lowerTokens(toks []string) []string {
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = strings.Trim(strings.ToLower(t), ",.!?;")
	}
	return out
}

// canonicalize renders lowered tokens as one string with contractions
// expanded and filler words dropped, for exact matching against the status
// and quit phrase sets.
func canonicalize(low []string) string {
	out := make([]string, 0, len(low))
	for _, t := range low {
		switch t {
		case "currently", "right", "now", "please", "just":
			// Filler: "what agents are currently working", "... right now".
			continue
		}
		out = append(out, expandContraction(t))
	}
	return strings.Join(out, " ")
}

// expandContraction normalises the contractions the phrase sets would
// otherwise have to list twice.
func expandContraction(t string) string {
	switch t {
	case "what's":
		return "what is"
	case "who's":
		return "who is"
	case "i'm":
		return "i am"
	case "everyone's":
		return "everyone is"
	case "that's":
		return "that is"
	}
	return t
}

func hasPrefix(low []string, words ...string) bool {
	if len(low) < len(words) {
		return false
	}
	for i, w := range words {
		if low[i] != w {
			return false
		}
	}
	return true
}

func trimLeading(low []string, word string) []string {
	if len(low) > 0 && low[0] == word {
		return low[1:]
	}
	return low
}
