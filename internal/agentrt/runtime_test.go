package agentrt

import "testing"

func TestPaneLabel(t *testing.T) {
	cases := []struct {
		name string
		a    AgentInstance
		want string
	}{
		{
			name: "workspace number and pane ordinal",
			a:    AgentInstance{PaneID: "w9:p1", Workspace: Workspace{Number: 5}},
			want: "5-1",
		},
		{
			name: "multi-digit pane ordinal",
			a:    AgentInstance{PaneID: "w1:p12", Workspace: Workspace{Number: 1}},
			want: "1-12",
		},
		{
			name: "unknown workspace number falls back to the raw id",
			a:    AgentInstance{PaneID: "w9:p1"},
			want: "w9:p1",
		},
		{
			name: "hex workspace handle still labels by number",
			a:    AgentInstance{PaneID: "wA:p1", Workspace: Workspace{Number: 6}},
			want: "6-1",
		},
		{
			name: "non-numeric pane ordinal falls back",
			a:    AgentInstance{PaneID: "w9:pD", Workspace: Workspace{Number: 5}},
			want: "w9:pD",
		},
		{
			name: "unexpected id scheme falls back",
			a:    AgentInstance{PaneID: "pane-7", Workspace: Workspace{Number: 5}},
			want: "pane-7",
		},
		{
			name: "empty ordinal falls back",
			a:    AgentInstance{PaneID: "w9:p", Workspace: Workspace{Number: 5}},
			want: "w9:p",
		},
		{
			name: "empty pane id",
			a:    AgentInstance{Workspace: Workspace{Number: 5}},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.PaneLabel(); got != tc.want {
				t.Fatalf("PaneLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDisplayKind(t *testing.T) {
	cases := []struct {
		kind Kind
		want string
	}{
		{KindClaude, "Claude"},
		{KindCodex, "Codex"},
		{"", "agent"},
		{"aider", "Aider"},
		{"q", "Q"},
	}
	for _, tc := range cases {
		if got := (AgentInstance{Kind: tc.kind}).DisplayKind(); got != tc.want {
			t.Fatalf("DisplayKind(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
