# qi

Go-first, local-first personal productivity CLI. Markdown is the source of truth. Fast by default. AI on demand, never silent.

---

## Three binaries

| Binary | Role |
|---|---|
| `qi`     | Stateless CLI — task / capture / note / agenda + `qi ai *` to talk to `qid`. |
| `qid`    | Long-running local orchestrator — tool registry, MCP fan-out, policy, approval queue, audit log. Listens on a unix socket. |
| `qi-mcp` | MCP server that re-publishes `qid`'s tool catalog to AI clients (Claude Desktop, etc.) over stdio. Mutating calls route to the approval queue. |

```
External MCP servers ─┐
                      ▼
        qid ── tool registry
        │   ├── MCP client manager
        │   ├── policy.Decider
        │   ├── approval queue
        │   └── JSON-RPC server (unix socket)
        │
   qi ──┘   qi-mcp ──┐
                     └── AI clients (Claude Desktop, …)
```

---

## What's built

### Vault primitives

| Package | What it does |
|---|---|
| `internal/domain` | `Task`, `Note`, `Event` value types — no I/O |
| `internal/vault` | Obsidian-compatible markdown parser/formatter (`ParseTaskLine`, `FormatTaskLine`, `ReadTasks`, `AppendTask`, `UpdateTaskLine`, `WriteCapture`, `WriteNote`) |
| `internal/service` | `TaskService`, `CaptureService`, `NoteService`, `AgendaService` — all business logic |
| `internal/index` | SQLite FTS5 (modernc.org/sqlite, pure Go) — `Open`, `Rebuild`, `Search`. Derived state, fully rebuildable from markdown. |
| `internal/calendar` | `Provider` interface, `LocalProvider` (parses `30-daily/`), `ICSProvider`, `CalDAVProvider`, Google OAuth + Calendar API |
| `internal/config` | TOML loader with env var overrides, XDG-compliant paths |

### AI infrastructure

| Package | What it does |
|---|---|
| `internal/tools` | In-memory tool registry. `SourceLocal`, `SourceMCP`, `SourceSkill`. `RegisterLocal`/`RegisterSkill`/`RegisterDynamic`/`Unregister`. JSON-tagged so the wire shape matches MCP catalog format. |
| `internal/tools/builtin` | Local tools wired into the registry. `vault.capture` so far. |
| `internal/skills` | Composed deterministic workflows registered as `SourceSkill`. `skill.daily-review` (today's agenda + open tasks + recent captures). |
| `internal/mcp` | `Manager` connecting external MCP servers via `mark3labs/mcp-go` stdio, registering their tools into the catalog under `mcp.<server>.<tool>`. |
| `internal/daemon` | JSON-RPC 2.0 server over unix socket. Methods: `tools.list`, `tools.call`, `approval.list/get/approve/deny`. ndjson framing matches MCP stdio convention. |
| `internal/daemon/client` | Typed JSON-RPC client. `Dial`, `Call`, `CallToolAs(caller, …)`, approval helpers. |
| `internal/policy` | `Decider` interface + `DefaultDecider`. Rules: empty caller → Deny; `cli` → Allow; read-only → Allow; mutating + non-cli → Confirm. |
| `internal/approval` | In-memory queue with state machine + append-only JSONL audit log. Every Enqueue/Approve/Deny/Execute/Fail is recorded. |
| `internal/qimcp` | Bridge that exposes `qid`'s catalog to MCP clients. Calls land as `caller="mcp:<sessionID>"`. |
| `internal/ai` | LLM-driven tool-use planner with provider-neutral interface. Backends: Anthropic (with prompt caching) and Ollama (stdlib HTTP). |

All packages have unit tests; race detector clean.

---

## Quick start

```bash
# Prerequisites: Go 1.25+

go mod tidy
go test ./...

mkdir -p ~/.config/qi
cat > ~/.config/qi/config.toml <<EOF
vault_path = "/path/to/your/obsidian/vault"
EOF

# Or: export QI_VAULT_PATH=/path/to/your/vault

# Build
go build -o bin/qi     ./cmd/qi
go build -o bin/qid    ./cmd/qid
go build -o bin/qi-mcp ./cmd/qi-mcp

# Stateless CLI works without qid:
bin/qi capture "Remember to fix the parser"
bin/qi task add "Fix the parser" --project qi --due 2026-05-01
bin/qi task list

# Start qid for AI / MCP features:
bin/qid &

# Talk to qid:
bin/qi ai tools list
bin/qi ai tools call skill.daily-review
bin/qi ai run "what's on my schedule today?"
```

---

## CLI reference

### Stateless commands (work without `qid`)

```
qi capture <text>             # alias: qi c <text>
qi task add <text> [--project <tag>] [--due YYYY-MM-DD]
qi task list
qi task done [fuzzy-text]

qi note new "title" [--body "..."]
qi note list
qi note search "query"
qi index rebuild

qi agenda                     # today's events (local + any ICS / CalDAV / Google)
qi agenda week
qi calendar ...               # OAuth setup for Google Calendar
```

### `qi ai` — requires `qid` running

```
qi ai tools list                              # show registered tools
qi ai tools call <name> [--args '{"k":"v"}'] [--caller cli|ai|...]
qi ai approvals [--status pending|approved|denied|executed|failed]
qi ai approve <id>                            # runs the queued tool
qi ai deny <id> [--reason "..."]
qi ai run "<prompt>" [--provider anthropic|ollama] [--model <id>]
```

Captures land in `00-inbox/` with a timestamped filename:
```
00-inbox/2026-04-23-225939.md
```

Tasks are Obsidian-compatible:
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

# --- Calendars (any combination) ---

[[ics_calendars]]
name = "Client A"
url  = "https://calendar.google.com/calendar/ical/.../basic.ics"

[[caldav_calendars]]
name     = "Client A"
email    = "raymond@clientdomain.com"
password = "xxxx xxxx xxxx xxxx"   # Google App Password

[[google_calendars]]
name        = "Personal"
account     = "you@gmail.com"
calendar_id = "primary"

# --- External MCP servers connected by qid ---

[[mcp_servers]]
id      = "github"
command = "/usr/local/bin/mcp-github"
args    = ["--mode", "stdio"]
env     = { GITHUB_TOKEN = "..." }

# --- AI provider for `qi ai run` ---

[ai]
provider     = "anthropic"          # or "ollama"
model        = "claude-sonnet-4-6"  # used when provider=anthropic
ollama_url   = "http://localhost:11434"
ollama_model = "qwen3:14b"
```

| Env var | Purpose |
|---|---|
| `QI_VAULT_PATH`     | Override vault path |
| `QI_TASK_FILE_PATH` | Override task file path |
| `QI_AI_PROVIDER`    | `anthropic` or `ollama` (overrides `[ai].provider`) |
| `ANTHROPIC_API_KEY` | Anthropic API key for `qi ai run` |
| `OLLAMA_URL`        | Override Ollama base URL (default `http://localhost:11434`) |

App Password (CalDAV): Google Account → Security → 2-Step Verification → App passwords. ⚠️ Plaintext — `chmod 600 ~/.config/qi/config.toml`.

Local daily-note events in `30-daily/YYYY-MM-DD.md` under `## Schedule`:
```markdown
## Schedule
- 09:00 Team standup
- 09:30-10:30 Design review #qi
- 14:00 Coffee #team
```
Format: `- HH:MM[-HH:MM] Title [#project]`.

---

## AI integration

### The approval gate

Every tool call carries a `caller` identity. The policy decider routes by caller + tool mutability:

| Caller | Read-only tool | Mutating tool |
|---|---|---|
| `cli`               | Allow | Allow (you are the user) |
| `ai-planner:<id>`   | Allow | Confirm (queued for approval) |
| `mcp:<sessionID>`   | Allow | Confirm (queued for approval) |
| (empty)             | Deny  | Deny |

When a mutation is queued, the caller receives `{"status":"pending","approval_id":"…"}`. A human must approve via `qi ai approve <id>` before it runs. Every transition (enqueue / approve / deny / execute / fail) is appended to `audit.log` next to the daemon socket.

### `qi ai run`

Runs a tool-use loop. The planner:
1. Fetches `tools.list` from `qid`.
2. Sends them to the configured LLM with `cache_control` on the system block (Anthropic) for free prompt caching across iterations and re-runs.
3. For each `tool_use` the model emits, dispatches via `client.CallToolAs(ctx, "ai-planner:<id>", name, args)`.
4. Surfaces approval-pending responses back to the model so it can tell you which `qi ai approve` to run.
5. Stops when the model emits a final text turn (or hits `DefaultMaxIterations=8`).

### `qi-mcp`

Connect Claude Desktop (or any MCP client) by pointing it at the `qi-mcp` binary with the qid socket path:
```json
{
  "mcpServers": {
    "qi": {
      "command": "/full/path/to/bin/qi-mcp",
      "args": ["-socket", "/Users/you/.local/state/qi/qid.sock"]
    }
  }
}
```
The AI client sees the full live catalog (`vault.capture`, `skill.daily-review`, every `mcp.<server>.<tool>`). Mutations route to the approval queue exactly like `qi ai run`.

---

## Architecture principles

- **Markdown is canonical.** If `qi` disappears, your vault still works with grep and your editor.
- **Capture latency < 100ms.** No network, no AI, no blocking on the `qi capture` hot path; CLI does not depend on `qid` for vault writes.
- **Commands are thin.** All logic lives in `internal/service` and `internal/skills`. Commands are wiring only.
- **AI is opt-in and confirmation-gated.** No silent LLM usage. Any AI-proposed mutation goes through the approval queue.
- **SQLite is a derived index.** Fully rebuildable from markdown via `qi index rebuild`.

---

## Vault layout

```
vault/
├── 00-inbox/       # qi capture writes here
├── 10-tasks/
│   └── inbox.md
├── 20-notes/
└── 30-daily/       # local agenda source
```

Machine-local state (never in vault, never synced):
```
~/.local/share/qi/qi.db          # SQLite FTS5 index
~/.local/state/qi/qid.sock       # qid unix-domain socket
~/.local/state/qi/audit.log      # append-only approval audit (JSONL)
```
Honors `XDG_DATA_HOME` and `XDG_RUNTIME_DIR` / `XDG_STATE_HOME` when set.

---

## Development

```bash
make tidy    # go mod tidy
make test    # go test ./...
make run     # go run ./cmd/qi

go test ./... -race    # race detector
go build -o bin/qid ./cmd/qid
go build -o bin/qi-mcp ./cmd/qi-mcp
```

Smoke-test fixtures under `internal/*/testdata/` are ignored by `go build ./...` per Go convention — build explicitly when needed:
```bash
go build -o /tmp/echoserver ./internal/mcp/testdata/echoserver
go build -o /tmp/mcpdriver  ./internal/qimcp/testdata/mcpdriver
```

---

## Roadmap

### Done
- Phase 1 — Tasks
- Phase 2 — Capture
- Phase 3 — Notes + FTS5 search
- Phase 4 — Calendar (local, ICS, CalDAV, Google OAuth)
- Tool registry + JSON-RPC daemon (`qid`)
- External MCP fan-out (`mark3labs/mcp-go`)
- Policy decider + approval queue + JSONL audit
- `qi-mcp` MCP server bridge
- `skill.daily-review`
- AI planner with provider abstraction (Anthropic + Ollama)

### Next
- More skills (`skill.weekly-plan`, `skill.process-inbox`)
- Streaming planner output (`Messages.NewStreaming`)
- Conversation-history caching (second cache breakpoint mid-loop)
- Mobile capture → write to synced inbox or POST to `qid` over Tailscale
