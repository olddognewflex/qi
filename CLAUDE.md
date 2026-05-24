# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make test                       # go test ./...
make tidy                       # go mod tidy
make run                        # go run ./cmd/qi
go test ./internal/vault -run TestParseTaskLine   # single test
go build -o bin/qi ./cmd/qi     # build binary (no `make build` target)
```

Go 1.25+. Module path is `qi`; import internal packages as `qi/internal/...`.

`qi` requires `vault_path` resolved from (priority order) env `QI_VAULT_PATH` > `~/.config/qi/config.toml` (`XDG_CONFIG_HOME` honored) > error. `QI_TASK_FILE_PATH` overrides task file. Without a vault path, every command exits at `commands.NewRootCommand` because `config.Load()` runs in the root constructor.

## Architecture

Strict layered Cobra CLI. Direction of dependency is one-way: `cmd → commands → service → {vault, index, calendar} → domain`.

- `cmd/qi/main.go` — single entrypoint; calls `commands.NewRootCommand().Execute()`. `cmd/qid` (daemon) and `cmd/qi-mcp` (MCP server) are planned but not yet built.
- `internal/commands/` — **thin** Cobra handlers. Wiring only: parse flags, call service, format output. No business logic. New subcommands register in `root.go`.
- `internal/service/` — all business logic. `TaskService`, `CaptureService`, `NoteService`, `AgendaService`. Services are plain structs constructed per-command from `config.Config`. Fuzzy matching, sorting, validation all live here.
- `internal/domain/` — pure value types (`Task`, `Note`, `Event`). No I/O, no deps.
- `internal/vault/` — markdown read/write. **Obsidian-compatible** task line format must be preserved: `- [ ] text #project 📅 YYYY-MM-DD`. See `ParseTaskLine`/`FormatTaskLine` in `tasks.go`; round-trip tests in `tasks_test.go` are load-bearing.
- `internal/index/` — SQLite FTS5 index for notes via `modernc.org/sqlite` (pure-Go, no CGO). Lives at `$XDG_DATA_HOME/qi/qi.db` (or `~/.local/share/qi/qi.db`). **Derived state** — must be fully rebuildable from markdown via `index rebuild`. Never the source of truth.
- `internal/calendar/` — `Provider` interface aggregates events into `AgendaService`. Three impls: `LocalProvider` (parses `## Schedule` block in `30-daily/YYYY-MM-DD.md`, format `- HH:MM[-HH:MM] Title [#tag]`), `ICSProvider` (HTTP fetch of `.ics`), `CalDAVProvider` (Google App Password). Add a provider → register it in `commands/agenda.go`.
- `internal/config/` — TOML loader with env var overrides. Derives `InboxPath`, `NotesPath`, `DailyPath` from `VaultPath` (subdirs `00-inbox`, `20-notes`, `30-daily`). `TaskFilePath` defaults to `10-tasks/inbox.md` relative to vault.

### Non-negotiable invariants

1. **Markdown is canonical.** Vault files must remain readable/usable without `qi`. SQLite is a derived index; never store anything there that isn't in the markdown.
2. **`qi capture` hot path <100ms.** No network, no AI, no blocking. `vault.WriteCapture` writes a timestamped `.md` to `00-inbox/` and returns.
3. **AI is opt-in and confirmation-gated.** No silent LLM calls. Any AI-proposed mutation requires explicit user approval before writing to the vault.
4. **Obsidian task-line compatibility.** Don't change `ParseTaskLine`/`FormatTaskLine` output format without verifying round-trip with existing vault files. Project tag becomes the first `#tag`; due date is `📅 YYYY-MM-DD`.

### Vault layout (writes target these paths)

```
vault/
├── 00-inbox/        # capture writes here (timestamped files)
├── 10-tasks/inbox.md
├── 20-notes/
└── 30-daily/YYYY-MM-DD.md   # local calendar source
```

Machine-local (never synced, not in vault): `~/.local/share/qi/qi.db`.

## Git hooks

Plugin-style hooks: enabled plugins listed in `git config --get-all hooks.enabled-plugins`, plugin code lives under `.git/hooks/<plugin>/`. Check the config before assuming hooks are inactive — they will run on commit/push.

## Scope boundaries (from AGENTS.md)

`qi` exposes local tools and runs deterministic skills. It does **not** autonomously chain tools, silently mutate vault state, hide tool execution, or invent workflows. When adding features, keep them deterministic and explicit; route any AI-driven action through a confirm-before-write flow.
