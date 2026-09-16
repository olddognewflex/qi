package voice

import (
	"testing"

	"qi/internal/agentrt"
	"qi/internal/service"
)

func agent(id, kind, wsID, wsLabel string, wsNum int, pane string, state agentrt.State) agentrt.AgentInstance {
	return agentrt.AgentInstance{
		ID:        agentrt.AgentID(id),
		Kind:      agentrt.Kind(kind),
		State:     state,
		PaneID:    pane,
		Workspace: agentrt.Workspace{ID: wsID, Number: wsNum, Label: wsLabel},
	}
}

func TestContextExpand(t *testing.T) {
	codex := agent("w9:p3", "codex", "w9", "qi", 5, "w9:p3", agentrt.StateIdle)

	tests := []struct {
		name string
		ctx  Context
		in   string
		want string
	}{
		{
			name: "no context leaves the instruction alone",
			in:   "review that",
			want: "review that",
		},
		{
			name: "no pronoun leaves the instruction alone",
			ctx:  Context{LastAgent: &codex, LastInstruction: "run the tests"},
			in:   "open a pull request",
			want: "open a pull request",
		},
		{
			name: "that is annotated with the previous request",
			ctx:  Context{LastAgent: &codex, LastInstruction: "run the tests"},
			in:   "review that",
			want: `review that (the result of the previous request to Codex: "run the tests")`,
		},
		{
			name: "it is annotated too",
			ctx:  Context{LastAgent: &codex, LastInstruction: "run the tests"},
			in:   "explain it",
			want: `explain it (the result of the previous request to Codex: "run the tests")`,
		},
		{
			name: "pronoun inside a word is not a back-reference",
			ctx:  Context{LastAgent: &codex, LastInstruction: "run the tests"},
			in:   "commit the fix",
			want: "commit the fix",
		},
		{
			name: "unknown agent still names the previous request",
			ctx:  Context{LastInstruction: "run the tests"},
			in:   "review that",
			want: `review that (the result of the previous request to the previous agent: "run the tests")`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ctx.Expand(tc.in); got != tc.want {
				t.Errorf("Expand(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestContextQuery(t *testing.T) {
	claude := agent("w9:p2", "claude", "w9", "qi", 5, "w9:p2", agentrt.StateIdle)

	tests := []struct {
		name string
		ctx  Context
		t    Target
		env  Env
		want service.AgentQuery
	}{
		{
			name: "explicit kind and workspace pass through",
			t:    Target{Kind: agentrt.KindCodex, Workspace: "qi"},
			want: service.AgentQuery{Kind: agentrt.KindCodex, Workspace: "qi"},
		},
		{
			name: "focused becomes a focus query",
			t:    Target{Focused: true},
			want: service.AgentQuery{Focused: true},
		},
		{
			name: "this workspace uses the herdr workspace when known",
			t:    Target{Kind: agentrt.KindClaude, ThisWorkspace: true},
			env:  Env{WorkspaceID: "w9", Cwd: "/src/qi"},
			want: service.AgentQuery{Kind: agentrt.KindClaude, Workspace: "w9"},
		},
		{
			name: "this workspace falls back to the working directory",
			t:    Target{Kind: agentrt.KindClaude, ThisWorkspace: true},
			env:  Env{Cwd: "/src/qi"},
			want: service.AgentQuery{Kind: agentrt.KindClaude, Cwd: "/src/qi"},
		},
		{
			name: "named agent passes through",
			t:    Target{Name: "reviewer"},
			want: service.AgentQuery{Name: "reviewer"},
		},
		{
			name: "bare kind reuses the agent we were just talking to",
			ctx:  Context{LastAgent: &claude},
			t:    Target{Kind: agentrt.KindClaude},
			want: service.AgentQuery{Kind: agentrt.KindClaude, PreferID: "w9:p2"},
		},
		{
			name: "a different kind does not reuse the last agent",
			ctx:  Context{LastAgent: &claude},
			t:    Target{Kind: agentrt.KindCodex},
			want: service.AgentQuery{Kind: agentrt.KindCodex},
		},
		{
			name: "an explicit workspace overrides conversational continuity",
			ctx:  Context{LastAgent: &claude},
			t:    Target{Kind: agentrt.KindClaude, Workspace: "ai-map"},
			want: service.AgentQuery{Kind: agentrt.KindClaude, Workspace: "ai-map"},
		},
		{
			name: "focus overrides conversational continuity",
			ctx:  Context{LastAgent: &claude},
			t:    Target{Kind: agentrt.KindClaude, Focused: true},
			want: service.AgentQuery{Kind: agentrt.KindClaude, Focused: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ctx.Query(tc.t, tc.env); got != tc.want {
				t.Errorf("Query() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestContextChoose(t *testing.T) {
	a1 := agent("w9:p1", "claude", "w9", "qi", 5, "w9:p1", agentrt.StateWorking)
	a2 := agent("w9:p2", "claude", "w9", "qi", 5, "w9:p2", agentrt.StateIdle)
	pending := func() *Context {
		held := Intent{Kind: IntentInstruct, Instruction: "run the tests"}
		return &Context{
			Pending:       &service.AmbiguousAgentError{Candidates: []agentrt.AgentInstance{a1, a2}},
			PendingIntent: &held,
		}
	}

	t.Run("ordinal", func(t *testing.T) {
		c := pending()
		got, err := c.Choose(Target{Ordinal: 2})
		if err != nil {
			t.Fatalf("Choose: %v", err)
		}
		if got.ID != a2.ID {
			t.Errorf("chose %s, want %s", got.ID, a2.ID)
		}
		if c.Pending != nil || c.PendingIntent != nil {
			t.Error("Choose should clear the pending question on success")
		}
	})

	t.Run("pane label", func(t *testing.T) {
		got, err := pending().Choose(Target{Pane: "5-1"})
		if err != nil {
			t.Fatalf("Choose: %v", err)
		}
		if got.ID != a1.ID {
			t.Errorf("chose %s, want %s", got.ID, a1.ID)
		}
	})

	t.Run("runtime pane id", func(t *testing.T) {
		got, err := pending().Choose(Target{Pane: "w9:p2"})
		if err != nil {
			t.Fatalf("Choose: %v", err)
		}
		if got.ID != a2.ID {
			t.Errorf("chose %s, want %s", got.ID, a2.ID)
		}
	})

	t.Run("state", func(t *testing.T) {
		got, err := pending().Choose(Target{State: agentrt.StateIdle})
		if err != nil {
			t.Fatalf("Choose: %v", err)
		}
		if got.ID != a2.ID {
			t.Errorf("chose %s, want %s", got.ID, a2.ID)
		}
	})

	t.Run("ordinal out of range keeps the question pending", func(t *testing.T) {
		c := pending()
		if _, err := c.Choose(Target{Ordinal: 5}); err == nil {
			t.Fatal("want an error for an out-of-range ordinal")
		} else if err.Error() != "I only have two options to choose from" {
			t.Errorf("error = %q", err.Error())
		}
		if c.Pending == nil {
			t.Error("an unusable answer must leave the question pending")
		}
	})

	t.Run("state matching nothing", func(t *testing.T) {
		if _, err := pending().Choose(Target{State: agentrt.StateBlocked}); err == nil {
			t.Fatal("want an error when no candidate is blocked")
		}
	})

	t.Run("ambiguous state", func(t *testing.T) {
		c := &Context{Pending: &service.AmbiguousAgentError{Candidates: []agentrt.AgentInstance{a1, a1}}}
		if _, err := c.Choose(Target{State: agentrt.StateWorking}); err == nil {
			t.Fatal("want an error when the state matches several candidates")
		}
	})

	t.Run("unknown pane", func(t *testing.T) {
		if _, err := pending().Choose(Target{Pane: "9-9"}); err == nil {
			t.Fatal("want an error for a pane that is not a candidate")
		}
	})

	t.Run("nothing pending", func(t *testing.T) {
		c := &Context{}
		if _, err := c.Choose(Target{Ordinal: 1}); err == nil {
			t.Fatal("want an error when no question is outstanding")
		}
	})

	t.Run("empty answer", func(t *testing.T) {
		if _, err := pending().Choose(Target{}); err == nil {
			t.Fatal("want an error for an answer that picks nothing")
		}
	})
}

func TestEnvFromOS(t *testing.T) {
	t.Setenv("HERDR_WORKSPACE_ID", "w9")
	t.Setenv("HERDR_PANE_ID", "w9:p1")
	env := EnvFromOS()
	if env.WorkspaceID != "w9" || env.PaneID != "w9:p1" {
		t.Errorf("EnvFromOS() = %+v, want the exported herdr handles", env)
	}
	if env.Cwd == "" {
		t.Error("EnvFromOS() left Cwd empty")
	}
}
