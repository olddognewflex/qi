package voice

import (
	"testing"

	"qi/internal/agentrt"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Intent
	}{
		// --- instruct: kind + explicit workspace ---
		{
			name: "codex agent in named workspace",
			in:   "Tell the Codex agent in the qi workspace to run the tests.",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindCodex, Workspace: "qi"}, Instruction: "run the tests"},
		},
		{
			name: "bare kind in bare workspace",
			in:   "Tell Codex in Qi to investigate the failing tests.",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindCodex, Workspace: "qi"}, Instruction: "investigate the failing tests"},
		},
		{
			name: "multiword workspace label",
			in:   "Ask the Claude agent in the ai map workspace to deploy",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude, Workspace: "ai map"}, Instruction: "deploy"},
		},
		{
			name: "claude code two-word kind",
			in:   "Tell the Claude Code agent in qi to open a PR",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude, Workspace: "qi"}, Instruction: "open a PR"},
		},

		// --- instruct: this workspace ---
		{
			name: "in this workspace",
			in:   "Ask Claude in this workspace to review the changes.",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude, ThisWorkspace: true}, Instruction: "review the changes"},
		},
		{
			name: "here",
			in:   "Tell Claude here to run the tests",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude, ThisWorkspace: true}, Instruction: "run the tests"},
		},

		// --- instruct: focused ---
		{
			name: "agent I'm looking at",
			in:   "Tell the agent I'm looking at to run the tests.",
			want: Intent{Kind: IntentInstruct, Target: Target{Focused: true}, Instruction: "run the tests"},
		},
		{
			name: "agent I am looking at",
			in:   "Tell the agent I am looking at to run the tests",
			want: Intent{Kind: IntentInstruct, Target: Target{Focused: true}, Instruction: "run the tests"},
		},
		{
			name: "kind I'm looking at keeps the kind",
			in:   "Tell the Codex I'm looking at to stop",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindCodex, Focused: true}, Instruction: "stop"},
		},
		{
			name: "focused agent",
			in:   "Ask the focused agent to summarize its changes",
			want: Intent{Kind: IntentInstruct, Target: Target{Focused: true}, Instruction: "summarize its changes"},
		},
		{
			name: "current agent",
			in:   "Have the current agent run the linter",
			want: Intent{Kind: IntentInstruct, Target: Target{Focused: true}, Instruction: "run the linter"},
		},
		{
			name: "this agent",
			in:   "Tell this agent to commit",
			want: Intent{Kind: IntentInstruct, Target: Target{Focused: true}, Instruction: "commit"},
		},
		{
			name: "agent in front of me",
			in:   "Tell the agent in front of me to run the tests",
			want: Intent{Kind: IntentInstruct, Target: Target{Focused: true}, Instruction: "run the tests"},
		},

		// --- instruct: named / conversational / no "to" ---
		{
			name: "agent named",
			in:   "Tell the agent named reviewer to deploy.",
			want: Intent{Kind: IntentInstruct, Target: Target{Name: "reviewer"}, Instruction: "deploy"},
		},
		{
			name: "agent called keeps original casing",
			in:   "Ask the agent called Reviewer to look again",
			want: Intent{Kind: IntentInstruct, Target: Target{Name: "Reviewer"}, Instruction: "look again"},
		},
		{
			name: "conversational bare kind without to",
			in:   "Have Claude review that.",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude}, Instruction: "review that"},
		},
		{
			name: "ask whether keeps the question as the instruction",
			in:   "Ask Codex whether the build is green",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindCodex}, Instruction: "whether the build is green"},
		},
		{
			name: "get verb",
			in:   "Get Codex to fix the build",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindCodex}, Instruction: "fix the build"},
		},
		{
			name: "instruct verb with wake word and please",
			in:   "Hey qi, please instruct Claude to write the changelog",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude}, Instruction: "write the changelog"},
		},
		{
			name: "trailing workspace phrase stays in the instruction",
			in:   "Ask Codex to run the tests in the qi workspace",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindCodex}, Instruction: "run the tests in the qi workspace"},
		},
		{
			name: "instruction casing preserved",
			in:   "Tell Claude to update the README and CLAUDE.md",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude}, Instruction: "update the README and CLAUDE.md"},
		},

		// --- status ---
		{name: "status: what are my agents doing", in: "What are my agents doing?", want: Intent{Kind: IntentStatus}},
		{name: "status: currently working", in: "What agents are currently working?", want: Intent{Kind: IntentStatus}},
		{name: "status: bare", in: "Status", want: Intent{Kind: IntentStatus}},
		{name: "status: agent status", in: "agent status", want: Intent{Kind: IntentStatus}},
		{name: "status: who is working", in: "Who's working?", want: Intent{Kind: IntentStatus}},
		{name: "status: everyone", in: "What's everyone doing?", want: Intent{Kind: IntentStatus}},
		{name: "status: list my agents", in: "List my agents.", want: Intent{Kind: IntentStatus}},
		{name: "status: wake word", in: "Hey qi, what are my agents doing", want: Intent{Kind: IntentStatus}},

		// --- clarification answers ---
		{name: "clarify: the first one", in: "the first one", want: Intent{Kind: IntentClarify, Target: Target{Ordinal: 1}}},
		{name: "clarify: first", in: "First", want: Intent{Kind: IntentClarify, Target: Target{Ordinal: 1}}},
		{name: "clarify: number one", in: "number one", want: Intent{Kind: IntentClarify, Target: Target{Ordinal: 1}}},
		{name: "clarify: digit", in: "2", want: Intent{Kind: IntentClarify, Target: Target{Ordinal: 2}}},
		{name: "clarify: the second one", in: "The second one.", want: Intent{Kind: IntentClarify, Target: Target{Ordinal: 2}}},
		{name: "clarify: pane label", in: "the one in pane 5-2", want: Intent{Kind: IntentClarify, Target: Target{Pane: "5-2"}}},
		{name: "clarify: bare pane", in: "pane 5-2", want: Intent{Kind: IntentClarify, Target: Target{Pane: "5-2"}}},
		{name: "clarify: runtime pane id", in: "pane w9:p2", want: Intent{Kind: IntentClarify, Target: Target{Pane: "w9:p2"}}},
		{name: "clarify: working one", in: "the working one", want: Intent{Kind: IntentClarify, Target: Target{State: agentrt.StateWorking}}},
		{name: "clarify: idle one", in: "The idle one", want: Intent{Kind: IntentClarify, Target: Target{State: agentrt.StateIdle}}},
		{name: "clarify: blocked one", in: "the blocked one", want: Intent{Kind: IntentClarify, Target: Target{State: agentrt.StateBlocked}}},
		{name: "clarify: the one that is working", in: "the one that is working", want: Intent{Kind: IntentClarify, Target: Target{State: agentrt.StateWorking}}},

		// --- quit ---
		{name: "quit", in: "Quit.", want: Intent{Kind: IntentQuit}},
		{name: "quit: exit", in: "exit", want: Intent{Kind: IntentQuit}},
		{name: "quit: stop listening", in: "Stop listening", want: Intent{Kind: IntentQuit}},
		{name: "quit: goodbye", in: "Goodbye!", want: Intent{Kind: IntentQuit}},
		{name: "quit: bye", in: "bye", want: Intent{Kind: IntentQuit}},

		// --- unknown ---
		{
			name: "conditional cross-agent orchestration is not an instruct",
			in:   "When Codex finishes, have Claude review its changes.",
			want: Intent{Kind: IntentUnknown},
		},
		{name: "unknown: inverted form unsupported", in: "Run the tests, Codex", want: Intent{Kind: IntentUnknown}},
		{
			name: "state qualifier after kind plus that-introducer",
			in:   "Tell claude working in the Qi workspace that I merged the latest PR and would like to move on to the next step.",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude, Workspace: "qi", State: agentrt.StateWorking}, Instruction: "I merged the latest PR and would like to move on to the next step"},
		},
		{
			name: "state qualifier with that's and commas",
			in:   "Tell Claude, that's idle in qi, that the build is green.",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude, Workspace: "qi", State: agentrt.StateIdle}, Instruction: "the build is green"},
		},
		{
			name: "state word before kind",
			in:   "Ask the idle Codex to run the tests.",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindCodex, State: agentrt.StateIdle}, Instruction: "run the tests"},
		},
		{
			name: "workspace keyword before the label",
			in:   "Tell claude in the workspace key to stop.",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude, Workspace: "key"}, Instruction: "stop"},
		},
		{name: "clarify: the one in qi", in: "the one in qi", want: Intent{Kind: IntentClarify, Target: Target{Workspace: "qi"}}},
		{name: "clarify: the one in the qi workspace", in: "The one in the qi workspace.", want: Intent{Kind: IntentClarify, Target: Target{Workspace: "qi"}}},
		{name: "clarify: in this workspace", in: "the one in this workspace", want: Intent{Kind: IntentClarify, Target: Target{ThisWorkspace: true}}},
		{name: "clarify: the qi one", in: "the qi one", want: Intent{Kind: IntentClarify, Target: Target{Workspace: "qi"}}},
		{name: "clarify: the ai map one", in: "the ai map one", want: Intent{Kind: IntentClarify, Target: Target{Workspace: "ai map"}}},
		{name: "clarify: the one that's idle", in: "the one that's idle", want: Intent{Kind: IntentClarify, Target: Target{State: agentrt.StateIdle}}},
		{name: "clarify: bare state", in: "idle", want: Intent{Kind: IntentClarify, Target: Target{State: agentrt.StateIdle}}},
		{
			name: "STT hears in as and: explicit workspace keyword rescues it",
			in:   "Tell Claude and the qi workspace to move on to the next step",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude, Workspace: "qi"}, Instruction: "move on to the next step"},
		},
		{name: "unknown: and without the workspace keyword is still a second addressee", in: "Tell Claude and qi to run the tests.", want: Intent{Kind: IntentUnknown}},
		{name: "unknown: two addressees joined by and", in: "Tell Claude and Codex to run the tests.", want: Intent{Kind: IntentUnknown}},
		{name: "unknown: two workspace-qualified addressees", in: "Tell Claude in qi and Codex in ai-map to run the tests", want: Intent{Kind: IntentUnknown}},
		{
			name: "bare in-phrase without to stays in the instruction",
			in:   "Ask Claude in which file the bug is.",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude}, Instruction: "in which file the bug is"},
		},
		{name: "unknown: two addressees joined by then", in: "Tell Claude then the Codex agent to run the tests.", want: Intent{Kind: IntentUnknown}},
		{name: "unknown: second kind before to", in: "Tell the Claude agent in qi Codex to run the tests.", want: Intent{Kind: IntentUnknown}},
		{
			name: "kind after to is part of the instruction",
			in:   "Tell Claude to ask Codex for the diff.",
			want: Intent{Kind: IntentInstruct, Target: Target{Kind: agentrt.KindClaude}, Instruction: "ask Codex for the diff"},
		},
		{name: "unknown: no instruction", in: "Tell Claude.", want: Intent{Kind: IntentUnknown}},
		{name: "unknown: unnamed target", in: "Tell everyone the build is broken", want: Intent{Kind: IntentUnknown}},
		{name: "unknown: empty", in: "   ", want: Intent{Kind: IntentUnknown}},
		{name: "unknown: unrelated question", in: "What is the weather in Dublin?", want: Intent{Kind: IntentUnknown}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.in)
			if got.Kind != tc.want.Kind {
				t.Fatalf("Kind = %v, want %v (raw %q)", got.Kind, tc.want.Kind, tc.in)
			}
			if got.Target != tc.want.Target {
				t.Errorf("Target = %+v, want %+v", got.Target, tc.want.Target)
			}
			if got.Instruction != tc.want.Instruction {
				t.Errorf("Instruction = %q, want %q", got.Instruction, tc.want.Instruction)
			}
			if got.Raw != tc.in {
				t.Errorf("Raw = %q, want %q", got.Raw, tc.in)
			}
		})
	}
}

func TestParseKeepsRawUtterance(t *testing.T) {
	in := "  Tell Claude to ship it.  "
	got := Parse(in)
	if got.Raw != in {
		t.Errorf("Raw = %q, want the untouched utterance %q", got.Raw, in)
	}
	if got.Instruction != "ship it" {
		t.Errorf("Instruction = %q, want %q", got.Instruction, "ship it")
	}
}

func TestIntentKindString(t *testing.T) {
	for k, want := range map[IntentKind]string{
		IntentUnknown:  "unknown",
		IntentStatus:   "status",
		IntentInstruct: "instruct",
		IntentClarify:  "clarify",
		IntentQuit:     "quit",
	} {
		if got := k.String(); got != want {
			t.Errorf("IntentKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}
