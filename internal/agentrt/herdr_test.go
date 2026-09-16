package agentrt

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fixtures are verbatim `herdr` output captured live (herdr 0.9.0,
// protocol 22). Do not reformat them: the point is that the real bytes
// decode.

const fixtureAgentList = `{"id":"cli:agent:list","result":{"type":"agent_list","agents":[` +
	`{"agent":"codex","agent_session":{"agent":"codex","kind":"id","source":"herdr:codex","value":"01a0a72c-4f0e-7e63-9709-60c4e3de1fe4"},"agent_status":"blocked","cwd":"/Users/raymonddoran/Development/odnf/TEACH","focused":false,"foreground_cwd":"/Users/raymonddoran/Development/odnf/TEACH","pane_id":"w1:p4","revision":28615,"state_change_seq":62,"tab_id":"w1:t1","terminal_id":"term_65b743d8cc5341","terminal_title":"[ ! ] Action Required | Create 1.0 release plan and issues | TEACH","terminal_title_stripped":"[ ! ] Action Required | Create 1.0 release plan and issues | TEACH","workspace_id":"w1"},` +
	`{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"2b90dfdc-cf4b-411e-83cf-82c33218bd1a"},"agent_status":"working","cwd":"/Users/raymonddoran/Development/odnf/Somehow","focused":true,"foreground_cwd":"/Users/raymonddoran/Development/odnf/Somehow","pane_id":"w5:p1","revision":8,"state_change_seq":126,"tab_id":"w5:t1","terminal_id":"term_65b743d8cf0b55","terminal_title":"◑ tier:free issues impact sort","terminal_title_stripped":"tier:free issues impact sort","workspace_id":"w5"},` +
	`{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"a07c2fa5-6efe-49c4-9ec0-2cb96fe52056"},"agent_status":"working","cwd":"/Users/raymonddoran/Development/odnf/qi","focused":false,"foreground_cwd":"/Users/raymonddoran/Development/odnf/qi","pane_id":"w9:p1","revision":2,"state_change_seq":127,"tab_id":"w9:t1","terminal_id":"term_65b9c3fe58ecac","terminal_title":"◑ PR merge conflicts #78 #79 #81","terminal_title_stripped":"PR merge conflicts #78 #79 #81","workspace_id":"w9"}]}}`

const fixtureWorkspaceList = `{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[` +
	`{"active_tab_id":"w1:t1","agent_status":"blocked","focused":false,"label":"TEACH","number":1,"pane_count":3,"tab_count":1,"workspace_id":"w1"},` +
	`{"active_tab_id":"w4:t1","agent_status":"unknown","focused":false,"label":"builder-hq","number":2,"pane_count":1,"tab_count":1,"workspace_id":"w4"},` +
	`{"active_tab_id":"w5:t1","agent_status":"working","focused":false,"label":"Somehow","number":3,"pane_count":3,"tab_count":1,"workspace_id":"w5"},` +
	`{"active_tab_id":"w9:t1","agent_status":"working","focused":true,"label":"qi","number":5,"pane_count":1,"tab_count":1,"workspace_id":"w9"},` +
	`{"active_tab_id":"wB:t1","agent_status":"unknown","focused":false,"label":"empty","number":7,"pane_count":0,"tab_count":0,"workspace_id":"wB"}]}}`

// pane list across all workspaces: w4 has a pane but no agent (cwd must
// still be derived), wB has no panes at all (cwd stays empty).
const fixturePaneList = `{"id":"cli:pane:list","result":{"panes":[` +
	`{"agent":"codex","agent_session":{"agent":"codex","kind":"id","source":"herdr:codex","value":"01a0a72c-4f0e-7e63-9709-60c4e3de1fe4"},"agent_status":"blocked","cwd":"/Users/raymonddoran/Development/odnf/TEACH","focused":false,"foreground_cwd":"/Users/raymonddoran/Development/odnf/TEACH","pane_id":"w1:p4","revision":28957,"tab_id":"w1:t1","terminal_id":"term_65b743d8cc5341","workspace_id":"w1"},` +
	`{"agent_status":"unknown","cwd":"/Users/raymonddoran/Development/odnf/TEACH-notes","focused":false,"foreground_cwd":"/Users/raymonddoran/Development/odnf/TEACH-notes","pane_id":"w1:p7","revision":0,"tab_id":"w1:t1","terminal_id":"term_65b743d8cc9911","workspace_id":"w1"},` +
	`{"agent_status":"unknown","cwd":"/Users/raymonddoran/Development/CC/builder-hq","focused":false,"foreground_cwd":"/Users/raymonddoran/Development/CC/builder-hq","pane_id":"w4:p1","revision":7,"tab_id":"w4:t1","terminal_id":"term_65b743d8cca122","workspace_id":"w4"},` +
	`{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"2b90dfdc-cf4b-411e-83cf-82c33218bd1a"},"agent_status":"idle","cwd":"/Users/raymonddoran/Development/odnf/Somehow","focused":false,"foreground_cwd":"/Users/raymonddoran/Development/odnf/Somehow","pane_id":"w5:p1","revision":8,"tab_id":"w5:t1","terminal_id":"term_65b743d8cf0b55","workspace_id":"w5"},` +
	`{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"a07c2fa5-6efe-49c4-9ec0-2cb96fe52056"},"agent_status":"working","cwd":"/Users/raymonddoran/Development/odnf/qi","focused":true,"foreground_cwd":"/Users/raymonddoran/Development/odnf/qi","pane_id":"w9:p1","revision":2,"tab_id":"w9:t1","terminal_id":"term_65b9c3fe58ecac","workspace_id":"w9"}],"type":"pane_list"}}`

const fixtureAgentGet = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"a07c2fa5-6efe-49c4-9ec0-2cb96fe52056"},"agent_status":"working","cwd":"/Users/raymonddoran/Development/odnf/qi","focused":false,"foreground_cwd":"/Users/raymonddoran/Development/odnf/qi","name":"qi-main","pane_id":"w9:p1","revision":2,"tab_id":"w9:t1","terminal_id":"term_65b9c3fe58ecac","terminal_title":"◑ PR merge conflicts","terminal_title_stripped":"PR merge conflicts","workspace_id":"w9"},"type":"agent_info"}}`

const fixtureAgentNotFound = `{"error":{"code":"agent_not_found","message":"agent target nope-nope not found"},"id":"cli:agent:get"}`

// --- fake runner ---------------------------------------------------------

type fakeCall struct {
	bin  string
	args []string
}

type fakeReply struct {
	stdout string
	stderr string
	code   int
	err    error
}

type fakeRunner struct {
	calls  []fakeCall
	routes map[string]fakeReply // keyed by "<verb> <noun>", e.g. "agent list"
	always *fakeReply
}

func (f *fakeRunner) run(ctx context.Context, bin string, args ...string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, fakeCall{bin: bin, args: append([]string(nil), args...)})
	if f.always != nil {
		return []byte(f.always.stdout), []byte(f.always.stderr), f.always.code, f.always.err
	}
	key := ""
	if len(args) >= 2 {
		key = args[0] + " " + args[1]
	}
	r, ok := f.routes[key]
	if !ok {
		return nil, []byte("no route for " + key), 2, nil
	}
	return []byte(r.stdout), []byte(r.stderr), r.code, r.err
}

func newHerdr(t *testing.T, routes map[string]fakeReply) (*HerdrRuntime, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{routes: routes}
	return NewHerdrRuntime("").SetRunner(f.run), f
}

func (f *fakeRunner) argsFor(t *testing.T, key string) []string {
	t.Helper()
	for _, c := range f.calls {
		if len(c.args) >= 2 && c.args[0]+" "+c.args[1] == key {
			return c.args
		}
	}
	t.Fatalf("no %q call; calls=%v", key, f.calls)
	return nil
}

// --- tests ---------------------------------------------------------------

func TestNewHerdrRuntimeDefaultsBinary(t *testing.T) {
	if got := NewHerdrRuntime("").bin; got != "herdr" {
		t.Fatalf("bin = %q, want herdr", got)
	}
	if got := NewHerdrRuntime("/opt/herdr").bin; got != "/opt/herdr" {
		t.Fatalf("bin = %q, want /opt/herdr", got)
	}
	if got := NewHerdrRuntime("").Name(); got != "herdr" {
		t.Fatalf("Name() = %q, want herdr", got)
	}
}

func TestHerdrListAgents(t *testing.T) {
	r, f := newHerdr(t, map[string]fakeReply{
		"agent list":     {stdout: fixtureAgentList},
		"workspace list": {stdout: fixtureWorkspaceList},
	})
	got, err := r.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	want := []AgentInstance{
		{
			ID: "w1:p4", Kind: KindCodex, State: StateBlocked,
			SessionID: "01a0a72c-4f0e-7e63-9709-60c4e3de1fe4",
			Workspace: Workspace{ID: "w1", Number: 1, Label: "TEACH"},
			TabID:     "w1:t1", PaneID: "w1:p4",
			Cwd:   "/Users/raymonddoran/Development/odnf/TEACH",
			Title: "[ ! ] Action Required | Create 1.0 release plan and issues | TEACH",
		},
		{
			ID: "w5:p1", Kind: KindClaude, State: StateWorking,
			SessionID: "2b90dfdc-cf4b-411e-83cf-82c33218bd1a",
			Workspace: Workspace{ID: "w5", Number: 3, Label: "Somehow"},
			TabID:     "w5:t1", PaneID: "w5:p1",
			Cwd:     "/Users/raymonddoran/Development/odnf/Somehow",
			Focused: true,
			Title:   "tier:free issues impact sort",
		},
		{
			ID: "w9:p1", Kind: KindClaude, State: StateWorking,
			SessionID: "a07c2fa5-6efe-49c4-9ec0-2cb96fe52056",
			Workspace: Workspace{ID: "w9", Number: 5, Label: "qi", Focused: true},
			TabID:     "w9:t1", PaneID: "w9:p1",
			Cwd:   "/Users/raymonddoran/Development/odnf/qi",
			Title: "PR merge conflicts #78 #79 #81",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListAgents mismatch:\n got %+v\nwant %+v", got, want)
	}
	// Two Claude instances must stay distinct instances, not one kind.
	if got[1].ID == got[2].ID || got[1].SessionID == got[2].SessionID {
		t.Fatal("the two Claude agents collapsed into one identity")
	}
	if a := f.argsFor(t, "agent list"); !reflect.DeepEqual(a, []string{"agent", "list"}) {
		t.Fatalf("agent list argv = %v", a)
	}
	if f.calls[0].bin != "herdr" {
		t.Fatalf("bin = %q", f.calls[0].bin)
	}
}

func TestHerdrListAgentsUnknownWorkspaceKeepsAgent(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent list":     {stdout: fixtureAgentList},
		"workspace list": {stdout: `{"id":"x","result":{"type":"workspace_list","workspaces":[]}}`},
	})
	got, err := r.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Workspace != (Workspace{ID: "w1"}) {
		t.Fatalf("workspace = %+v, want bare handle", got[0].Workspace)
	}
}

func TestHerdrListWorkspacesCwdDerivation(t *testing.T) {
	r, f := newHerdr(t, map[string]fakeReply{
		"workspace list": {stdout: fixtureWorkspaceList},
		"pane list":      {stdout: fixturePaneList},
	})
	got, err := r.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	want := []Workspace{
		// w1: agent pane wins over the earlier-listed non-agent pane.
		{ID: "w1", Number: 1, Label: "TEACH", Cwd: "/Users/raymonddoran/Development/odnf/TEACH"},
		// w4: no agent, so the only pane's cwd.
		{ID: "w4", Number: 2, Label: "builder-hq", Cwd: "/Users/raymonddoran/Development/CC/builder-hq"},
		{ID: "w5", Number: 3, Label: "Somehow", Cwd: "/Users/raymonddoran/Development/odnf/Somehow"},
		{ID: "w9", Number: 5, Label: "qi", Cwd: "/Users/raymonddoran/Development/odnf/qi", Focused: true},
		// wB: no panes at all.
		{ID: "wB", Number: 7, Label: "empty"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListWorkspaces mismatch:\n got %+v\nwant %+v", got, want)
	}
	if a := f.argsFor(t, "pane list"); !reflect.DeepEqual(a, []string{"pane", "list"}) {
		t.Fatalf("pane list argv = %v", a)
	}
	if len(f.calls) != 2 {
		t.Fatalf("made %d calls, want 2 (one workspace list, one pane list)", len(f.calls))
	}
}

func TestHerdrListWorkspacesToleratesPaneListFailure(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"workspace list": {stdout: fixtureWorkspaceList},
		"pane list":      {stderr: "boom", code: 2},
	})
	got, err := r.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
	for _, w := range got {
		if w.Cwd != "" {
			t.Fatalf("workspace %s has cwd %q, want empty", w.ID, w.Cwd)
		}
	}
}

func TestHerdrGetAgent(t *testing.T) {
	r, f := newHerdr(t, map[string]fakeReply{
		"agent get":      {stdout: fixtureAgentGet},
		"workspace list": {stdout: fixtureWorkspaceList},
	})
	got, err := r.GetAgent(context.Background(), "w9:p1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Name != "qi-main" || got.Kind != KindClaude || got.Workspace.Label != "qi" {
		t.Fatalf("GetAgent = %+v", got)
	}
	if a := f.argsFor(t, "agent get"); !reflect.DeepEqual(a, []string{"agent", "get", "w9:p1"}) {
		t.Fatalf("agent get argv = %v", a)
	}
}

func TestHerdrGetAgentNotFound(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent get": {stderr: fixtureAgentNotFound, code: 1},
	})
	_, err := r.GetAgent(context.Background(), "nope-nope")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("err = %v, want ErrAgentNotFound", err)
	}
	var re *RuntimeError
	if !errors.As(err, &re) {
		t.Fatalf("err = %T, want *RuntimeError", err)
	}
	if re.Code != "agent_not_found" || !strings.Contains(re.Message, "nope-nope") {
		t.Fatalf("RuntimeError = %+v", re)
	}
}

func TestHerdrFocused(t *testing.T) {
	r, f := newHerdr(t, map[string]fakeReply{"pane list": {stdout: fixturePaneList}})
	got, err := r.Focused(context.Background())
	if err != nil {
		t.Fatalf("Focused: %v", err)
	}
	want := Focus{WorkspaceID: "w9", PaneID: "w9:p1", AgentID: "w9:p1"}
	if got != want {
		t.Fatalf("Focused = %+v, want %+v", got, want)
	}
	// Never `pane current`: that resolves the calling pane, not the user's.
	if a := f.argsFor(t, "pane list"); !reflect.DeepEqual(a, []string{"pane", "list"}) {
		t.Fatalf("argv = %v", a)
	}
}

func TestHerdrFocusedPaneWithoutAgent(t *testing.T) {
	const panes = `{"id":"x","result":{"type":"pane_list","panes":[` +
		`{"agent_status":"unknown","cwd":"/tmp","focused":true,"pane_id":"w4:p1","tab_id":"w4:t1","workspace_id":"w4"}]}}`
	r, _ := newHerdr(t, map[string]fakeReply{"pane list": {stdout: panes}})
	got, err := r.Focused(context.Background())
	if err != nil {
		t.Fatalf("Focused: %v", err)
	}
	if got.AgentID != "" || got.PaneID != "w4:p1" {
		t.Fatalf("Focused = %+v", got)
	}
}

func TestHerdrFocusedNothingFocused(t *testing.T) {
	const panes = `{"id":"x","result":{"type":"pane_list","panes":[` +
		`{"agent_status":"idle","focused":false,"pane_id":"w4:p1","tab_id":"w4:t1","workspace_id":"w4"}]}}`
	r, _ := newHerdr(t, map[string]fakeReply{"pane list": {stdout: panes}})
	got, err := r.Focused(context.Background())
	if err != nil {
		t.Fatalf("Focused: %v", err)
	}
	if got != (Focus{}) {
		t.Fatalf("Focused = %+v, want zero", got)
	}
}

func TestHerdrSend(t *testing.T) {
	r, f := newHerdr(t, map[string]fakeReply{
		"agent prompt": {stdout: `{"id":"cli:agent:prompt","result":{"type":"agent_prompted","agent":{"agent_status":"working","pane_id":"w9:p1","tab_id":"w9:t1","workspace_id":"w9","focused":false}}}`},
	})
	msg := "run the tests --now; and $(echo nope)"
	if err := r.Send(context.Background(), "w9:p1", msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	want := []string{"agent", "prompt", "w9:p1", msg}
	if a := f.argsFor(t, "agent prompt"); !reflect.DeepEqual(a, want) {
		t.Fatalf("argv = %q, want %q", a, want)
	}
}

func TestHerdrSendBlocked(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent prompt": {stderr: `{"error":{"code":"agent_blocked","message":"agent w1:p4 is blocked"},"id":"cli:agent:prompt"}`, code: 1},
	})
	err := r.Send(context.Background(), "w1:p4", "hello")
	if !errors.Is(err, ErrAgentBlocked) {
		t.Fatalf("err = %v, want ErrAgentBlocked", err)
	}
}

func TestHerdrSendNotFound(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent prompt": {stderr: fixtureAgentNotFound, code: 1},
	})
	if err := r.Send(context.Background(), "nope-nope", "hello"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("err = %v, want ErrAgentNotFound", err)
	}
}

// The prompt text is user content; it must never appear in an error string
// (which callers log).
func TestHerdrSendErrorOmitsMessage(t *testing.T) {
	secret := "do-not-log-this-prompt"
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent prompt": {stderr: "usage: herdr agent prompt", code: 2},
	})
	err := r.Send(context.Background(), "w9:p1", secret)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks the prompt: %v", err)
	}
}

func TestHerdrReadOutput(t *testing.T) {
	const raw = "› Ask Codex to do anything\nline two\n"
	r, f := newHerdr(t, map[string]fakeReply{"agent read": {stdout: raw}})

	got, err := r.ReadOutput(context.Background(), "w1:p4", OutputOptions{})
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if got.Text != raw || got.Truncated {
		t.Fatalf("ReadOutput = %+v", got)
	}
	want := []string{"agent", "read", "w1:p4", "--source", "recent-unwrapped"}
	if a := f.argsFor(t, "agent read"); !reflect.DeepEqual(a, want) {
		t.Fatalf("argv = %v, want %v", a, want)
	}

	f.calls = nil
	if _, err := r.ReadOutput(context.Background(), "w1:p4", OutputOptions{Lines: 40, Source: SourceVisible}); err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	want = []string{"agent", "read", "w1:p4", "--source", "visible", "--lines", "40"}
	if a := f.argsFor(t, "agent read"); !reflect.DeepEqual(a, want) {
		t.Fatalf("argv = %v, want %v", a, want)
	}
}

func TestHerdrReadOutputNotFound(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent read": {stderr: `{"error":{"code":"agent_not_found","message":"agent target nope-nope not found"},"id":"cli:agent:read"}`, code: 1},
	})
	if _, err := r.ReadOutput(context.Background(), "nope-nope", OutputOptions{}); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("err = %v, want ErrAgentNotFound", err)
	}
}

const fixtureAgentWaited = `{"id":"cli:agent:wait","result":{"type":"agent_waited","agent":{"agent":"claude","agent_status":"idle","cwd":"/Users/raymonddoran/Development/odnf/qi","focused":false,"pane_id":"w9:p1","tab_id":"w9:t1","workspace_id":"w9"}}}`

func TestHerdrWaitForStateNoDeadline(t *testing.T) {
	r, f := newHerdr(t, map[string]fakeReply{"agent wait": {stdout: fixtureAgentWaited}})
	got, err := r.WaitForState(context.Background(), "w9:p1")
	if err != nil {
		t.Fatalf("WaitForState: %v", err)
	}
	if got != StateIdle {
		t.Fatalf("state = %q, want idle", got)
	}
	want := []string{"agent", "wait", "w9:p1"}
	if a := f.argsFor(t, "agent wait"); !reflect.DeepEqual(a, want) {
		t.Fatalf("argv = %v, want %v", a, want)
	}
}

func TestHerdrWaitForStateUntilAndTimeout(t *testing.T) {
	r, f := newHerdr(t, map[string]fakeReply{"agent wait": {stdout: fixtureAgentWaited}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := r.WaitForState(ctx, "w9:p1", StateIdle, StateDone); err != nil {
		t.Fatalf("WaitForState: %v", err)
	}
	a := f.argsFor(t, "agent wait")
	head := []string{"agent", "wait", "w9:p1", "--until", "idle", "--until", "done", "--timeout"}
	if len(a) != len(head)+1 || !reflect.DeepEqual(a[:len(head)], head) {
		t.Fatalf("argv = %v, want %v <ms>", a, head)
	}
	ms, err := strconv.Atoi(a[len(a)-1])
	if err != nil {
		t.Fatalf("timeout %q: %v", a[len(a)-1], err)
	}
	if ms < 25_000 || ms > 30_000 {
		t.Fatalf("timeout = %dms, want ~30000", ms)
	}
}

func TestHerdrWaitForStateTimeoutError(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent wait": {stderr: `{"error":{"code":"timeout","message":"timed out after 500ms"},"id":"cli:agent:wait"}`, code: 1},
	})
	_, err := r.WaitForState(context.Background(), "w9:p1")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestHerdrWaitForStateContextDeadline(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent wait": {err: context.DeadlineExceeded, code: -1},
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	_, err := r.WaitForState(ctx, "w9:p1")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to keep the context cause", err)
	}
}

func TestTimeoutMillisFloorsAtOne(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(-time.Hour))
	defer cancel()
	ms, ok := timeoutMillis(ctx, now)
	if !ok || ms != 1 {
		t.Fatalf("timeoutMillis = %d, %v; want 1, true", ms, ok)
	}
	if _, ok := timeoutMillis(context.Background(), now); ok {
		t.Fatal("no deadline should yield ok=false")
	}
}

func TestHerdrBinaryMissingIsUnavailable(t *testing.T) {
	f := &fakeRunner{always: &fakeReply{code: -1, err: exec.ErrNotFound}}
	r := NewHerdrRuntime("herdr").SetRunner(f.run)
	if _, err := r.ListAgents(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if _, err := r.Focused(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestHerdrServerUnavailableCodeMapsToUnavailable(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent list": {stderr: `{"error":{"code":"server_unavailable","message":"no herdr server"},"id":"cli:agent:list"}`, code: 1},
	})
	if _, err := r.ListAgents(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

// A CLI usage error (exit 2, plain text) is not a protocol error and must
// not be dressed up as one.
func TestHerdrExitTwoIsPlainError(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent list": {stderr: "usage: herdr agent list\n", code: 2},
	})
	_, err := r.ListAgents(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	var re *RuntimeError
	if errors.As(err, &re) {
		t.Fatalf("got RuntimeError %+v, want plain error", re)
	}
	if !strings.Contains(err.Error(), "usage: herdr agent list") || !strings.Contains(err.Error(), "exit 2") {
		t.Fatalf("error = %v", err)
	}
}

func TestHerdrUnexpectedResultType(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent list": {stdout: `{"id":"x","result":{"type":"pane_list","panes":[]}}`},
	})
	_, err := r.ListAgents(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected result type") {
		t.Fatalf("err = %v, want unexpected result type", err)
	}
}

func TestHerdrMalformedJSON(t *testing.T) {
	r, _ := newHerdr(t, map[string]fakeReply{"agent list": {stdout: "not json"}})
	if _, err := r.ListAgents(context.Background()); err == nil {
		t.Fatal("want error")
	}
	r2, _ := newHerdr(t, map[string]fakeReply{"agent list": {stdout: `{"id":"x"}`}})
	if _, err := r2.ListAgents(context.Background()); err == nil || !strings.Contains(err.Error(), "no result") {
		t.Fatalf("err = %v, want missing-result error", err)
	}
}

func TestParseHerdrError(t *testing.T) {
	if got := parseHerdrError([]byte("usage: herdr")); got != nil {
		t.Fatalf("plain stderr = %+v, want nil", got)
	}
	if got := parseHerdrError([]byte(`{"id":"x"}`)); got != nil {
		t.Fatalf("envelope without error = %+v, want nil", got)
	}
	got := parseHerdrError([]byte("  " + fixtureAgentNotFound + "\n"))
	if got == nil || got.Code != "agent_not_found" {
		t.Fatalf("got %+v", got)
	}
}

func TestAgentInfoNullFields(t *testing.T) {
	const panes = `{"id":"x","result":{"type":"agent_list","agents":[` +
		`{"agent":null,"agent_session":null,"agent_status":"unknown","name":null,"pane_id":"w4:p1","tab_id":"w4:t1","workspace_id":"w4","focused":false,"foreground_cwd":"/tmp/fg","terminal_title":"raw title"}]}}`
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent list":     {stdout: panes},
		"workspace list": {stdout: fixtureWorkspaceList},
	})
	got, err := r.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	a := got[0]
	if a.Kind != "" || a.Name != "" || a.SessionID != "" {
		t.Fatalf("null fields leaked: %+v", a)
	}
	if a.State != StateUnknown {
		t.Fatalf("state = %q", a.State)
	}
	if a.Cwd != "/tmp/fg" {
		t.Fatalf("cwd = %q, want the foreground_cwd fallback", a.Cwd)
	}
	if a.Title != "raw title" {
		t.Fatalf("title = %q, want the terminal_title fallback", a.Title)
	}
}

// A "path" session is a transcript file, not an identity, so it is not a
// SessionID.
func TestAgentSessionPathKindIsNotASessionID(t *testing.T) {
	const payload = `{"id":"x","result":{"type":"agent_list","agents":[` +
		`{"agent":"claude","agent_session":{"agent":"claude","kind":"path","source":"herdr:claude","value":"/tmp/session.jsonl"},"agent_status":"idle","pane_id":"w4:p1","tab_id":"w4:t1","workspace_id":"w4","focused":false}]}}`
	r, _ := newHerdr(t, map[string]fakeReply{
		"agent list":     {stdout: payload},
		"workspace list": {stdout: fixtureWorkspaceList},
	})
	got, err := r.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if got[0].SessionID != "" {
		t.Fatalf("SessionID = %q, want empty", got[0].SessionID)
	}
}

func TestHerdrSend_LeadingDashRefusedBeforeExec(t *testing.T) {
	calls := 0
	rt := NewHerdrRuntime("herdr").SetRunner(func(ctx context.Context, bin string, args ...string) ([]byte, []byte, int, error) {
		calls++
		return nil, nil, 0, nil
	})
	err := rt.Send(context.Background(), "w9:p1", "--force rebuild the index")
	if err == nil || calls != 0 {
		t.Fatalf("err=%v calls=%d; want refusal with no exec", err, calls)
	}
	if strings.Contains(err.Error(), "rebuild the index") {
		t.Errorf("error leaks the prompt: %v", err)
	}
}

func TestHerdrSend_UsageErrorStderrWithheld(t *testing.T) {
	rt := NewHerdrRuntime("herdr").SetRunner(func(ctx context.Context, bin string, args ...string) ([]byte, []byte, int, error) {
		return nil, []byte("unknown option: secret prompt text"), 2, nil
	})
	err := rt.Send(context.Background(), "w9:p1", "secret prompt text")
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "secret prompt text") {
		t.Errorf("error leaks stderr/prompt: %v", err)
	}
}
