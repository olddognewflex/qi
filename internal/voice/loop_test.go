package voice

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"qi/internal/agentrt"
	"qi/internal/agentrt/agentrttest"
	"qi/internal/service"
)

// fixture builds the five-agent world the voice slice is specified against:
// two Claudes in the qi workspace (one working, one idle), a Codex in qi, a
// Claude in ai-map, and a blocked Codex in handyman.
func fixture() *agentrttest.Fake {
	qi := agentrt.Workspace{ID: "w9", Number: 5, Label: "qi", Cwd: "/src/qi"}
	aiMap := agentrt.Workspace{ID: "w10", Number: 6, Label: "ai-map", Cwd: "/src/ai-map"}
	handyman := agentrt.Workspace{ID: "w11", Number: 7, Label: "handyman", Cwd: "/src/handyman"}

	return &agentrttest.Fake{
		Workspaces: []agentrt.Workspace{qi, aiMap, handyman},
		Agents: []agentrt.AgentInstance{
			{ID: "w9:p1", Kind: agentrt.KindClaude, State: agentrt.StateWorking, Workspace: qi, PaneID: "w9:p1", Cwd: "/src/qi"},
			{ID: "w9:p2", Kind: agentrt.KindClaude, State: agentrt.StateIdle, Workspace: qi, PaneID: "w9:p2", Cwd: "/src/qi"},
			{ID: "w9:p3", Kind: agentrt.KindCodex, State: agentrt.StateIdle, Workspace: qi, PaneID: "w9:p3", Cwd: "/src/qi"},
			{ID: "w10:p1", Kind: agentrt.KindClaude, State: agentrt.StateWorking, Workspace: aiMap, PaneID: "w10:p1", Cwd: "/src/ai-map"},
			{ID: "w11:p1", Kind: agentrt.KindCodex, State: agentrt.StateBlocked, Workspace: handyman, PaneID: "w11:p1", Cwd: "/src/handyman"},
		},
		Focus: agentrt.Focus{WorkspaceID: "w9", PaneID: "w9:p1", AgentID: "w9:p1"},
		Outputs: map[agentrt.AgentID]string{
			"w9:p3": "╭───────────────╮\n│ running tests │\n╰───────────────╯\n\nRan 12 tests, all green.\nDone in 4.2s\n> ",
			"w9:p2": "Reviewed the diff.\nNo blocking issues.",
		},
	}
}

func newLoop(t *testing.T, fake *agentrttest.Fake) (*Loop, *bytes.Buffer) {
	t.Helper()
	var spoken bytes.Buffer
	l := NewLoop(service.NewAgentService(fake), nil, NewEchoSpeaker(&spoken), Options{
		WaitTimeout: time.Second,
		OutputLines: 40,
		Env:         Env{WorkspaceID: "w9", PaneID: "w9:p1", Cwd: "/src/qi"},
	})
	return l, &spoken
}

func handle(t *testing.T, l *Loop, utterance string) []string {
	t.Helper()
	replies, err := l.HandleUtterance(context.Background(), utterance)
	if err != nil {
		t.Fatalf("HandleUtterance(%q): %v", utterance, err)
	}
	return replies
}

// TestLoopSuccessScenario is the spec's happy path end to end.
func TestLoopSuccessScenario(t *testing.T) {
	fake := fixture()
	fake.WaitStates = map[agentrt.AgentID]agentrt.State{"w9:p3": agentrt.StateDone}
	l, spoken := newLoop(t, fake)

	replies := handle(t, l, "Tell the Codex agent in the qi workspace to run the tests.")

	want := []string{
		"Found Codex in qi, pane 5-3. Sending the request.",
		"Codex finished. running tests. Ran 12 tests, all green. Done in 4.2s.",
	}
	if !reflect.DeepEqual(replies, want) {
		t.Errorf("replies =\n  %q\nwant\n  %q", replies, want)
	}
	if got := fake.SentMessages; len(got) != 1 || got[0].ID != "w9:p3" || got[0].Message != "run the tests" {
		t.Errorf("SentMessages = %+v, want one send of %q to w9:p3", got, "run the tests")
	}
	for _, line := range want {
		if !strings.Contains(spoken.String(), line) {
			t.Errorf("reply %q was never spoken; transcript:\n%s", line, spoken.String())
		}
	}
}

func TestLoopStatus(t *testing.T) {
	l, _ := newLoop(t, fixture())

	replies := handle(t, l, "What are my agents doing?")
	want := []string{
		"Claude in pane 5-1 is working on qi.",
		"Claude in pane 5-2 is idle on qi.",
		"Codex is idle on qi.",
		"Claude is working on ai-map.",
		"Codex is blocked on handyman.",
	}
	if !reflect.DeepEqual(replies, want) {
		t.Errorf("replies =\n  %q\nwant\n  %q", replies, want)
	}
}

// TestLoopAmbiguityThenClarificationThenFollowUp is the heart of the package:
// two Claudes in one workspace must produce a question, the answer must pick
// the right instance, and the next sentence must keep talking to it.
func TestLoopAmbiguityThenClarificationThenFollowUp(t *testing.T) {
	fake := fixture()
	fake.WaitStates = map[agentrt.AgentID]agentrt.State{"w9:p2": agentrt.StateIdle}
	l, _ := newLoop(t, fake)

	// 1. Ambiguous: two Claudes in qi.
	replies := handle(t, l, "Tell Claude in qi to run the tests")
	if len(replies) != 1 {
		t.Fatalf("replies = %q, want a single clarification question", replies)
	}
	q := replies[0]
	for _, want := range []string{"two Claude agents", "the qi workspace", "pane 5-1", "pane 5-2", "Which one do you mean?"} {
		if !strings.Contains(q, want) {
			t.Errorf("clarification %q is missing %q", q, want)
		}
	}
	if len(fake.SentMessages) != 0 {
		t.Fatalf("an ambiguous request must send nothing, sent %+v", fake.SentMessages)
	}
	if l.Context().Pending == nil || l.Context().PendingIntent == nil {
		t.Fatal("the loop must hold the question and the deferred instruction")
	}

	// 2. The answer picks the second candidate and the held instruction goes out.
	replies = handle(t, l, "the second one")
	if len(replies) != 2 || !strings.HasPrefix(replies[0], "Found Claude in qi, pane 5-2.") {
		t.Fatalf("replies = %q, want the chosen agent then its result", replies)
	}
	if got := fake.SentMessages; len(got) != 1 || got[0].ID != "w9:p2" || got[0].Message != "run the tests" {
		t.Fatalf("SentMessages = %+v, want one send of %q to w9:p2", got, "run the tests")
	}
	if l.Context().Pending != nil {
		t.Error("the question must be cleared once answered")
	}

	// 3. "Have Claude review that." keeps talking to the same instance, with
	// the back-reference expanded, and asks nothing.
	replies = handle(t, l, "Have Claude review that.")
	if len(replies) != 2 || !strings.HasPrefix(replies[0], "Found Claude in qi, pane 5-2.") {
		t.Fatalf("replies = %q, want the follow-up to reach pane 5-2 without asking", replies)
	}
	got := fake.SentMessages
	if len(got) != 2 {
		t.Fatalf("SentMessages = %+v, want a second send", got)
	}
	wantMsg := `review that (the result of the previous request to Claude: "run the tests")`
	if got[1].ID != "w9:p2" || got[1].Message != wantMsg {
		t.Errorf("second send = %+v, want %q to w9:p2", got[1], wantMsg)
	}
	if !strings.Contains(replies[1], "Reviewed the diff") {
		t.Errorf("result reply = %q, want the agent's output summarised", replies[1])
	}
}

func TestLoopClarifyByPaneAndState(t *testing.T) {
	for _, tc := range []struct {
		answer string
		wantID agentrt.AgentID
	}{
		{"the one in pane 5-1", "w9:p1"},
		{"pane 5-2", "w9:p2"},
		{"the idle one", "w9:p2"},
		{"the working one", "w9:p1"},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			fake := fixture()
			l, _ := newLoop(t, fake)
			handle(t, l, "Tell Claude in qi to run the tests")
			handle(t, l, tc.answer)
			if got := fake.SentMessages; len(got) != 1 || got[0].ID != tc.wantID {
				t.Fatalf("SentMessages = %+v, want a send to %s", got, tc.wantID)
			}
		})
	}
}

func TestLoopClarifyWithNothingPending(t *testing.T) {
	fake := fixture()
	l, _ := newLoop(t, fake)
	replies := handle(t, l, "the second one")
	if len(replies) != 1 || replies[0] != "I'm not waiting on a choice." {
		t.Errorf("replies = %q", replies)
	}
	if len(fake.SentMessages) != 0 {
		t.Errorf("nothing should have been sent, got %+v", fake.SentMessages)
	}
}

func TestLoopClarifyOutOfRangeKeepsAsking(t *testing.T) {
	fake := fixture()
	l, _ := newLoop(t, fake)
	handle(t, l, "Tell Claude in qi to run the tests")
	replies := handle(t, l, "the fifth one")
	if len(replies) != 1 || !strings.Contains(replies[0], "two options") {
		t.Fatalf("replies = %q, want a reply naming how many options there are", replies)
	}
	if l.Context().Pending == nil {
		t.Error("an unusable answer must leave the question open")
	}
	if len(fake.SentMessages) != 0 {
		t.Errorf("nothing should have been sent, got %+v", fake.SentMessages)
	}
}

func TestLoopFocusedAgent(t *testing.T) {
	fake := fixture()
	fake.WaitStates = map[agentrt.AgentID]agentrt.State{"w9:p1": agentrt.StateDone}
	fake.Outputs["w9:p1"] = "All green."
	l, _ := newLoop(t, fake)

	replies := handle(t, l, "Tell the agent I'm looking at to run the tests.")
	if len(replies) != 2 || !strings.HasPrefix(replies[0], "Found Claude in qi, pane 5-1.") {
		t.Fatalf("replies = %q, want the focused pane's agent", replies)
	}
	if got := fake.SentMessages; len(got) != 1 || got[0].ID != "w9:p1" {
		t.Fatalf("SentMessages = %+v, want a send to the focused pane", got)
	}
	if replies[1] != "Claude finished. All green." {
		t.Errorf("result = %q", replies[1])
	}
}

func TestLoopThisWorkspace(t *testing.T) {
	fake := fixture()
	l, _ := newLoop(t, fake)
	// Env.WorkspaceID is w9, so "in this workspace" narrows to qi; Codex
	// there is unique.
	replies := handle(t, l, "Ask Codex in this workspace to review the changes.")
	if !strings.HasPrefix(replies[0], "Found Codex in qi, pane 5-3.") {
		t.Fatalf("replies = %q", replies)
	}
	if got := fake.SentMessages; len(got) != 1 || got[0].ID != "w9:p3" {
		t.Errorf("SentMessages = %+v, want a send to the Codex in this workspace", got)
	}
}

func TestLoopBlockedAgent(t *testing.T) {
	fake := fixture()
	l, _ := newLoop(t, fake)

	replies := handle(t, l, "Tell Codex in handyman to run the tests")
	if len(replies) != 2 {
		t.Fatalf("replies = %q, want the found line and the blocked report", replies)
	}
	want := "Codex in handyman is blocked on an approval or question. Please handle it in pane 7-1 first."
	if replies[1] != want {
		t.Errorf("reply = %q, want %q", replies[1], want)
	}
	if len(fake.SentMessages) != 0 {
		t.Errorf("a blocked agent must not receive the instruction, got %+v", fake.SentMessages)
	}
}

func TestLoopNoSuchAgent(t *testing.T) {
	fake := fixture()
	l, _ := newLoop(t, fake)
	replies := handle(t, l, "Tell Codex in ai-map to run the tests")
	if len(replies) != 1 || !strings.Contains(replies[0], "can't find") {
		t.Fatalf("replies = %q, want a spoken not-found message", replies)
	}
	if len(fake.SentMessages) != 0 {
		t.Errorf("nothing should have been sent, got %+v", fake.SentMessages)
	}
}

func TestLoopStillWorking(t *testing.T) {
	fake := fixture()
	// The agent never settles: WaitForState reports it still working.
	fake.WaitStates = map[agentrt.AgentID]agentrt.State{"w9:p3": agentrt.StateWorking}
	l, _ := newLoop(t, fake)

	replies := handle(t, l, "Tell Codex in qi to run the tests")
	if len(replies) != 2 || replies[1] != "Codex is still working. I'll stop waiting." {
		t.Fatalf("replies = %q", replies)
	}
}

func TestLoopBlockedAfterSend(t *testing.T) {
	fake := fixture()
	fake.WaitStates = map[agentrt.AgentID]agentrt.State{"w9:p3": agentrt.StateBlocked}
	l, _ := newLoop(t, fake)

	replies := handle(t, l, "Tell Codex in qi to run the tests")
	if len(replies) != 2 || replies[1] != "Codex needs your input in pane 5-3." {
		t.Fatalf("replies = %q", replies)
	}
}

func TestLoopUnknownUtterance(t *testing.T) {
	fake := fixture()
	l, _ := newLoop(t, fake)
	replies := handle(t, l, "When Codex finishes, have Claude review its changes.")
	if len(replies) != 1 || replies[0] != helpReply {
		t.Fatalf("replies = %q, want the help reply", replies)
	}
	if len(fake.SentMessages) != 0 {
		t.Errorf("an utterance qi does not understand must send nothing, got %+v", fake.SentMessages)
	}
}

func TestLoopRunEndsOnQuit(t *testing.T) {
	fake := fixture()
	var spoken bytes.Buffer
	in := NewTextTranscriber(strings.NewReader("what are my agents doing\nquit\nthis line is never read\n"), nil)
	l := NewLoop(service.NewAgentService(fake), in, NewEchoSpeaker(&spoken), Options{})

	if err := l.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(spoken.String(), "Goodbye.") {
		t.Errorf("transcript = %q, want a goodbye", spoken.String())
	}
	if !strings.Contains(spoken.String(), "is working on qi") {
		t.Errorf("transcript = %q, want the status report", spoken.String())
	}
}

func TestLoopRunEndsOnEOF(t *testing.T) {
	fake := fixture()
	in := NewTextTranscriber(strings.NewReader("status\n"), nil)
	l := NewLoop(service.NewAgentService(fake), in, NewEchoSpeaker(&bytes.Buffer{}), Options{})
	if err := l.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestLoopRunHonoursCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := NewTextTranscriber(strings.NewReader("status\n"), nil)
	l := NewLoop(service.NewAgentService(fixture()), in, NewEchoSpeaker(&bytes.Buffer{}), Options{})
	if err := l.Run(ctx); err != context.Canceled {
		t.Fatalf("Run() = %v, want context.Canceled", err)
	}
}

func TestLoopRunKeepsGoingAfterARuntimeFailure(t *testing.T) {
	fake := fixture()
	fake.Err = agentrt.ErrUnavailable
	var spoken bytes.Buffer
	in := NewTextTranscriber(strings.NewReader("status\nquit\n"), nil)
	l := NewLoop(service.NewAgentService(fake), in, NewEchoSpeaker(&spoken), Options{})

	if err := l.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(spoken.String(), "can't reach the agent runtime") {
		t.Errorf("transcript = %q, want the failure spoken back", spoken.String())
	}
	if !strings.Contains(spoken.String(), "Goodbye.") {
		t.Errorf("a failed utterance must not end the session; transcript = %q", spoken.String())
	}
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		lines int
		want  string
	}{
		{name: "empty", in: "", want: ""},
		{name: "only chrome", in: "╭────╮\n│    │\n╰────╯\n\n> \n", want: ""},
		{
			name: "keeps the tail",
			in:   "first\nsecond\nthird\nfourth\nfifth",
			want: "third. fourth. fifth.",
		},
		{
			name:  "respects maxLines",
			in:    "first\nsecond\nthird",
			lines: 1,
			want:  "third.",
		},
		{
			name: "strips box drawing and blank lines",
			in:   "╭───────╮\n│ done! │\n╰───────╯\n\n  Ran 12 tests.  \n",
			want: "done!. Ran 12 tests.",
		},
		{
			name: "strips ansi escapes",
			in:   "\x1b[32mAll tests passed\x1b[0m\n",
			want: "All tests passed.",
		},
		{
			name: "drops a bare prompt line",
			in:   "Compiled successfully\n> ",
			want: "Compiled successfully.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Summarize(tc.in, tc.lines); got != tc.want {
				t.Errorf("Summarize() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSummarizeIsCapped(t *testing.T) {
	long := strings.Repeat("a very long line of terminal output ", 40)
	got := Summarize(long, 3)
	if len([]rune(got)) > summaryCap+3 {
		t.Errorf("summary is %d runes, want it capped near %d", len([]rune(got)), summaryCap)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("a truncated summary should say so, got %q", got[max(0, len(got)-20):])
	}
}
