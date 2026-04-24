# qi

Go-first, local-first personal productivity CLI. Markdown is the source of truth. Fast by default. AI on demand.

---

## What's built (Phases 1–2)

**Phase 1 — Core task management — fully functional.**

| Package | What it does |
|---|---|
| `internal/domain` | `Task` struct — canonical domain type |
| `internal/vault` | Obsidian-compatible markdown parser/formatter (`ParseTaskLine`, `FormatTaskLine`, `ReadTasks`, `AppendTask`, `UpdateTaskLine`) |
| `internal/service` | `TaskService` — `AddTask`, `ListOpenTasks`, `CompleteTask`, `FuzzyMatch` |
| `internal/commands` | Thin Cobra handlers for `task add`, `task list`, `task done` |
| `internal/config` | TOML config loader (XDG-compliant path, env var overrides) |
| `raycast/` | Shell scripts for Raycast integration (`task-add.sh`, `task-list.sh`) |

**Phase 2 — Capture — fully functional.**

| Package | What it does |
|---|---|
| `internal/vault` | `WriteCapture` — writes timestamped `.md` file to `00-inbox/`, no locking, no races |
| `internal/service` | `CaptureService` — thin wrapper over vault write |
| `internal/commands` | `capture` command with `c` alias |
| `internal/config` | `InboxPath` field — defaults to `{vault_path}/00-inbox` |

**All operations have unit tests.**

---

## Quick start

```bash
# Prerequisites: Go 1.23+

go mod tidy
go test ./...

# Set up config
mkdir -p ~/.config/qi
cat > ~/.config/qi/config.toml <<EOF
vault_path = "/path/to/your/obsidian/vault"
EOF

# Or use env vars
export QI_VAULT_PATH=/path/to/your/obsidian/vault

# Run
go run ./cmd/qi capture "Remember to fix the parser"
go run ./cmd/qi task add "Fix the parser" --project qi --due 2026-05-01
go run ./cmd/qi task list
go run ./cmd/qi task done "fix"
```

Build a binary:
```bash
make build   # outputs bin/qi
qi task add "Buy milk"
```

---

## CLI reference

```
qi capture <text>             # alias: qi c <text>
qi task add <text> [--project <tag>] [--due YYYY-MM-DD]
qi task list
qi task done [fuzzy-text]
```

Capture writes a timestamped file to `00-inbox/`:
```
00-inbox/2026-04-23-225939.md
```

Task format written to vault (Obsidian-compatible):
```
- [ ] Buy milk #qi 📅 2026-05-01
```

---

## Configuration

Priority: **env vars > `~/.config/qi/config.toml` > defaults**

```toml
# ~/.config/qi/config.toml
vault_path     = "/Users/you/Documents/vault"
task_file_path = "10-tasks/inbox.md"   # optional, relative to vault_path
```

| Env var | Purpose |
|---|---|
| `QI_VAULT_PATH` | Override vault path |
| `QI_TASK_FILE_PATH` | Override task file path |

---

## Architecture principles

- **Markdown is canonical.** If `qi` disappears, your vault still works with grep and your editor.
- **Capture latency < 100ms.** No network, no AI, no blocking on the hot path.
- **Commands are thin.** All logic lives in `internal/service`. Commands are wiring only.
- **AI is opt-in.** No silent LLM usage. No mutations without user confirmation.
- **SQLite is a derived index.** Not implemented yet; will be fully rebuildable from markdown.

---

## Vault layout

```
vault/
├── 00-inbox/       # qi capture writes here
├── 10-tasks/       # qi task add writes here by default
|    └── inbox.md |
| --- |
├── 20-notes/
├── 30-daily/
└── .qi/
    └── config.toml
```

Machine-local state (not in vault, not synced):
```
~/.local/share/qi/
├── qi.db           # SQLite index (not yet built)
├── cache/
├── logs/
└── queue/
```

---

## Roadmap

### Phase 3 — Notes + search
- `qi note new "title"`, `qi note search "query"`
- SQLite FTS5 index over the vault
- `qi index rebuild`

### Phase 4 — Calendar
- `qi agenda` / `qi agenda week`
- CalendarService + Google/Apple calendar sync


### Phase 5 — MCP server (`qi-mcp`)
- Strict JSON schema tools for AI agents: `search_notes`, `get_note`, `add_note`, `search_tasks`, `add_task`, `get_agenda`, `capture`

### Phase 6 — Daemon (`qid`)
- Background file watcher
- Calendar sync jobs
- Queue processing + index updates

### Phase 7 — AI commands (explicit, opt-in)
- `qi ask "question"` — query your vault
- `qi digest` — daily summary
- `qi triage` — suggested task prioritization (suggest only, never auto-apply)

### Phase 8 — Mobile
- Phase 1: write to synced inbox file
- Phase 2: POST to local `qid` endpoint over Tailscale

---

## Development

```bash
make tidy    # go mod tidy
make test    # go test ./...
make run     # go run ./cmd/qi
```

Three binaries planned:
- `cmd/qi` — stateless CLI (fast, no AI on hot path) ← **only this exists today**
- `cmd/qid` — background daemon
- `cmd/qi-mcp` — MCP server

