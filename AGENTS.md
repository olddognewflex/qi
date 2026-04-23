# AGENTS.md — qi

## Status
**Pre-implementation.** No code exists. Only source of truth: `docs/qi-blueprint-v2.md`.

---

## What this is
Go CLI productivity tool. Three binaries:
- `qi` — stateless CLI (fast, no AI on hot path)
- `qid` — background daemon (file watcher, sync, queue, index)
- `qi-mcp` — MCP server (AI interface via strict schemas)

---

## Architectural rules

1. Markdown files in the Obsidian vault are the canonical source of truth.
2. Capture latency < 100ms.** `qi capture` must never call network, AI, or block. No exceptions.
2. SQLite, when added, is a derived per-machine index only.
3. Commands must stay thin. Business logic belongs in `internal/service`.
4. Vault parsing and formatting belongs in `internal/vault`.
5. Domain types belong in `internal/domain`.
6. Cross-platform logic belongs behind interfaces in `internal/platform`.
7. AI features must be explicit and opt-in. No silent LLM usage in normal commands.
8. Any AI-proposed mutation must require user confirmation before writing.

---

## Planned directory structure

```
qi/
├── cmd/
|    ├── qi/          # CLI entrypoint |
| --- |
|    ├── qid/         # Daemon entrypoint |
|    └── qi-mcp/      # MCP server entrypoint |
├── internal/
|    ├── commands/    # Thin command handlers — no logic here |
| --- |
|    ├── service/     # All business logic (TaskService, CaptureService, etc.) |
|    ├── domain/      # Domain types (Task, Note, Event) |
|    ├── vault/       # Markdown file read/write |
|    ├── index/       # SQLite (FTS5 for notes) |
|    ├── calendar/ |
|    ├── llm/ |
|    ├── platform/ |
|    ├── queue/ |
|    └── config/ |
├── mcp/
├── raycast/
├── migrations/
└── Makefile
```

Commands are thin. All logic lives in service layer.

---

## Storage paths

```
vault/
├── 00-inbox/       # capture always writes here
├── 10-tasks/
├── 20-notes/
├── 30-daily/
└── .qi/config.toml

~/.local/share/qi/  # machine-local
├── qi.db
├── cache/
├── logs/
└── queue/

~/.config/qi/
├── config.toml
└── prompts/
```

Secrets → system keychain.

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

## Build phases (implementation order)

1. Core CLI: `qi task`, `qi capture`
2. Calendar integration
3. Notes + full-text search
4. MCP server
5. Daemon (`qid`)
6. AI commands
7. Mobile

Start with phase 1. Don't scaffold AI/daemon layers until core CLI is stable.

---

## MCP tools (planned)

`search_notes`, `get_note`, `add_note`, `search_tasks`, `add_task`, `get_agenda`, `capture` — all require strict JSON schemas.

---

## Coding rules

- Prefer small packages and small functions.
- Add tests for parsers and service behavior.
- Keep output predictable.
- Preserve markdown compatibility with Obsidian task lines.
- Avoid OS-specific behavior in the core packages.

---

## What AI may NOT do

- Silent vault writes
- Auto task creation without confirmation
- Any mutation without explicit user approval

