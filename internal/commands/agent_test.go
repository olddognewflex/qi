package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"qi/internal/agentrt"
	"qi/internal/agentrt/agentrttest"
	"qi/internal/config"
)

func fakeAgentRuntime(t *testing.T) *agentrttest.Fake {
	t.Helper()
	qi := agentrt.Workspace{ID: "w9", Number: 5, Label: "qi", Cwd: "/dev/qi"}
	hm := agentrt.Workspace{ID: "w11", Number: 7, Label: "handyman", Cwd: "/dev/handyman"}
	f := &agentrttest.Fake{
		Workspaces: []agentrt.Workspace{qi, hm},
		Agents: []agentrt.AgentInstance{
			{ID: "w9:p1", Kind: agentrt.KindClaude, State: agentrt.StateWorking, SessionID: "aaaa1111-0000", Workspace: qi, PaneID: "w9:p1", Cwd: "/dev/qi"},
			{ID: "w9:p2", Kind: agentrt.KindClaude, State: agentrt.StateIdle, SessionID: "bbbb2222-0000", Workspace: qi, PaneID: "w9:p2", Cwd: "/dev/qi"},
			{ID: "w9:p3", Kind: agentrt.KindCodex, State: agentrt.StateIdle, Workspace: qi, PaneID: "w9:p3", Cwd: "/dev/qi"},
			{ID: "w11:p1", Kind: agentrt.KindCodex, State: agentrt.StateBlocked, Workspace: hm, PaneID: "w11:p1", Cwd: "/dev/handyman"},
		},
		Outputs:    map[agentrt.AgentID]string{"w9:p3": "running tests...\nok  qi/internal/vault 0.3s\nPASS"},
		WaitStates: map[agentrt.AgentID]agentrt.State{"w9:p3": agentrt.StateDone},
	}
	prev := buildAgentRuntime
	buildAgentRuntime = func(config.Config) (agentrt.Runtime, error) { return f, nil }
	t.Cleanup(func() { buildAgentRuntime = prev })
	return f
}

func runAgentCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newAgentCommand(config.Config{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestAgentList_TableAndJSON(t *testing.T) {
	fakeAgentRuntime(t)
	out, err := runAgentCmd(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"KIND", "Claude", "Codex", "w9:p2", "handyman", "blocked", "aaaa1111"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	out, err = runAgentCmd(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got agentListJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if got.Runtime != "fake" || len(got.Agents) != 4 || got.Agents[1].PaneLabel != "5-2" || got.Agents[1].SessionID != "bbbb2222-0000" {
		t.Errorf("unexpected json: %+v", got)
	}
}

func TestAgentStatus_Sentences(t *testing.T) {
	fakeAgentRuntime(t)
	out, err := runAgentCmd(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Codex is blocked on handyman.") || !strings.Contains(out, "pane 5-1") {
		t.Errorf("status output:\n%s", out)
	}
}

func TestAgentSend_ExplicitTargetWaits(t *testing.T) {
	f := fakeAgentRuntime(t)
	out, err := runAgentCmd(t, "send", "--kind", "codex", "--workspace", "qi", "--wait", "run", "the", "tests")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.SentMessages) != 1 || f.SentMessages[0].ID != "w9:p3" || f.SentMessages[0].Message != "run the tests" {
		t.Errorf("sent = %+v", f.SentMessages)
	}
	for _, want := range []string{"Found Codex in qi, pane 5-3. Sending the request.", "Codex is done.", "PASS"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestAgentSend_AmbiguousRefusesAndNames(t *testing.T) {
	f := fakeAgentRuntime(t)
	out, err := runAgentCmd(t, "send", "--kind", "claude", "--workspace", "qi", "hello")
	if err == nil {
		t.Fatalf("expected ambiguity error, got output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "two Claude agents") || !strings.Contains(err.Error(), "pane 5-1") || !strings.Contains(err.Error(), "--id") {
		t.Errorf("error = %v", err)
	}
	if len(f.SentMessages) != 0 {
		t.Errorf("nothing should be sent, got %+v", f.SentMessages)
	}
}

func TestAgentSend_BlockedNotWritten(t *testing.T) {
	f := fakeAgentRuntime(t)
	_, err := runAgentCmd(t, "send", "--kind", "codex", "--workspace", "handyman", "hello")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("err = %v", err)
	}
	if len(f.SentMessages) != 0 {
		t.Errorf("blocked agent must not be written to: %+v", f.SentMessages)
	}
}

func TestAgentSend_NoMatch(t *testing.T) {
	fakeAgentRuntime(t)
	_, err := runAgentCmd(t, "send", "--kind", "codex", "--workspace", "nope", "hello")
	if err == nil || !strings.Contains(err.Error(), "I don't have a workspace called nope. Your workspaces are qi and handyman.") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentRead_ByID(t *testing.T) {
	fakeAgentRuntime(t)
	out, err := runAgentCmd(t, "read", "--id", "w9:p3", "--lines", "2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PASS") {
		t.Errorf("out = %q", out)
	}
}

func TestVoiceOnce_DrivesLoopWithoutAudio(t *testing.T) {
	f := fakeAgentRuntime(t)
	cmd := newVoiceCommand(config.Config{Voice: config.VoiceConfig{TTS: "echo"}})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--once", "Tell the Codex agent in the qi workspace to run the tests."})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(f.SentMessages) != 1 || f.SentMessages[0].ID != "w9:p3" {
		t.Errorf("sent = %+v", f.SentMessages)
	}
	got := out.String()
	if strings.Count(got, "Found Codex in qi, pane 5-3") != 1 {
		t.Errorf("reply should be spoken exactly once:\n%s", got)
	}
	if !strings.Contains(got, "Codex finished.") {
		t.Errorf("missing completion:\n%s", got)
	}
}

func TestAgentSend_LocalRuntimeExplainsNoRuntime(t *testing.T) {
	prev := buildAgentRuntime
	buildAgentRuntime = func(config.Config) (agentrt.Runtime, error) { return agentrt.NewLocalRuntime(), nil }
	t.Cleanup(func() { buildAgentRuntime = prev })
	_, err := runAgentCmd(t, "send", "--kind", "codex", "hello")
	if err == nil || !strings.Contains(err.Error(), "No agent runtime was detected") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentSend_IDRejectsOtherConstraints(t *testing.T) {
	f := fakeAgentRuntime(t)
	_, err := runAgentCmd(t, "send", "--id", "w9:p3", "--focused", "hello")
	if err == nil || !strings.Contains(err.Error(), "--id cannot be combined") {
		t.Fatalf("err = %v", err)
	}
	if len(f.SentMessages) != 0 {
		t.Errorf("nothing should be sent: %+v", f.SentMessages)
	}
}
