package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"qi/internal/agentrt"
	"qi/internal/agentrt/agentrttest"
)

// The fixture mirrors a real Herdr session: three workspaces, six agents, and
// crucially two Claude instances inside one workspace — the case qi must never
// resolve by guessing.
//
//	qi       (5, w9)  w9:p1  Claude  working   /Users/x/qi
//	                  w9:p2  Claude  idle      (inherits workspace cwd)
//	                  w9:p3  Codex   idle      /Users/x/qi/internal
//	ai-map   (6, w10) w10:p1 Claude  working   /Users/x/ai-map
//	handyman (7, w11) w11:p1 Codex   blocked   /Users/x/handyman
//	                  w11:p2 Claude  idle, named "reviewer"
//
// Agents are deliberately stored out of order so List's sort is load-bearing.
func fixture() *agentrttest.Fake {
	qi := agentrt.Workspace{ID: "w9", Number: 5, Label: "qi", Cwd: "/Users/x/qi", Focused: true}
	aimap := agentrt.Workspace{ID: "w10", Number: 6, Label: "ai-map", Cwd: "/Users/x/ai-map"}
	handyman := agentrt.Workspace{ID: "w11", Number: 7, Label: "handyman", Cwd: "/Users/x/handyman"}

	return &agentrttest.Fake{
		Workspaces: []agentrt.Workspace{handyman, qi, aimap},
		Agents: []agentrt.AgentInstance{
			{ID: "w11:p1", PaneID: "w11:p1", Kind: agentrt.KindCodex, State: agentrt.StateBlocked, Workspace: handyman, Cwd: "/Users/x/handyman"},
			{ID: "w9:p2", PaneID: "w9:p2", Kind: agentrt.KindClaude, State: agentrt.StateIdle, SessionID: "B", Workspace: qi},
			{ID: "w10:p1", PaneID: "w10:p1", Kind: agentrt.KindClaude, State: agentrt.StateWorking, Workspace: aimap, Cwd: "/Users/x/ai-map"},
			{ID: "w9:p3", PaneID: "w9:p3", Kind: agentrt.KindCodex, State: agentrt.StateIdle, Workspace: qi, Cwd: "/Users/x/qi/internal"},
			{ID: "w11:p2", PaneID: "w11:p2", Kind: agentrt.KindClaude, Name: "reviewer", State: agentrt.StateIdle, Workspace: handyman, Cwd: "/Users/x/handyman"},
			{ID: "w9:p1", PaneID: "w9:p1", Kind: agentrt.KindClaude, State: agentrt.StateWorking, SessionID: "A", Workspace: qi, Cwd: "/Users/x/qi", Focused: true},
		},
		Focus: agentrt.Focus{WorkspaceID: "w9", PaneID: "w9:p1", AgentID: "w9:p1"},
	}
}

func newFixtureService() *AgentService { return NewAgentService(fixture()) }

func TestAgentServiceListSorted(t *testing.T) {
	s := newFixtureService()
	agents, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]string, 0, len(agents))
	for _, a := range agents {
		got = append(got, string(a.ID))
	}
	want := []string{"w9:p1", "w9:p2", "w9:p3", "w10:p1", "w11:p1", "w11:p2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List order = %v, want %v", got, want)
	}
}

func TestAgentServiceWorkspacesSorted(t *testing.T) {
	s := newFixtureService()
	wss, err := s.Workspaces(context.Background())
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	got := make([]string, 0, len(wss))
	for _, w := range wss {
		got = append(got, w.Label)
	}
	want := []string{"qi", "ai-map", "handyman"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Workspaces order = %v, want %v", got, want)
	}
}

func TestAgentServiceRuntimeIsExposed(t *testing.T) {
	f := fixture()
	if got := NewAgentService(f).Runtime(); got != agentrt.Runtime(f) {
		t.Errorf("Runtime() did not return the wired runtime")
	}
}

func TestResolveUniqueByKindAndWorkspace(t *testing.T) {
	s := newFixtureService()
	got, err := s.Resolve(context.Background(), AgentQuery{Kind: agentrt.KindCodex, Workspace: "qi"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != "w9:p3" {
		t.Errorf("resolved %q, want w9:p3", got.ID)
	}
}

func TestResolveWorkspaceByIDAndCaseInsensitiveLabel(t *testing.T) {
	s := newFixtureService()
	for _, ws := range []string{"w9", "QI", "qi", "Qi"} {
		got, err := s.Resolve(context.Background(), AgentQuery{Kind: agentrt.KindCodex, Workspace: ws})
		if err != nil {
			t.Fatalf("Resolve(workspace=%q): %v", ws, err)
		}
		if got.ID != "w9:p3" {
			t.Errorf("Resolve(workspace=%q) = %q, want w9:p3", ws, got.ID)
		}
	}
}

func TestResolveKindOnlyIsAmbiguousAcrossWorkspaces(t *testing.T) {
	s := newFixtureService()
	_, err := s.Resolve(context.Background(), AgentQuery{Kind: agentrt.KindClaude})

	var amb *AmbiguousAgentError
	if !errors.As(err, &amb) {
		t.Fatalf("err = %v, want *AmbiguousAgentError", err)
	}
	gotIDs := make([]string, 0, len(amb.Candidates))
	for _, c := range amb.Candidates {
		gotIDs = append(gotIDs, string(c.ID))
	}
	wantIDs := []string{"w9:p1", "w9:p2", "w10:p1", "w11:p2"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("candidates = %v, want %v", gotIDs, wantIDs)
	}

	want := "I have four Claude agents. " +
		"The first is working in pane 5-1 in the qi workspace, " +
		"the second is idle in pane 5-2 in the qi workspace, " +
		"the third is working in pane 6-1 in the ai-map workspace and " +
		"the fourth is idle in pane 7-2 in the handyman workspace. " +
		"Which one do you mean?"
	if got := amb.Prompt(); got != want {
		t.Errorf("Prompt()\n got: %s\nwant: %s", got, want)
	}
}

func TestResolveTwoClaudesInOneWorkspaceIsAmbiguous(t *testing.T) {
	s := newFixtureService()
	_, err := s.Resolve(context.Background(), AgentQuery{Kind: agentrt.KindClaude, Workspace: "qi"})

	var amb *AmbiguousAgentError
	if !errors.As(err, &amb) {
		t.Fatalf("err = %v, want *AmbiguousAgentError", err)
	}
	if len(amb.Candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(amb.Candidates))
	}
	want := "I have two Claude agents in the qi workspace. " +
		"The first is working in pane 5-1 and the second is idle in pane 5-2. " +
		"Which one do you mean?"
	if got := amb.Prompt(); got != want {
		t.Errorf("Prompt()\n got: %s\nwant: %s", got, want)
	}
	// Error() stays a developer-facing string; Prompt() is the spoken one.
	if amb.Error() == "" {
		t.Error("Error() is empty")
	}
}

func TestAmbiguousPromptNamesKindsWhenTheyDiffer(t *testing.T) {
	s := newFixtureService()
	_, err := s.Resolve(context.Background(), AgentQuery{Workspace: "qi"})

	var amb *AmbiguousAgentError
	if !errors.As(err, &amb) {
		t.Fatalf("err = %v, want *AmbiguousAgentError", err)
	}
	want := "I have three agents in the qi workspace. " +
		"The first is Claude, working in pane 5-1, " +
		"the second is Claude, idle in pane 5-2 and " +
		"the third is Codex, idle in pane 5-3. " +
		"Which one do you mean?"
	if got := amb.Prompt(); got != want {
		t.Errorf("Prompt()\n got: %s\nwant: %s", got, want)
	}
}

func TestResolveFocused(t *testing.T) {
	s := newFixtureService()
	got, err := s.Resolve(context.Background(), AgentQuery{Focused: true})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != "w9:p1" {
		t.Errorf("resolved %q, want w9:p1", got.ID)
	}
}

func TestResolveFocusedPaneWithoutAgent(t *testing.T) {
	f := fixture()
	f.Focus = agentrt.Focus{WorkspaceID: "w9", PaneID: "w9:p9"}
	s := NewAgentService(f)

	_, err := s.Resolve(context.Background(), AgentQuery{Focused: true})
	var none *NoAgentError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want *NoAgentError", err)
	}
	if got, want := none.Error(), "No agent is in the focused pane."; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, agentrt.ErrAgentNotFound) {
		t.Error("NoAgentError should unwrap to agentrt.ErrAgentNotFound")
	}
}

func TestResolveFocusedWithKindMismatch(t *testing.T) {
	// The focused pane hosts Claude, so "the Codex I'm looking at" must fail
	// rather than widening to some other Codex.
	s := newFixtureService()
	_, err := s.Resolve(context.Background(), AgentQuery{Focused: true, Kind: agentrt.KindCodex})

	var none *NoAgentError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want *NoAgentError", err)
	}
	if got, want := none.Error(), "I can't find a Codex agent in the focused pane."; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestResolveByCwd(t *testing.T) {
	tests := []struct {
		name  string
		query AgentQuery
		want  agentrt.AgentID
	}{
		{"exact agent cwd", AgentQuery{Kind: agentrt.KindClaude, Cwd: "/Users/x/ai-map"}, "w10:p1"},
		{"query inside agent cwd", AgentQuery{Kind: agentrt.KindClaude, Cwd: "/Users/x/ai-map/web/src"}, "w10:p1"},
		{"query inside a deeper agent cwd", AgentQuery{Kind: agentrt.KindCodex, Cwd: "/Users/x/qi/internal/service"}, "w9:p3"},
		{"agent cwd inside query dir", AgentQuery{Kind: agentrt.KindCodex, Cwd: "/Users/x/handyman/app"}, "w11:p1"},
		{"unclean path", AgentQuery{Kind: agentrt.KindClaude, Cwd: "/Users/x/ai-map/web/.."}, "w10:p1"},
		{"falls back to workspace cwd", AgentQuery{Name: "", Kind: agentrt.KindClaude, Cwd: "/Users/x/qi", Workspace: "w9"}, ""},
	}
	s := newFixtureService()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Resolve(context.Background(), tc.query)
			if tc.want == "" {
				// Both qi Claudes match (one by its own cwd, one by the
				// workspace fallback), so this must be ambiguous.
				var amb *AmbiguousAgentError
				if !errors.As(err, &amb) {
					t.Fatalf("err = %v, want *AmbiguousAgentError", err)
				}
				if len(amb.Candidates) != 2 {
					t.Fatalf("got %d candidates, want 2", len(amb.Candidates))
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.ID != tc.want {
				t.Errorf("resolved %q, want %q", got.ID, tc.want)
			}
		})
	}
}

func TestResolveCwdRespectsPathBoundaries(t *testing.T) {
	// "/Users/x/qi2" is a sibling of "/Users/x/qi", not a child of it.
	s := newFixtureService()
	_, err := s.Resolve(context.Background(), AgentQuery{Cwd: "/Users/x/qi2"})

	var none *NoAgentError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want *NoAgentError", err)
	}
	if got, want := none.Error(), "I can't find an agent working in /Users/x/qi2."; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestResolveByName(t *testing.T) {
	s := newFixtureService()
	got, err := s.Resolve(context.Background(), AgentQuery{Name: "reviewer"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != "w11:p2" {
		t.Errorf("resolved %q, want w11:p2", got.ID)
	}

	_, err = s.Resolve(context.Background(), AgentQuery{Name: "ghost"})
	var none *NoAgentError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want *NoAgentError", err)
	}
	if got, want := none.Error(), "I can't find an agent named ghost."; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestResolveByID(t *testing.T) {
	s := newFixtureService()
	got, err := s.Resolve(context.Background(), AgentQuery{ID: "w9:p2"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.SessionID != "B" {
		t.Errorf("resolved session %q, want B", got.SessionID)
	}

	// An explicit handle beats every other field, even a contradictory one.
	got, err = s.Resolve(context.Background(), AgentQuery{ID: "w9:p2", Kind: agentrt.KindCodex, Workspace: "handyman"})
	if err != nil {
		t.Fatalf("Resolve with contradictory filters: %v", err)
	}
	if got.ID != "w9:p2" {
		t.Errorf("resolved %q, want w9:p2", got.ID)
	}
}

func TestResolveByIDNotFound(t *testing.T) {
	s := newFixtureService()
	_, err := s.Resolve(context.Background(), AgentQuery{ID: "w9:p99"})
	if !errors.Is(err, agentrt.ErrAgentNotFound) {
		t.Fatalf("err = %v, want agentrt.ErrAgentNotFound", err)
	}
}

func TestResolveNoMatch(t *testing.T) {
	s := newFixtureService()
	_, err := s.Resolve(context.Background(), AgentQuery{Kind: agentrt.KindCodex, Workspace: "ai-map"})

	var none *NoAgentError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want *NoAgentError", err)
	}
	if got, want := none.Error(), "Codex is not running in the ai-map workspace."; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestResolveNoAgentsAtAll(t *testing.T) {
	s := NewAgentService(&agentrttest.Fake{})
	_, err := s.Resolve(context.Background(), AgentQuery{})

	var none *NoAgentError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want *NoAgentError", err)
	}
	if got, want := none.Error(), "I can't find any agents."; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestRuntimeErrorPropagates(t *testing.T) {
	boom := errors.New("herdr socket closed")
	f := fixture()
	f.Err = boom
	s := NewAgentService(f)
	ctx := context.Background()

	if _, err := s.List(ctx); !errors.Is(err, boom) {
		t.Errorf("List err = %v, want %v", err, boom)
	}
	if _, err := s.Workspaces(ctx); !errors.Is(err, boom) {
		t.Errorf("Workspaces err = %v, want %v", err, boom)
	}
	if _, err := s.Resolve(ctx, AgentQuery{Kind: agentrt.KindClaude}); !errors.Is(err, boom) {
		t.Errorf("Resolve err = %v, want %v", err, boom)
	}
	if _, err := s.Resolve(ctx, AgentQuery{Focused: true}); !errors.Is(err, boom) {
		t.Errorf("Resolve(focused) err = %v, want %v", err, boom)
	}
	if _, err := s.Resolve(ctx, AgentQuery{ID: "w9:p1"}); !errors.Is(err, boom) {
		t.Errorf("Resolve(id) err = %v, want %v", err, boom)
	}
	if err := s.Instruct(ctx, "w9:p1", "hi"); !errors.Is(err, boom) {
		t.Errorf("Instruct err = %v, want %v", err, boom)
	}
	if _, err := s.AwaitResult(ctx, "w9:p1", 0); !errors.Is(err, boom) {
		t.Errorf("AwaitResult err = %v, want %v", err, boom)
	}
}

func TestInstruct(t *testing.T) {
	f := fixture()
	s := NewAgentService(f)

	if err := s.Instruct(context.Background(), "w9:p2", "run the tests"); err != nil {
		t.Fatalf("Instruct: %v", err)
	}
	want := []agentrttest.Sent{{ID: "w9:p2", Message: "run the tests"}}
	if !reflect.DeepEqual(f.SentMessages, want) {
		t.Errorf("SentMessages = %+v, want %+v", f.SentMessages, want)
	}
}

func TestInstructBlockedAgent(t *testing.T) {
	f := fixture()
	s := NewAgentService(f)

	err := s.Instruct(context.Background(), "w11:p1", "continue")
	if !errors.Is(err, agentrt.ErrAgentBlocked) {
		t.Fatalf("err = %v, want agentrt.ErrAgentBlocked", err)
	}
	if len(f.SentMessages) != 0 {
		t.Errorf("blocked send was recorded: %+v", f.SentMessages)
	}
}

func TestInstructUnknownAgent(t *testing.T) {
	s := newFixtureService()
	if err := s.Instruct(context.Background(), "w9:p99", "hi"); !errors.Is(err, agentrt.ErrAgentNotFound) {
		t.Fatalf("err = %v, want agentrt.ErrAgentNotFound", err)
	}
}

func TestAwaitResult(t *testing.T) {
	f := fixture()
	f.WaitStates = map[agentrt.AgentID]agentrt.State{"w9:p1": agentrt.StateDone}
	f.Outputs = map[agentrt.AgentID]string{"w9:p1": "all tests pass\n"}
	s := NewAgentService(f)

	got, err := s.AwaitResult(context.Background(), "w9:p1", 0)
	if err != nil {
		t.Fatalf("AwaitResult: %v", err)
	}
	if got.State != agentrt.StateDone {
		t.Errorf("State = %q, want done", got.State)
	}
	if got.Output != "all tests pass\n" {
		t.Errorf("Output = %q", got.Output)
	}
	// No explicit states: the runtime applies SettledStates itself.
	if len(f.Waits) != 1 || len(f.Waits[0]) != 0 {
		t.Errorf("Waits = %+v, want one call with no explicit states", f.Waits)
	}
}

func TestAwaitResultWaitFailure(t *testing.T) {
	f := fixture()
	f.WaitErr = agentrt.ErrTimeout
	s := NewAgentService(f)

	got, err := s.AwaitResult(context.Background(), "w9:p1", 10)
	if !errors.Is(err, agentrt.ErrTimeout) {
		t.Fatalf("err = %v, want agentrt.ErrTimeout", err)
	}
	if got.State != "" || got.Output != "" {
		t.Errorf("got %+v, want zero result", got)
	}
}

func TestDescribeAgent(t *testing.T) {
	agents := map[agentrt.AgentID]string{
		"w9:p3":  "Codex in qi, pane 5-3",
		"w11:p2": "Claude (reviewer) in handyman, pane 7-2",
		"w9:p1":  "Claude in qi, pane 5-1",
	}
	s := newFixtureService()
	for id, want := range agents {
		a, err := s.Resolve(context.Background(), AgentQuery{ID: id})
		if err != nil {
			t.Fatalf("Resolve(%q): %v", id, err)
		}
		if got := DescribeAgent(a); got != want {
			t.Errorf("DescribeAgent(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestSummarizeAgentsFixture(t *testing.T) {
	s := newFixtureService()
	agents, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{
		// Two Claudes share the qi workspace, so both name their pane.
		"Claude in pane 5-1 is working on qi.",
		"Claude in pane 5-2 is idle on qi.",
		"Codex is idle on qi.",
		"Claude is working on ai-map.",
		"Codex is blocked on handyman.",
		"Claude is idle on handyman.",
	}
	if got := SummarizeAgents(agents); !reflect.DeepEqual(got, want) {
		t.Errorf("SummarizeAgents()\n got: %q\nwant: %q", got, want)
	}
}

func TestSummarizeAgentsStates(t *testing.T) {
	ws := agentrt.Workspace{ID: "w10", Number: 6, Label: "ai-map"}
	agents := []agentrt.AgentInstance{
		{ID: "w10:p1", PaneID: "w10:p1", Kind: agentrt.KindClaude, State: agentrt.StateDone, Workspace: ws},
		{ID: "w10:p2", PaneID: "w10:p2", Kind: agentrt.KindCodex, State: agentrt.StateUnknown, Workspace: ws},
		{ID: "w10:p3", PaneID: "w10:p3", Kind: "aider", State: agentrt.StateWorking, Workspace: ws},
	}
	want := []string{
		"Claude is done on ai-map.",
		"Codex is in an unknown state on ai-map.",
		"Aider is working on ai-map.",
	}
	if got := SummarizeAgents(agents); !reflect.DeepEqual(got, want) {
		t.Errorf("SummarizeAgents()\n got: %q\nwant: %q", got, want)
	}
}

func TestSummarizeAgentsEmpty(t *testing.T) {
	want := []string{"No agents are running."}
	if got := SummarizeAgents(nil); !reflect.DeepEqual(got, want) {
		t.Errorf("SummarizeAgents(nil) = %q, want %q", got, want)
	}
}

func TestParseKind(t *testing.T) {
	tests := []struct {
		in   string
		want agentrt.Kind
	}{
		{"claude", agentrt.KindClaude},
		{"Claude", agentrt.KindClaude},
		{"claude code", agentrt.KindClaude},
		{"  Claude Code  ", agentrt.KindClaude},
		{"claude-code", agentrt.KindClaude},
		{"codex", agentrt.KindCodex},
		{"Codex", agentrt.KindCodex},
		{"openai codex", agentrt.KindCodex},
		{"OpenAI Codex", agentrt.KindCodex},
		{"", ""},
		{"   ", ""},
		{"Aider", agentrt.Kind("aider")},
		{"Some New Agent", agentrt.Kind("some new agent")},
	}
	for _, tc := range tests {
		if got := ParseKind(tc.in); got != tc.want {
			t.Errorf("ParseKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolve_WorkspaceLabelFoldsSeparators(t *testing.T) {
	rt := &agentrttest.Fake{Agents: []agentrt.AgentInstance{
		{ID: "w10:p1", Kind: agentrt.KindClaude, State: agentrt.StateWorking, PaneID: "w10:p1", Workspace: agentrt.Workspace{ID: "w10", Number: 6, Label: "ai-map"}},
		{ID: "w9:p1", Kind: agentrt.KindClaude, State: agentrt.StateIdle, PaneID: "w9:p1", Workspace: agentrt.Workspace{ID: "w9", Number: 5, Label: "qi"}},
	}}
	svc := NewAgentService(rt)
	for _, spoken := range []string{"ai map", "AI Map", "ai_map", "aimap"} {
		got, err := svc.Resolve(context.Background(), AgentQuery{Kind: agentrt.KindClaude, Workspace: spoken})
		if err != nil {
			t.Fatalf("Resolve(%q): %v", spoken, err)
		}
		if got.ID != "w10:p1" {
			t.Errorf("Resolve(%q) = %s, want w10:p1", spoken, got.ID)
		}
	}
	if _, err := svc.Resolve(context.Background(), AgentQuery{Kind: agentrt.KindClaude, Workspace: "ai-maps"}); err == nil {
		t.Error("ai-maps should not match ai-map")
	}
}

func TestResolve_PreferIDIsSoft(t *testing.T) {
	qi := agentrt.Workspace{ID: "w9", Number: 5, Label: "qi"}
	rt := &agentrttest.Fake{Agents: []agentrt.AgentInstance{
		{ID: "w9:p1", Kind: agentrt.KindClaude, State: agentrt.StateWorking, SessionID: "s1", PaneID: "w9:p1", Workspace: qi},
		{ID: "w9:p2", Kind: agentrt.KindClaude, State: agentrt.StateIdle, SessionID: "s2", PaneID: "w9:p2", Workspace: qi},
	}}
	svc := NewAgentService(rt)
	ctx := context.Background()

	// Still there with the same session: preferred wins, no question.
	got, err := svc.Resolve(ctx, AgentQuery{Kind: agentrt.KindClaude, PreferID: "w9:p2", PreferSessionID: "s2"})
	if err != nil || got.ID != "w9:p2" {
		t.Fatalf("got %v, %v", got.ID, err)
	}
	// Pane now hosts a different session: fall back to asking.
	_, err = svc.Resolve(ctx, AgentQuery{Kind: agentrt.KindClaude, PreferID: "w9:p2", PreferSessionID: "restarted"})
	var amb *AmbiguousAgentError
	if !errors.As(err, &amb) {
		t.Fatalf("want ambiguity after session change, got %v", err)
	}
	// Pane now hosts a different kind: the kind filter excludes it, so ask.
	rt.Agents[1].Kind = agentrt.KindCodex
	got, err = svc.Resolve(ctx, AgentQuery{Kind: agentrt.KindClaude, PreferID: "w9:p2", PreferSessionID: "s2"})
	if err != nil || got.ID != "w9:p1" {
		t.Fatalf("only one Claude remains; got %v, %v", got.ID, err)
	}
	rt.Agents[1].Kind = agentrt.KindClaude
	_, err = svc.Resolve(ctx, AgentQuery{Kind: agentrt.KindCodex, PreferID: "w9:p2", PreferSessionID: "s2"})
	var none *NoAgentError
	if !errors.As(err, &none) {
		t.Fatalf("preferred id must not override kind; got %v", err)
	}
}

// deadlineAwareFake reports the deadline its ReadOutput call received so a
// test can prove the read is not bounded by the (already spent) wait budget.
type deadlineAwareFake struct {
	agentrttest.Fake
	readHadDeadline bool
	readExpired     bool
}

func (d *deadlineAwareFake) ReadOutput(ctx context.Context, id agentrt.AgentID, opts agentrt.OutputOptions) (agentrt.AgentOutput, error) {
	_, d.readHadDeadline = ctx.Deadline()
	d.readExpired = ctx.Err() != nil
	return d.Fake.ReadOutput(ctx, id, opts)
}

func TestAwaitResult_ReadNotStarvedByWaitDeadline(t *testing.T) {
	f := &deadlineAwareFake{}
	f.Agents = []agentrt.AgentInstance{{ID: "w9:p3", Kind: agentrt.KindCodex, State: agentrt.StateDone, PaneID: "w9:p3"}}
	f.Outputs = map[agentrt.AgentID]string{"w9:p3": "PASS"}
	svc := NewAgentService(f)
	// A deadline that is already (effectively) spent by the time the wait returns.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Nanosecond))
	defer cancel()
	time.Sleep(time.Millisecond)
	res, err := svc.AwaitResult(ctx, "w9:p3", 5)
	if err != nil {
		t.Fatalf("AwaitResult: %v (read must not inherit the expired wait deadline)", err)
	}
	if res.State != agentrt.StateDone || res.Output != "PASS" {
		t.Errorf("res = %+v", res)
	}
	if !f.readHadDeadline || f.readExpired {
		t.Errorf("read ctx: hadDeadline=%v expired=%v; want a fresh, unexpired deadline", f.readHadDeadline, f.readExpired)
	}
}

func TestResolve_UnknownWorkspaceNamesTheRealOnes(t *testing.T) {
	rt := &agentrttest.Fake{
		Workspaces: []agentrt.Workspace{{ID: "w9", Number: 5, Label: "qi"}, {ID: "w10", Number: 6, Label: "ai-map"}},
		Agents:     []agentrt.AgentInstance{{ID: "w9:p1", Kind: agentrt.KindClaude, State: agentrt.StateIdle, PaneID: "w9:p1", Workspace: agentrt.Workspace{ID: "w9", Number: 5, Label: "qi"}}},
	}
	svc := NewAgentService(rt)
	_, err := svc.Resolve(context.Background(), AgentQuery{Kind: agentrt.KindClaude, Workspace: "key"})
	var none *NoAgentError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v", err)
	}
	if got, want := none.Error(), "I don't have a workspace called key. Your workspaces are qi and ai-map."; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	_, err = svc.Resolve(context.Background(), AgentQuery{Workspace: "ai-map"})
	if !errors.As(err, &none) || none.Error() != "No agent is running in the ai-map workspace." {
		t.Errorf("err = %v", err)
	}
	// Alias-folded label counts as known.
	_, err = svc.Resolve(context.Background(), AgentQuery{Kind: agentrt.KindCodex, Workspace: "AI Map"})
	if !errors.As(err, &none) || none.Error() != "Codex is not running in the AI Map workspace." {
		t.Errorf("err = %v", err)
	}
}
