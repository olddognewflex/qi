package agentrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// HerdrRuntime addresses agents through the `herdr` CLI, one short-lived
// exec per call.
//
// Why the CLI and not the unix socket: herdr's socket speaks a *private*
// versioned protocol (protocol 22 at time of writing) whose compatibility
// the CLI itself manages. The CLI is the documented public surface, so
// shelling out keeps qi off a private wire format that can change between
// herdr releases. Nothing here needs a long-lived connection: the blocking
// operations (`agent prompt --wait`, `agent wait`) block server-side, and
// lifecycle streaming (`events.subscribe`) is deferred until qi needs
// cross-agent orchestration.
//
// Every call is stateless and re-discovers what it needs, so a herdr
// restart or a pane close never leaves this struct holding stale handles.
type HerdrRuntime struct {
	bin string
	run Runner
}

// Runner executes one herdr invocation. It is injected so tests never exec
// a real binary. err is non-nil only for failures to *run* the command (a
// missing binary, a context cancel); a command that ran and failed reports
// a non-zero exitCode with err nil.
type Runner func(ctx context.Context, bin string, args ...string) (stdout, stderr []byte, exitCode int, err error)

var _ Runtime = (*HerdrRuntime)(nil)

// NewHerdrRuntime returns a runtime driving the herdr binary at bin. An
// empty bin means "herdr", resolved via PATH at call time.
func NewHerdrRuntime(bin string) *HerdrRuntime {
	if bin == "" {
		bin = "herdr"
	}
	return &HerdrRuntime{bin: bin, run: execRunner}
}

// SetRunner replaces the command runner (tests). It returns the receiver
// so it can be chained onto NewHerdrRuntime.
func (r *HerdrRuntime) SetRunner(run Runner) *HerdrRuntime {
	if run != nil {
		r.run = run
	}
	return r
}

func (r *HerdrRuntime) Name() string { return "herdr" }

// execRunner is the default Runner: one os/exec call bounded by ctx.
func execRunner(ctx context.Context, bin string, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out, errOut := []byte(stdout.String()), []byte(stderr.String())
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// The command ran and failed: that is a protocol outcome, not
			// a runner failure.
			return out, errOut, ee.ExitCode(), nil
		}
		return out, errOut, -1, err
	}
	return out, errOut, 0, nil
}

// --- wire shapes ---------------------------------------------------------

// herdrResult is the success envelope: {"id":...,"result":{"type":...}}.
type herdrResult struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
}

// herdrErrorEnvelope is the failure envelope printed on *stderr* with exit 1.
type herdrErrorEnvelope struct {
	ID    string `json:"id"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type herdrTyped struct {
	Type string `json:"type"`
}

// herdrAgentInfo mirrors herdr's AgentInfo. Only the fields qi maps are
// declared; herdr adds fields freely and unknown ones must stay harmless.
// agent and name are pointers because herdr sends JSON null for "no agent
// kind detected" / "unnamed", which must not be confused with "".
type herdrAgentInfo struct {
	Agent        *string `json:"agent"`
	AgentSession *struct {
		Agent  string `json:"agent"`
		Kind   string `json:"kind"`
		Source string `json:"source"`
		Value  string `json:"value"`
	} `json:"agent_session"`
	AgentStatus           string  `json:"agent_status"`
	Cwd                   string  `json:"cwd"`
	ForegroundCwd         string  `json:"foreground_cwd"`
	Focused               bool    `json:"focused"`
	Name                  *string `json:"name"`
	PaneID                string  `json:"pane_id"`
	TabID                 string  `json:"tab_id"`
	WorkspaceID           string  `json:"workspace_id"`
	TerminalTitle         string  `json:"terminal_title"`
	TerminalTitleStripped string  `json:"terminal_title_stripped"`
}

type herdrWorkspaceInfo struct {
	WorkspaceID string `json:"workspace_id"`
	Number      int    `json:"number"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	ActiveTabID string `json:"active_tab_id"`
	AgentStatus string `json:"agent_status"`
}

// --- exec plumbing -------------------------------------------------------

// call runs herdr and returns stdout. label is a message-free description
// of the command used in error text: argv is never echoed, because
// `agent prompt` carries the user's prompt and must not leak into logs or
// wrapped errors.
func (r *HerdrRuntime) call(ctx context.Context, label string, args ...string) ([]byte, error) {
	stdout, stderr, code, err := r.run(ctx, r.bin, args...)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("herdr %s: %w: %w", label, ErrUnavailable, err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("herdr %s: %w", label, ctxErr)
		}
		return nil, fmt.Errorf("herdr %s: %w", label, err)
	}
	if code == 0 {
		return stdout, nil
	}
	if rErr := parseHerdrError(stderr); rErr != nil {
		return nil, rErr
	}
	// Exit 2 (CLI usage error) and any other non-JSON failure: surface the
	// raw stderr, which is plain text — except for agent prompt, whose usage
	// errors can quote the prompt itself.
	if label == promptLabel {
		return nil, fmt.Errorf("herdr %s: exit %d (stderr withheld: it may quote the prompt)", label, code)
	}
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		msg = strings.TrimSpace(string(stdout))
	}
	return nil, fmt.Errorf("herdr %s: exit %d: %s", label, code, msg)
}

// callJSON runs herdr, unwraps the success envelope and verifies the
// result discriminator. wantType may be "" for results whose type string
// this package deliberately does not pin.
func (r *HerdrRuntime) callJSON(ctx context.Context, label, wantType string, args ...string) (json.RawMessage, error) {
	stdout, err := r.call(ctx, label, args...)
	if err != nil {
		return nil, err
	}
	var env herdrResult
	if err := json.Unmarshal(stdout, &env); err != nil {
		return nil, fmt.Errorf("herdr %s: decoding response: %w", label, err)
	}
	if len(env.Result) == 0 {
		return nil, fmt.Errorf("herdr %s: response has no result", label)
	}
	if wantType != "" {
		var typed herdrTyped
		if err := json.Unmarshal(env.Result, &typed); err != nil {
			return nil, fmt.Errorf("herdr %s: decoding result type: %w", label, err)
		}
		if typed.Type != wantType {
			return nil, fmt.Errorf("herdr %s: unexpected result type %q (want %q)", label, typed.Type, wantType)
		}
	}
	return env.Result, nil
}

// parseHerdrError decodes herdr's stderr error envelope. It returns nil
// when stderr is not that envelope (a CLI usage error, a panic, nothing).
func parseHerdrError(stderr []byte) *RuntimeError {
	trimmed := strings.TrimSpace(string(stderr))
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var env herdrErrorEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil || env.Error == nil || env.Error.Code == "" {
		return nil
	}
	return &RuntimeError{
		Code:    env.Error.Code,
		Message: env.Error.Message,
		Err:     sentinelForHerdrCode(env.Error.Code),
	}
}

// sentinelForHerdrCode maps herdr's error codes onto qi's runtime-agnostic
// sentinels. An unmapped code yields nil: the RuntimeError still carries
// the code verbatim, so callers lose nothing.
func sentinelForHerdrCode(code string) error {
	switch code {
	case "agent_not_found", "pane_not_found", "workspace_not_found":
		return ErrAgentNotFound
	case "agent_blocked":
		return ErrAgentBlocked
	case "timeout":
		return ErrTimeout
	case "agent_prompt_stalled":
		return ErrStalled
	case "server_unavailable", "not_connected":
		return ErrUnavailable
	}
	return nil
}

// --- mapping -------------------------------------------------------------

func (a herdrAgentInfo) kind() Kind {
	if a.Agent == nil {
		return ""
	}
	return Kind(strings.ToLower(strings.TrimSpace(*a.Agent)))
}

func (a herdrAgentInfo) state() State {
	if a.AgentStatus == "" {
		return StateUnknown
	}
	return State(a.AgentStatus)
}

// sessionID is the agent's own session identity, e.g. a Claude Code session
// UUID. herdr also reports kind "path" sessions (a transcript file rather
// than an identity), which qi does not treat as a session ID.
func (a herdrAgentInfo) sessionID() string {
	if a.AgentSession == nil || a.AgentSession.Kind != "id" {
		return ""
	}
	return a.AgentSession.Value
}

// cwdOf prefers the pane's recorded cwd and falls back to the foreground
// process's cwd, which herdr reports separately and which is the only one
// populated for some shells.
func (a herdrAgentInfo) cwdOf() string {
	if a.Cwd != "" {
		return a.Cwd
	}
	return a.ForegroundCwd
}

func (a herdrAgentInfo) title() string {
	if a.TerminalTitleStripped != "" {
		return a.TerminalTitleStripped
	}
	return a.TerminalTitle
}

func (a herdrAgentInfo) name() string {
	if a.Name == nil {
		return ""
	}
	return *a.Name
}

func workspaceFromInfo(w herdrWorkspaceInfo) Workspace {
	return Workspace{
		ID:      w.WorkspaceID,
		Number:  w.Number,
		Label:   w.Label,
		Focused: w.Focused,
	}
}

func (r *HerdrRuntime) agentFrom(a herdrAgentInfo, byWorkspace map[string]Workspace) AgentInstance {
	ws, ok := byWorkspace[a.WorkspaceID]
	if !ok {
		// A workspace herdr created between our two calls: keep the handle
		// rather than dropping the agent.
		ws = Workspace{ID: a.WorkspaceID}
	}
	return AgentInstance{
		ID:        AgentID(a.PaneID),
		Kind:      a.kind(),
		Name:      a.name(),
		State:     a.state(),
		SessionID: a.sessionID(),
		Workspace: ws,
		TabID:     a.TabID,
		PaneID:    a.PaneID,
		Cwd:       a.cwdOf(),
		Focused:   a.Focused,
		Title:     a.title(),
	}
}

// --- Runtime -------------------------------------------------------------

// ListWorkspaces returns every workspace herdr knows, with a best-effort
// Cwd.
//
// herdr's WorkspaceInfo carries no cwd, so it is derived from the
// workspace's panes in one extra `pane list` call (which covers every
// workspace at once — cheaper than one call per workspace). Preference
// order within a workspace: the cwd of a pane hosting an agent, then the
// focused pane's, then the first pane's. A failing pane list degrades to
// workspaces without cwd rather than failing the listing, since cwd is
// documented as best-effort.
func (r *HerdrRuntime) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	raw, err := r.callJSON(ctx, "workspace list", "workspace_list", "workspace", "list")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Workspaces []herdrWorkspaceInfo `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("herdr workspace list: decoding workspaces: %w", err)
	}

	cwds := r.workspaceCwds(ctx)
	out := make([]Workspace, 0, len(payload.Workspaces))
	for _, w := range payload.Workspaces {
		ws := workspaceFromInfo(w)
		ws.Cwd = cwds[w.WorkspaceID]
		out = append(out, ws)
	}
	return out, nil
}

// workspaceCwds derives one cwd per workspace from a single `pane list`.
func (r *HerdrRuntime) workspaceCwds(ctx context.Context) map[string]string {
	raw, err := r.callJSON(ctx, "pane list", "pane_list", "pane", "list")
	if err != nil {
		return nil
	}
	var payload struct {
		Panes []herdrAgentInfo `json:"panes"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	type candidate struct {
		agent, focused, first string
	}
	picks := map[string]*candidate{}
	for _, p := range payload.Panes {
		cwd := p.cwdOf()
		if cwd == "" {
			continue
		}
		c := picks[p.WorkspaceID]
		if c == nil {
			c = &candidate{}
			picks[p.WorkspaceID] = c
		}
		if c.first == "" {
			c.first = cwd
		}
		if c.focused == "" && p.Focused {
			c.focused = cwd
		}
		if c.agent == "" && p.kind() != "" {
			c.agent = cwd
		}
	}
	out := make(map[string]string, len(picks))
	for id, c := range picks {
		switch {
		case c.agent != "":
			out[id] = c.agent
		case c.focused != "":
			out[id] = c.focused
		default:
			out[id] = c.first
		}
	}
	return out
}

// ListAgents returns every agent herdr recognises, across all workspaces.
// The companion `workspace list` call supplies each agent's workspace
// label and number, which AgentInfo does not carry.
func (r *HerdrRuntime) ListAgents(ctx context.Context) ([]AgentInstance, error) {
	raw, err := r.callJSON(ctx, "agent list", "agent_list", "agent", "list")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Agents []herdrAgentInfo `json:"agents"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("herdr agent list: decoding agents: %w", err)
	}
	byWorkspace, err := r.workspacesByID(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AgentInstance, 0, len(payload.Agents))
	for _, a := range payload.Agents {
		out = append(out, r.agentFrom(a, byWorkspace))
	}
	return out, nil
}

func (r *HerdrRuntime) workspacesByID(ctx context.Context) (map[string]Workspace, error) {
	raw, err := r.callJSON(ctx, "workspace list", "workspace_list", "workspace", "list")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Workspaces []herdrWorkspaceInfo `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("herdr workspace list: decoding workspaces: %w", err)
	}
	out := make(map[string]Workspace, len(payload.Workspaces))
	for _, w := range payload.Workspaces {
		out[w.WorkspaceID] = workspaceFromInfo(w)
	}
	return out, nil
}

// GetAgent resolves one agent by pane ID or unique live agent name.
func (r *HerdrRuntime) GetAgent(ctx context.Context, id AgentID) (AgentInstance, error) {
	raw, err := r.callJSON(ctx, "agent get", "agent_info", "agent", "get", string(id))
	if err != nil {
		return AgentInstance{}, err
	}
	var payload struct {
		Agent herdrAgentInfo `json:"agent"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return AgentInstance{}, fmt.Errorf("herdr agent get: decoding agent: %w", err)
	}
	// Best-effort workspace enrichment: a missing workspace list must not
	// fail a lookup that already succeeded.
	byWorkspace, _ := r.workspacesByID(ctx)
	return r.agentFrom(payload.Agent, byWorkspace), nil
}

// Focused reports the UI-focused workspace/pane.
//
// It reads `pane list` and picks the pane herdr flags as focused, rather
// than `herdr pane current`. Verified live: `pane current` resolves the
// *calling* pane (with or without --current, and even with the HERDR_*
// env vars stripped), so when qi runs inside a herdr pane it would report
// qi's own pane as the user's focus. The focused flag on pane list is
// authoritative and agrees with workspace list's focused workspace.
func (r *HerdrRuntime) Focused(ctx context.Context) (Focus, error) {
	raw, err := r.callJSON(ctx, "pane list", "pane_list", "pane", "list")
	if err != nil {
		return Focus{}, err
	}
	var payload struct {
		Panes []herdrAgentInfo `json:"panes"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Focus{}, fmt.Errorf("herdr pane list: decoding panes: %w", err)
	}
	for _, p := range payload.Panes {
		if !p.Focused {
			continue
		}
		f := Focus{WorkspaceID: p.WorkspaceID, PaneID: p.PaneID}
		if p.kind() != "" {
			f.AgentID = AgentID(p.PaneID)
		}
		return f, nil
	}
	// Nothing focused (the herdr UI is not frontmost): a zero Focus, not
	// an error — callers treat it as "no implied target".
	return Focus{}, nil
}

// Send submits message as a prompt and returns as soon as herdr accepts
// it. It deliberately does not pass --wait: waiting is WaitForState's job,
// so a caller can send to several agents before blocking on any of them.
// message is passed as a single argv element; no shell is involved.
//
// herdr 0.9.0 does not honour a "--" positional separator, so a message
// beginning with "-" would be parsed as a flag and echoed back in a usage
// error. It is refused here, before any exec, and call() withholds raw
// stderr for this command so the prompt text can never reach an error.
func (r *HerdrRuntime) Send(ctx context.Context, id AgentID, message string) error {
	if strings.HasPrefix(strings.TrimSpace(message), "-") {
		return fmt.Errorf("herdr agent prompt: instruction may not begin with \"-\" (herdr would read it as a flag)")
	}
	_, err := r.call(ctx, promptLabel, "agent", "prompt", string(id), message)
	return err
}

// promptLabel marks the one call whose argv carries user prose; call()
// never surfaces raw stderr under this label.
const promptLabel = "agent prompt"

// ReadOutput snapshots the agent's terminal text. `herdr agent read`
// prints raw text on stdout (not a JSON envelope), so there is nothing to
// unwrap and no truncation flag to report.
func (r *HerdrRuntime) ReadOutput(ctx context.Context, id AgentID, opts OutputOptions) (AgentOutput, error) {
	source := opts.Source
	if source == "" {
		source = SourceRecentUnwrapped
	}
	args := []string{"agent", "read", string(id), "--source", string(source)}
	if opts.Lines > 0 {
		args = append(args, "--lines", strconv.Itoa(opts.Lines))
	}
	stdout, err := r.call(ctx, "agent read", args...)
	if err != nil {
		return AgentOutput{}, err
	}
	return AgentOutput{Text: string(stdout)}, nil
}

// WaitForState blocks until the agent settles into one of states (herdr's
// own default, idle|done|blocked, when none are given).
//
// The wait blocks server-side, so ctx's deadline is translated into
// herdr's --timeout: without it a cancelled ctx would kill the CLI but
// leave the caller with an exec error rather than ErrTimeout.
func (r *HerdrRuntime) WaitForState(ctx context.Context, id AgentID, states ...State) (State, error) {
	args := []string{"agent", "wait", string(id)}
	for _, s := range states {
		args = append(args, "--until", string(s))
	}
	if ms, ok := timeoutMillis(ctx, time.Now()); ok {
		args = append(args, "--timeout", strconv.FormatInt(ms, 10))
	}
	// agent wait's result discriminator is not pinned here: it could not be
	// verified live (running a wait against a real agent is not a read-only
	// operation), and the agent payload is what matters.
	raw, err := r.callJSON(ctx, "agent wait", "", args...)
	if err != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
			return "", fmt.Errorf("herdr agent wait: %w: %w", ErrTimeout, ctxErr)
		}
		return "", err
	}
	var payload struct {
		Agent herdrAgentInfo `json:"agent"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("herdr agent wait: decoding agent: %w", err)
	}
	return payload.Agent.state(), nil
}

// timeoutMillis converts ctx's deadline into herdr's --timeout argument.
// An already-expired deadline yields 1ms rather than 0 or a negative
// value, so herdr fails fast with its own timeout error instead of
// treating the flag as "wait forever".
func timeoutMillis(ctx context.Context, now time.Time) (int64, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, false
	}
	ms := deadline.Sub(now).Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return ms, true
}
