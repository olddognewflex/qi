# AGENTS.md — qi

## Status
**Implemented and evolving.** Core CLI, notes + FTS index, calendar, the `qid` daemon, the `qi-mcp` server, and the AI planner all exist. `docs/qi-blueprint-v2.md` is the original design intent; this file and `CLAUDE.md` describe what is actually built.

---

## What this is
Go CLI productivity tool over an Obsidian-compatible markdown vault. Three binaries:
- `qi` — stateless CLI (fast, no AI on the hot path)
- `qid` — background daemon: hosts the tool registry and serves a JSON-RPC 2.0 API over a unix-domain socket
- `qi-mcp` — MCP server: re-publishes qid's tools to AI clients (e.g. Claude Desktop) over stdio

`qi` works standalone. `qid` and `qi-mcp` are only needed for the tool/AI surface.

---

## Architectural rules

1. Markdown files in the Obsidian vault are the canonical source of truth.
2. **Capture latency < 100ms.** `qi capture` must never call network, AI, or block. No exceptions.
3. SQLite is a derived per-machine index only — fully rebuildable from markdown.
4. Commands stay thin. Business logic belongs in `internal/service`.
5. Vault parsing and formatting belongs in `internal/vault`.
6. Domain types belong in `internal/domain`.
7. AI features must be explicit and opt-in. No silent LLM usage in normal commands.
8. Any AI-proposed mutation must require user confirmation before writing — enforced by the qid policy gate + approval queue, not by convention.

---

## Directory structure

```
qi/
├── cmd/
│    ├── qi/          # CLI entrypoint
│    ├── qid/         # Daemon entrypoint
│    └── qi-mcp/      # MCP server entrypoint
├── internal/
│    ├── commands/    # Thin Cobra handlers — no logic. `ai.go` = qid client
│    ├── service/     # All business logic (TaskService, CaptureService, ...)
│    ├── domain/      # Domain types (Task, Note, Event)
│    ├── vault/       # Markdown read/write (Obsidian task-line format)
│    ├── index/       # SQLite FTS5 note index (derived state)
│    ├── calendar/    # Provider interface: Local / ICS / CalDAV / Google
│    ├── tui/         # Bubble Tea pickers
│    ├── config/      # TOML loader + env overrides
│    ├── tools/       # qid tool registry (Local / MCP / Skill sources)
│    │    └── builtin/ # Compiled-in local tools (capture, task.add, task.list, note.search, agenda.today)
│    ├── skills/      # Deterministic composed workflows (daily-review, process-inbox[-apply])
│    ├── daemon/      # JSON-RPC 2.0 server + client (unix socket)
│    ├── policy/      # allow / queue / refuse decisions per caller
│    ├── approval/    # Approval queue + append-only JSONL audit log
│    ├── mcp/         # qid → external MCP servers (inbound tools)
│    ├── qimcp/       # qid → AI clients via MCP (outbound bridge)
│    └── ai/          # LLM tool-use planner (Anthropic, Ollama)
└── Makefile
```

Commands are thin. All logic lives in the service layer (CLI) or the tools/skills/policy layers (daemon).

---

## The daemon trust model

Every tool call flows through one registry and one policy gate. The **caller identity** drives the decision:

- `cli` — interactive human via `qi`; runs immediately.
- read-only tools — run immediately regardless of caller.
- `ai-planner:<id>` (the `qi ai run` planner) and `mcp:<id>` (an external AI client via `qi-mcp`) — any **mutating** call routes through the approval queue. A human approves or denies with `qi ai approve <id>` / `qi ai deny <id>`. Every transition is written to the append-only audit log.

No code path may let a non-cli caller mutate state without passing through `internal/policy` + `internal/approval`.

---

## Storage paths

```
vault/                # canonical, Obsidian-compatible, may be synced
├── 00-inbox/         # capture always writes here (timestamped files)
├── 10-tasks/inbox.md
├── 20-notes/
└── 30-daily/YYYY-MM-DD.md   # local calendar source

~/.local/share/qi/    # machine-local (XDG_DATA_HOME)
└── qi.db             # SQLite FTS5 note index

~/.local/state/qi/    # machine-local (XDG_RUNTIME_DIR → XDG_STATE_HOME → here)
├── qid.sock          # daemon unix socket (0600)
└── audit.log         # approval audit log (JSONL)

~/.config/qi/
└── config.toml       # vault_path, calendars, [[mcp_servers]], [ai]
```

Secrets (e.g. CalDAV passwords) → system keychain.

---

## Domain types (Go)

```go
type Task struct {
    ID, Text, Project string
    Tags              []string
    Due               *time.Time
    Priority          string
    Completed         bool
    CompletedAt       *time.Time
    FilePath          string
    LineNumber        int
}
```

---

## Git hooks

Plugin-style hook system — hooks delegate to enabled plugins via:
```
git config hooks.enabled-plugins <plugin-name>
```
Plugins live in `.git/hooks/<plugin>/`. Active plugins run on pre-commit, commit-msg, pre-push, etc. Check `git config --get-all hooks.enabled-plugins` before assuming hooks are inactive.

---

## MCP tools

qid exposes its registered tools to AI clients through `qi-mcp`. Tools come from three sources: compiled-in local Go (`internal/tools/builtin`), connected external MCP servers (namespaced `mcp.<serverID>.<toolName>`), and deterministic skills (`internal/skills`). Mutating tools surface to AI clients as approval-pending and are gated behind `qi ai approve`.

Current catalog (local + skill sources):

| Tool | Source | Mutating | Purpose |
|---|---|---|---|
| `vault.capture` | builtin | yes | Write a timestamped capture to `00-inbox/` |
| `task.add` | builtin | yes | Append a task (`text`, `project`, `due`, `scheduled`) |
| `task.list` | builtin | no | List tasks (`query`, `project`, `all`) |
| `note.search` | builtin | no | FTS5 search over notes (`query`) |
| `agenda.today` | builtin | no | Today's events |
| `skill.daily-review` | skill | no | Agenda + open tasks + recent captures |
| `skill.process-inbox` | skill | no | Propose task/note/archive per `00-inbox/` capture |
| `skill.process-inbox-apply` | skill | yes | Execute one proposed inbox action (`path`, `action`) |

---

## Coding rules

- Prefer small packages and small functions.
- Add tests for parsers and service behavior; vault round-trip tests are load-bearing.
- Keep output predictable.
- Preserve markdown compatibility with Obsidian task lines.
- Avoid OS-specific behavior in the core packages.

---

## Qi's role boundary

Qi is not a general autonomous agent runner.

**Qi may:**
- Expose local tools
- Execute explicit commands
- Run deterministic skills
- Propose AI-assisted actions
- Require approval for mutations

**Qi may not:**
- Autonomously chain arbitrary tools
- Silently mutate calendars, notes, tasks, or files
- Hide tool execution
- Invent new workflows without user intent
- Become a second operating system made of vibes and JSON

---

## What AI may NOT do

- Silent vault writes
- Auto task creation without confirmation
- Any mutation without explicit user approval
