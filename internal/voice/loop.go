package voice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"qi/internal/agentrt"
	"qi/internal/service"
)

// ErrQuit is returned by [Loop.HandleUtterance] when the user ended the
// conversation. [Loop.Run] treats it as a clean exit.
var ErrQuit = errors.New("voice: conversation ended")

// helpReply is what an unrecognised utterance gets. It names the two things
// the grammar understands rather than apologising vaguely.
const helpReply = "Sorry, I didn't understand that. You can ask what your agents are doing, or tell an agent to do something."

// DefaultWaitTimeout bounds how long one instruction waits for its agent to
// settle before the loop hands control back to the user.
const DefaultWaitTimeout = 5 * time.Minute

// DefaultOutputLines is how much of an agent's transcript is read back to
// summarise a finished turn.
const DefaultOutputLines = 40

// Options configures a [Loop].
type Options struct {
	// WaitTimeout bounds the wait for an instructed agent to settle;
	// 0 means DefaultWaitTimeout.
	WaitTimeout time.Duration
	// OutputLines is how many trailing transcript lines to read;
	// 0 means DefaultOutputLines.
	OutputLines int
	// Env is where qi is running, used to resolve "in this workspace".
	Env Env
	// Log, when non-nil, receives every reply as a plain line. Replies are
	// spoken either way; this is for a visible transcript alongside audio.
	Log io.Writer
}

// Loop is one voice conversation: listen, parse, resolve, act, reply.
//
// Nothing in the loop is generative. Utterances are parsed by the grammar in
// intent.go, agents are chosen by [service.AgentService] (which asks rather
// than guesses), and replies are assembled from fixed sentences plus the
// runtime's own words. An LLM summary of an agent's output is a plausible
// future opt-in; it is not called from here.
type Loop struct {
	svc   *service.AgentService
	in    Transcriber
	out   Speaker
	convo Context
	opts  Options
}

// NewLoop builds a loop over svc, reading from in and replying through out.
func NewLoop(svc *service.AgentService, in Transcriber, out Speaker, opts Options) *Loop {
	if opts.WaitTimeout <= 0 {
		opts.WaitTimeout = DefaultWaitTimeout
	}
	if opts.OutputLines <= 0 {
		opts.OutputLines = DefaultOutputLines
	}
	return &Loop{svc: svc, in: in, out: out, opts: opts}
}

// Context exposes the conversational memory, for a caller that wants to seed
// or inspect it.
func (l *Loop) Context() *Context { return &l.convo }

// Run listens until the user quits, the input ends, or ctx is cancelled.
//
// It returns nil for a clean end (a quit utterance or io.EOF) and ctx.Err()
// for a cancellation. A failure handling one utterance does not end the
// session: it has already been spoken back to the user, and the next utterance
// may well work.
func (l *Loop) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		utterance, err := l.in.Listen(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if _, err := l.HandleUtterance(ctx, utterance); err != nil {
			if errors.Is(err, ErrQuit) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Already spoken; keep listening.
			continue
		}
	}
}

// HandleUtterance processes one utterance: it speaks its replies through the
// loop's speaker and also returns them, so commands and tests can drive a
// whole conversation without audio.
func (l *Loop) HandleUtterance(ctx context.Context, utterance string) ([]string, error) {
	in := Parse(utterance)
	switch in.Kind {
	case IntentQuit:
		replies, err := l.say(ctx, "Goodbye.")
		if err != nil {
			return replies, err
		}
		return replies, ErrQuit

	case IntentStatus:
		l.dropPending()
		return l.status(ctx)

	case IntentClarify:
		return l.clarify(ctx, in.Target)

	case IntentInstruct:
		// A fresh instruction supersedes an unanswered question: the user
		// moved on, and answering the old one later would send the wrong
		// text to the wrong agent.
		l.dropPending()
		return l.instruct(ctx, in.Target, in.Instruction)

	default:
		return l.say(ctx, helpReply)
	}
}

// status reports what every live agent is doing.
func (l *Loop) status(ctx context.Context) ([]string, error) {
	agents, err := l.svc.List(ctx)
	if err != nil {
		replies, serr := l.say(ctx, "I can't reach the agent runtime right now.")
		if serr != nil {
			return replies, serr
		}
		return replies, err
	}
	return l.say(ctx, service.SummarizeAgents(agents)...)
}

// clarify applies an answer to the pending question and then carries out the
// instruction that was waiting on it.
func (l *Loop) clarify(ctx context.Context, t Target) ([]string, error) {
	if l.convo.Pending == nil {
		return l.say(ctx, "I'm not waiting on a choice.")
	}
	// Read the deferred instruction before Choose clears it.
	pending := l.convo.PendingIntent
	agent, err := l.convo.Choose(t)
	if err != nil {
		return l.say(ctx, capitalizeFirst(err.Error())+".")
	}
	instruction := ""
	if pending != nil {
		instruction = pending.Instruction
	}
	if instruction == "" {
		l.convo.LastAgent = &agent
		return l.say(ctx, fmt.Sprintf("Okay, %s.", service.DescribeAgent(agent)))
	}
	return l.send(ctx, agent, instruction)
}

// instruct resolves who was addressed and relays the instruction.
func (l *Loop) instruct(ctx context.Context, t Target, instruction string) ([]string, error) {
	q := l.convo.Query(t, l.opts.Env)
	agent, err := l.svc.Resolve(ctx, q)
	if err != nil {
		var amb *service.AmbiguousAgentError
		if errors.As(err, &amb) {
			// Hold the instruction until the user says which agent they
			// meant. This is the whole point of the package: two Claudes in
			// one workspace are two agents, and picking one would be a guess.
			l.convo.Pending = amb
			held := Intent{Kind: IntentInstruct, Target: t, Instruction: instruction}
			l.convo.PendingIntent = &held
			return l.say(ctx, amb.Prompt())
		}
		var none *service.NoAgentError
		if errors.As(err, &none) {
			return l.say(ctx, none.Error())
		}
		replies, serr := l.say(ctx, "I can't reach the agent runtime right now.")
		if serr != nil {
			return replies, serr
		}
		return replies, err
	}
	return l.send(ctx, agent, instruction)
}

// send relays the instruction to a resolved agent and reports what came back.
func (l *Loop) send(ctx context.Context, agent agentrt.AgentInstance, instruction string) ([]string, error) {
	replies, err := l.say(ctx, fmt.Sprintf("Found %s. Sending the request.", service.DescribeAgent(agent)))
	if err != nil {
		return replies, err
	}

	text := l.convo.Expand(instruction)
	if err := l.svc.Instruct(ctx, agent.ID, text); err != nil {
		if errors.Is(err, agentrt.ErrAgentBlocked) {
			more, serr := l.say(ctx, fmt.Sprintf("%s in %s is blocked on an approval or question. Please handle it in pane %s first.",
				agent.DisplayKind(), workspaceName(agent), agent.PaneLabel()))
			return append(replies, more...), serr
		}
		more, serr := l.say(ctx, fmt.Sprintf("I couldn't send that to %s.", service.DescribeAgent(agent)))
		replies = append(replies, more...)
		if serr != nil {
			return replies, serr
		}
		return replies, err
	}

	l.convo.LastAgent = &agent
	l.convo.LastInstruction = instruction

	waitCtx, cancel := context.WithTimeout(ctx, l.opts.WaitTimeout)
	defer cancel()
	res, err := l.svc.AwaitResult(waitCtx, agent.ID, l.opts.OutputLines)
	if err != nil {
		// A timeout is an ordinary outcome, not a failure: the agent is
		// simply still working, and the user gets their turn back.
		if errors.Is(err, agentrt.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			more, serr := l.say(ctx, fmt.Sprintf("%s is still working. I'll stop waiting.", agent.DisplayKind()))
			return append(replies, more...), serr
		}
		more, serr := l.say(ctx, fmt.Sprintf("I lost track of %s.", service.DescribeAgent(agent)))
		replies = append(replies, more...)
		if serr != nil {
			return replies, serr
		}
		return replies, err
	}

	summary := Summarize(res.Output, 3)
	l.convo.LastResult = summary

	var reply string
	switch res.State {
	case agentrt.StateDone, agentrt.StateIdle:
		reply = fmt.Sprintf("%s finished.", agent.DisplayKind())
		if summary != "" {
			reply += " " + summary
		}
	case agentrt.StateBlocked:
		reply = fmt.Sprintf("%s needs your input in pane %s.", agent.DisplayKind(), agent.PaneLabel())
	default:
		reply = fmt.Sprintf("%s is still working. I'll stop waiting.", agent.DisplayKind())
	}
	more, serr := l.say(ctx, reply)
	return append(replies, more...), serr
}

// say speaks each line, logs it when a log writer is configured, and returns
// the lines for the caller.
func (l *Loop) say(ctx context.Context, lines ...string) ([]string, error) {
	for _, line := range lines {
		if l.opts.Log != nil {
			fmt.Fprintln(l.opts.Log, line)
		}
		if l.out == nil {
			continue
		}
		if err := l.out.Speak(ctx, line); err != nil {
			return lines, err
		}
	}
	return lines, nil
}

// dropPending forgets an unanswered clarification question.
func (l *Loop) dropPending() {
	l.convo.Pending = nil
	l.convo.PendingIntent = nil
}

// ansiRe matches ANSI escape sequences. The runtime is expected to hand over
// clean text; this is belt and braces so a stray escape never reaches a
// text-to-speech binary.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// chromeRe matches a line that is nothing but terminal decoration: box
// drawing, block elements, rules, and spinner glyphs.
var chromeRe = regexp.MustCompile(`^[\s\x{2500}-\x{257F}\x{2580}\x{259F}\x{25A0}-\x{25FF}\x{2800}-\x{28FF}=_*~#·•\-\|>]+$`)

// summaryCap bounds a spoken summary. Past this, a reply stops being an answer
// and becomes a recitation.
const summaryCap = 300

// Summarize reduces an agent's terminal output to something worth saying
// aloud: the last maxLines lines that carry text, joined into one sentence and
// capped.
//
// It is deterministic string handling, nothing more. Blank lines and lines
// made only of box drawing or prompt chrome are dropped, surrounding
// decoration is trimmed, and the tail is kept because that is where an agent
// says what it did. maxLines of 0 means 3.
//
// An LLM-written summary would read better and is a plausible future opt-in.
// It is deliberately NOT done here: this package makes no model calls, so a
// voice session never spends tokens or leaks an agent's transcript without the
// user asking for it.
func Summarize(output string, maxLines int) string {
	if maxLines <= 0 {
		maxLines = 3
	}
	var kept []string
	for _, raw := range strings.Split(ansiRe.ReplaceAllString(output, ""), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || chromeRe.MatchString(line) {
			continue
		}
		line = strings.Trim(line, "│|┃> \t")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return ""
	}
	if len(kept) > maxLines {
		kept = kept[len(kept)-maxLines:]
	}
	parts := make([]string, 0, len(kept))
	for _, k := range kept {
		parts = append(parts, strings.TrimRight(k, ". "))
	}
	out := strings.Join(parts, ". ") + "."
	return capRunes(out, summaryCap)
}

// capRunes truncates on a rune boundary, marking the cut.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n]), " ") + "..."
}

// workspaceName is the agent's workspace label, falling back to the runtime
// handle so a reply can always name where the agent is.
func workspaceName(a agentrt.AgentInstance) string {
	if a.Workspace.Label != "" {
		return a.Workspace.Label
	}
	return a.Workspace.ID
}

// capitalizeFirst upper-cases the first letter so a returned error reads as a
// sentence when spoken.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
