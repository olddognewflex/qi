# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make test                       # go test ./...
make tidy                       # go mod tidy
make run                        # go run ./cmd/qi
go test ./internal/vault -run TestParseTaskLine   # single test
go build -o bin/qi ./cmd/qi     # build CLI (no `make build` target)
go build -o bin/qid ./cmd/qid   # build daemon
go build -o bin/qi-mcp ./cmd/qi-mcp   # build MCP server
```

Go 1.25+. Module path is `qi`; import internal packages as `qi/internal/...`.

`qi` requires `vault_path` resolved from (priority order) env `QI_VAULT_PATH` > `~/.config/qi/config.toml` (`XDG_CONFIG_HOME` honored) > error. `QI_TASK_FILE_PATH` overrides task file. Without a vault path, every command exits at `commands.NewRootCommand` because `config.Load()` runs in the root constructor.

## Architecture

Three binaries:
- `cmd/qi/main.go` — stateless CLI; calls `commands.NewRootCommand().Execute()`. Fast, no AI on the hot path.
- `cmd/qid/main.go` — long-running daemon. Hosts the tool registry and serves a JSON-RPC 2.0 API over a unix-domain socket. Wires builtin tools, deterministic skills, external MCP servers, the policy gate, and the approval queue.
- `cmd/qi-mcp/main.go` — MCP server (stdio subprocess of an AI client). Dials qid, fetches the live tool catalog, and re-publishes it to MCP clients (e.g. Claude Desktop).

### CLI layer (`qi`)

Strict layered Cobra CLI. Direction of dependency is one-way: `cmd → commands → service → {vault, index, calendar} → domain`.

- `internal/commands/` — **thin** Cobra handlers. Wiring only: parse flags, call service, format output. No business logic. New subcommands register in `root.go`. `qi task add` takes `--project` (free-form tag) or `--client` (validated client name → routes to the client `task_file`; mutually exclusive). `qi note new` takes `--client`/`--project` to write into that vault's configured notes dir (`notes_path`, default `00-inbox`; via `Config.NoteVaultFor`) instead of the main vault. `ai.go` is the client side of qid: `qi ai tools list|call`, `qi ai run`, `qi ai approvals|approve|deny`. `launch.go` is `qi launch harness` (alias `qi launch ai`): resolves an external AI harness via `Config.ResolveLaunchTarget` and runs it with `QI_VAULT_PATH` exported (matched vault, else global) and cwd set to the resolved working dir (project `dev_path` / client `dev_root`, else the current dir is inherited — no chdir) — non-detached harnesses replace the qi process (`syscall.Exec`, unix), detached ones spawn and return. Platform exec split lives in `launch_exec_unix.go` / `launch_exec_windows.go`.
- `internal/service/` — all business logic. `TaskService`, `CaptureService`, `NoteService`, `AgendaService`. Services are plain structs constructed per-command from `config.Config`. Fuzzy matching, sorting, validation all live here.
- `internal/domain/` — pure value types (`Task`, `Note`, `Event`). No I/O, no deps.
- `internal/vault/` — markdown read/write. **Obsidian-compatible** task line format must be preserved: `- [ ] text #project 📅 YYYY-MM-DD`. See `ParseTaskLine`/`FormatTaskLine` in `tasks.go`; round-trip tests in `tasks_test.go` are load-bearing.
- `internal/index/` — SQLite FTS5 index for notes via `modernc.org/sqlite` (pure-Go, no CGO). Lives at `$XDG_DATA_HOME/qi/qi.db` (or `~/.local/share/qi/qi.db`). **Derived state** — must be fully rebuildable from markdown via `index rebuild`. Never the source of truth.
- `internal/calendar/` — `Provider` interface aggregates events into `AgendaService`. Four impls: `LocalProvider` (parses `## Schedule` block in `30-daily/YYYY-MM-DD.md`, format `- HH:MM[-HH:MM] Title [#tag]`), `ICSProvider` (HTTP fetch of `.ics`), `CalDAVProvider` (App Password), `GoogleProvider` (OAuth; token + keychain helpers in `google_auth.go`/`google_token.go`/`keychain.go`). Add a provider → register it in `commands/agenda.go`.
- `internal/tui/` — Bubble Tea pickers for interactive CLI selection.
- `internal/config/` — TOML loader with env var overrides. Derives `InboxPath`, `NotesPath`, `DailyPath` from `VaultPath` (subdirs `00-inbox`, `20-notes`, `30-daily`). `TaskFilePath` defaults to `10-tasks/inbox.md` relative to vault. Also parses `[[mcp_servers]]` (external MCP servers qid connects to), the `[ai]` section (provider/model defaults for `qi ai run`), `[[client]]` entries with nested `[[client.project]]` (a client = one `vault_path` + `dev_root` shared across its projects; each project has a `#tag`-matching `project` key, optional `task_file` (projection task file), optional `notes_path` (note dir for `qi note new --project`, default `00-inbox`, inherits the client's `notes_path` when unset), and optional `dev_path` (absolute or relative to `dev_root`) used as the `qi launch harness` working dir; the client itself takes an optional `notes_path` (default `00-inbox`) for `qi note new --client`). Clients flatten into `Config.Projects` at load (each project inheriting the client `vault_path`/`launch`, paths resolved absolute) so `sync`/`launch` consume the flat list; `Config.Clients` is retained for client-name launch resolution. A `[[client]]` with `task_file` also flattens into a **synthetic** project tagged by the client name (for `sync` routing of client-wide tasks); synthetic projects are excluded from `ProjectByName` so they never shadow client-name launch resolution. `Config.NoteVaultFor(client, project)` resolves which vault a `qi note new` lands in, and the `[launch]` section (default AI harness for `qi launch harness`, with optional per-`[[project]]` `[project.launch]` overrides). `Config.ResolveLaunchTarget(flag)` resolves the launch target (`--project` flag, else `$WORK_CONTEXT`) **project-first, then client**: a project match runs `[client.project.launch]` > `[client.launch]` > `[launch]` > `$AI_HARNESS` > `$AI_EDITOR` with cwd = project `dev_path`; a client match runs `[client.launch]` > `[launch]` > env with cwd = client `dev_root`; no match runs the global harness in the current dir. An explicit flag matching neither errors; an unmatched `$WORK_CONTEXT` is lenient and falls through to the global default. `QI_VAULT_PATH` is exported as the matched vault (else global).

### Daemon layer (`qid` / `qi-mcp`)

qid is the orchestration core. Every tool call — from the CLI, the AI planner, or an MCP client — flows through one registry and one policy gate. The **caller identity** drives the trust decision: `cli` callers and read-only tools run immediately; any non-cli caller mutating state routes through the approval queue.

- `internal/tools/` — in-memory tool registry. Tools come from three sources: `SourceLocal` (compiled-in Go), `SourceMCP` (connected external MCP servers), `SourceSkill` (composed workflows). Derived state — rebuilt from code plus live MCP connections, never persisted. `Execute` dispatches local handlers directly and MCP tools through the manager.
- `internal/tools/builtin/` — compiled-in `SourceLocal` tools (e.g. `capture`).
- `internal/skills/` — deterministic composed workflows exposed as `SourceSkill` tools (e.g. `daily-review`). Chain existing services and read the vault; **never call an LLM, never write silently.** Read-only by default; mutating skills must set `Mutating: true` so policy gates them.
- `internal/daemon/` — JSON-RPC 2.0 server. Newline-delimited JSON over a unix-domain socket (`server.go`, `wire.go`, `socket.go`). `internal/daemon/client/` is the Go client used by `qi ai` and `qi-mcp`; it carries the caller identity and detects approval-pending results via `IsPending`.
- `internal/policy/` — decides allow / queue / refuse for each call. Deterministic and conservative: anything a non-cli caller wants to mutate routes through the approval queue. `DefaultDecider` is the wired default.
- `internal/approval/` — in-memory queue of mutating calls policy did not auto-allow, plus an **append-only JSONL audit log** (the durable record). Every state transition (queued → approved/denied → executed/failed) is logged.
- `internal/mcp/` — connects qid to external MCP servers listed in `config.MCPServers`, surfaces their tools under namespace `mcp.<serverID>.<toolName>`, and routes `Execute` calls back to the right client.
- `internal/qimcp/` — the inverse bridge: re-publishes qid's tools to AI clients via MCP, forwarding each call back through the daemon client as `caller="mcp:<sessionID>"` so mutations hit the approval queue. Drives `cmd/qi-mcp`.
- `internal/ai/` — LLM tool-use loop (`Planner`) against qid's catalog. Providers: Anthropic and Ollama. Executes proposed tool calls as `caller="ai-planner:<sessionID>"`; mutating calls surface as approval-pending results — the planner never bypasses qid's policy gate.

### Non-negotiable invariants

1. **Markdown is canonical.** Vault files must remain readable/usable without `qi`. SQLite is a derived index; never store anything there that isn't in the markdown.
2. **`qi capture` hot path <100ms.** No network, no AI, no blocking. `vault.WriteCapture` writes a timestamped `.md` to `00-inbox/` and returns.
3. **AI is opt-in and confirmation-gated.** No silent LLM calls. Any AI-proposed mutation requires explicit user approval before writing to the vault. This is enforced structurally in qid: non-cli callers (`ai-planner:*`, `mcp:*`) can never mutate directly — `internal/policy` routes their mutations into `internal/approval`, where a human runs `qi ai approve <id>` (or `deny`). Don't add a code path that lets a non-cli caller bypass that gate.
4. **Obsidian task-line compatibility.** Don't change `ParseTaskLine`/`FormatTaskLine` output format without verifying round-trip with existing vault files. Project tag becomes the first `#tag`; due date is `📅 YYYY-MM-DD`.

### Vault layout (writes target these paths)

```
vault/
├── 00-inbox/        # capture writes here (timestamped files)
├── 10-tasks/inbox.md
├── 20-notes/
└── 30-daily/YYYY-MM-DD.md   # local calendar source
```

Machine-local (never synced, not in vault):
- `$XDG_DATA_HOME/qi/qi.db` (or `~/.local/share/qi/qi.db`) — SQLite note index.
- `$XDG_RUNTIME_DIR/qi/qid.sock` → `$XDG_STATE_HOME/qi/qid.sock` → `~/.local/state/qi/qid.sock` — qid unix socket (first that resolves; 0600). `--socket` overrides on both qid and the clients.
- `audit.log` next to the socket — append-only JSONL approval audit log.

## Git hooks

Plugin-style hooks: enabled plugins listed in `git config --get-all hooks.enabled-plugins`, plugin code lives under `.git/hooks/<plugin>/`. Check the config before assuming hooks are inactive — they will run on commit/push.

## Scope boundaries (from AGENTS.md)

`qi` exposes local tools and runs deterministic skills. It does **not** autonomously chain tools, silently mutate vault state, hide tool execution, or invent workflows. When adding features, keep them deterministic and explicit; route any AI-driven action through a confirm-before-write flow.
