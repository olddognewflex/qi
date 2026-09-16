# Herdr as qi's agent runtime

qi addresses coding-agent **instances** (Claude Code, Codex, ...) that run in
terminals managed by [Herdr](https://herdr.dev). This document records what
was discovered about Herdr on the reference machine, the decisions taken, and
the boundary between the two tools.

## Boundary

| Herdr owns | qi owns |
|---|---|
| terminals, panes, tabs, workspaces | user interaction, voice |
| process lifetime | conversational context |
| agent discovery and identity | intent resolution ("who do I mean?") |
| agent lifecycle state (`working`/`blocked`/`done`/`idle`/`unknown`) | agent *selection* (asking when ambiguous) |
| sending input to / reading output from a pane | orchestration, memory, response shaping |

qi never keeps its own registry of agent processes and never re-derives agent
state from terminal text. Everything qi knows about *where* an agent is and
*what it is doing* comes through the `agentrt.Runtime` interface.

```
            ┌──────────────────────┐
            │          qi          │  internal/voice   (intent, context, I/O)
            │  "who do I mean?"    │  internal/service (AgentService: resolve/ask)
            └──────────┬───────────┘
                       │ agentrt.Runtime
            ┌──────────▼───────────┐
            │  HerdrRuntime        │  internal/agentrt/herdr.go  (herdr CLI, JSON)
            │  LocalRuntime        │  internal/agentrt/local.go  (no agents)
            └──────────────────────┘
```

## What Herdr exposes (verified against herdr 0.9.0, protocol 22)

Every control command returns JSON on stdout and exits 0; server errors are a
JSON `{"error":{"code","message"},"id"}` object on **stderr** with exit 1;
CLI syntax errors exit 2. Bare `herdr` launches the TUI and must never be run
for discovery.

| Need | Command | Notes |
|---|---|---|
| workspaces + labels + focus | `herdr workspace list` | `WorkspaceInfo` has **no cwd**; derived from panes/agents |
| all panes (cwd, focus) | `herdr pane list` | one call covers every workspace; `--workspace <id>` narrows. `panes[].cwd` gives a workspace's cwd (agent pane, else focused pane, else first) |
| all live agents | `herdr agent list` | `agent` (kind), `agent_status`, `agent_session.value` (the agent's own session UUID), `pane_id`, `workspace_id`, `cwd`, `focused`, optional unique `name` |
| one agent | `herdr agent get <pane-id \| name>` | `agent_not_found` |
| focused pane / agent | `herdr pane list` → the pane with `focused: true` | `herdr pane current` returns the *calling* pane (qi itself) with or without `--current`, even with `HERDR_*` unset — verified live; it is not the UI focus |
| send an instruction | `herdr agent prompt <target> <text>` | rejects with `agent_blocked` before writing anything |
| wait for a state | `herdr agent wait <target> [--until s]... [--timeout ms]` | default matches `idle\|done\|blocked` |
| read output | `herdr agent read <target> --source recent-unwrapped --lines N` | prints raw text |
| environment | `HERDR_ENV=1`, `HERDR_BIN_PATH`, `HERDR_SOCKET_PATH`, `HERDR_WORKSPACE_ID`, `HERDR_TAB_ID`, `HERDR_PANE_ID` | injected into every managed pane |
| liveness from outside a pane | `herdr status --json` | `server.running` |

Observed `result.type` discriminators, which `HerdrRuntime` pins and treats
a mismatch of as an error: `workspace list` → `workspace_list`, `pane list` →
`pane_list`, `agent list` → `agent_list`, `agent get` → `agent_info`,
`agent prompt` → `agent_prompted`, `pane current` → `pane_current`. `agent
wait` returns an `agent_info`-shaped payload but its type string was not
observed live (waiting on a real agent was out of scope), so it is not
pinned. Re-verify these against `herdr api schema --json`
(`schemas.success_response.$defs.ResponseResult`) after a Herdr upgrade.

Identity model: a Herdr **pane ID** (`w9:p1`) is the stable handle for the
agent currently in that pane; `agent_session.value` is the agent's *own*
session id (e.g. the Claude Code session UUID). Two Claude Code processes are
therefore two `AgentInstance`s with different `ID`, `PaneID` and `SessionID`
even when they share a workspace. Herdr may also assign a unique live
**name** (`[a-z][a-z0-9_-]{0,31}`); qi accepts it as a target when present.

## Decisions

1. **Transport: the `herdr` CLI, one process per call.** The unix socket
   speaks a *private*, per-release protocol whose compatibility the CLI
   negotiates (`herdr status` reports `private_protocol_compatible`). The CLI
   is the documented public surface, emits stable JSON, and every operation
   qi needs is a request/response — `agent prompt --wait` and `agent wait`
   block server-side, so no event stream is needed for the first slice.
   `events.subscribe` exists on the socket and is the natural path for the
   future "when Codex finishes, have Claude review it" orchestration; the
   `Runtime` interface leaves room for a `Watch`-style method without
   changing callers.
2. **No long-lived connection.** `HerdrRuntime` holds only a binary path and
   an injectable process runner. Each call is independent, cancellable via
   context (`--timeout` is derived from the context deadline), and testable
   without Herdr.
3. **Detection, not dependency.** `agentrt.Detect` picks Herdr when
   `HERDR_ENV=1` (qi is inside a managed pane) or when a `herdr` binary is on
   `PATH` and its socket exists (qi in a plain terminal while Herdr runs);
   otherwise `LocalRuntime`, which knows no agents and says so. `[agents]
   runtime = "herdr" | "local" | "auto"` overrides. Herdr is never a build or
   runtime requirement.
4. **Resolution asks, never guesses.** `service.AgentService.Resolve` narrows
   the live instances by kind / workspace (label or id) / name / focus / cwd.
   One match proceeds; zero returns a speakable `NoAgentError`; several
   return an `AmbiguousAgentError` whose `Prompt()` names each candidate's
   pane and state so the user can choose ("the second one", "the idle one",
   "pane 5-2").
5. **Intent parsing is deterministic.** `internal/voice.Parse` is a small
   grammar (status / instruct / clarify / quit), not an LLM call, which keeps
   invariant #3 (no silent AI) intact. Conversational references ("Have
   Claude review *that*") resolve against `voice.Context`: the last agent
   addressed of that kind is *preferred* (by pane id and session id), so a
   follow-up does not re-ask — but only while that pane still hosts the
   same agent; if its occupant changed, qi asks again rather than relaying
   to a stranger. An utterance naming two agents ("tell Claude and Codex
   ...") is refused, never split.
6. **Speech I/O is pluggable and optional.** `qi voice --text` (typed
   utterances) is the reference path and what the tests drive. Real STT is
   `[voice] stt = "http"`: ffmpeg captures a fixed-length clip from the
   macOS microphone and an OpenAI-compatible `/v1/audio/transcriptions`
   endpoint (local Whisper server or hosted) transcribes it. TTS is macOS
   `say`. Neither is exercised by CI; the loop above them is.

## Not yet

- Cross-agent orchestration ("when Codex finishes, have Claude review its
  changes"): parsed as unknown today; needs a lifecycle stream
  (`events.subscribe`) and a small scheduler in qi.
- Output summarisation is a deterministic tail of the agent's recent output.
  An opt-in LLM summary would go through the existing `[ai]` providers.
- Remote Herdr machines: IDs and names are per-server; qi targets the
  session the `herdr` binary resolves.
