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
qi task add <text> [--project <tag>] [--due YYYY-MM-DD] [--schedule YYYY-MM-DD]
qi task list
qi task done [fuzzy-text]
qi sync [--dry-run]            # reconcile tasks with project vaults (see Cross-vault sync)

qi note new "title" [--body "..."]
qi note list
qi note search "query"
qi index rebuild

qi agenda                     # today's events (local + any ICS / CalDAV / Google)
qi agenda week
qi calendar ...               # OAuth setup for Google Calendar

qi daily start                # open today's note, write events into ## Agenda
qi daily cp <text>            # append a timestamped checkpoint to ## Logs
```

`--due` writes the Obsidian Tasks due marker (`📅 YYYY-MM-DD`); `--schedule` writes the scheduled marker (`⏳ YYYY-MM-DD`). Both are optional and shown by `qi task list`.

### `qi ai` — requires `qid` running

```
qi ai tools list                              # show registered tools
qi ai tools call <name> [--args '{"k":"v"}'] [--caller cli|ai|...]
qi ai approvals [--status pending|approved|denied|executed|failed]
qi ai approve <id>                            # runs the queued tool
qi ai deny <id> [--reason "..."]
qi ai run "<prompt>" [--provider anthropic|ollama] [--model <id>]

qi daily end [YYYY-MM-DD]                     # AI-summarize a day's ## Logs into ## Summary
                                              # (confirm before write; defaults to today)
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

# --- Clients & projects (cross-vault task sync + launch) ---

[[client]]
name       = "Acme"                       # required; matched by --project / $WORK_CONTEXT
vault_path = "/Users/you/Vaults/Acme"     # required; Obsidian notes vault (-> QI_VAULT_PATH)
dev_root   = "/Users/you/Development/Acme" # optional; root for relative project dev_path
task_file  = "10-tasks/_acme.md"          # optional; client-wide tasks (qi task add --client Acme)
notes_path = "00-inbox"                   # optional; dir for qi note new --client (default 00-inbox)
  [client.launch]                         # optional; default harness for the client's projects
  harness = "claude"

  [[client.project]]
  project = "acme"                        # required; matches a task's first #tag

  [[client.project]]
  project  = "widget"
  path      = "10-projects/Widget"        # optional; base subdir; relative task_file/notes_path resolve under it
  dev_path  = "widget-svc"                # cwd for `qi launch harness`; relative -> under dev_root
  task_file = "tasks.md"                  # optional; default 10-tasks/<project>.md (under path when set)
  notes_path = "notes"                    # optional; default 00-inbox (under path; inherits client when unset)
    [client.project.launch]               # optional; overrides the client harness
    harness = "aider"
    args    = ["--model", "sonnet"]
```

A `[[client]]` is one Obsidian vault + one dev root shared by its `[[client.project]]` entries. `vault_path` is exported as `QI_VAULT_PATH`; a project's `dev_path` (absolute, or relative to `dev_root`) is the working directory `qi launch harness` runs in. `qi launch harness` resolves its target — `--project`, else `$WORK_CONTEXT` — **project-first, then client**: a project match uses the project's `dev_path`; a client match uses the client's `dev_root`; no match runs the global `[launch]` harness in the current directory. Harness precedence: `[client.project.launch]` > `[client.launch]` > `[launch]` > `$AI_HARNESS` > `$AI_EDITOR`. Validation rejects a missing client `name`/`vault_path`, a missing project tag, duplicate client names, duplicate project tags, two projects resolving to the same file, or a relative `dev_path` with no `dev_root`.

A client may also set `task_file` to become a sync target for client-wide tasks: `qi task add --client Acme "…"` tags the task `#Acme` and routes it to that file (the client name becomes a project tag, so it must not collide with a `[[client.project]]`). `--client` / `--project` also route notes — `qi note new --client Acme "…"` writes into that vault's configured notes dir (`notes_path`, default `00-inbox`) instead of the main vault (such notes aren't in the `qi note search` index, which only covers the main vault), and `qi launch harness --client Acme` is shorthand for resolving the client.

A project's optional `path` is a base subdirectory within the vault: relative `task_file` and `notes_path` — and their defaults (`10-tasks/<project>.md`, `00-inbox`) — resolve under `vault_path + path`, so a project that keeps its docs in one folder need not repeat that prefix. Absolute `task_file`/`notes_path` ignore `path`. A project's `notes_path` inherits the client's when unset (default `00-inbox`).

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

### Remote task creation (iPhone over Tailscale)

`qid` can expose a **token-authenticated HTTP endpoint** so an iPhone Shortcut can add
tasks that get a proper minted `^qi-…` block-ref ID and project routing — the same
`task.add` code path as the CLI, not a raw Obsidian edit that sync has to backfill.

It is **off by default**. Enable it with a token (env preferred so the secret stays out
of the vault-adjacent config):

```toml
# ~/.config/qi/config.toml
[remote]
enabled = true
addr    = "127.0.0.1:7777"   # loopback; reach it via Tailscale. NEVER 0.0.0.0 on an untrusted network.
# token = "…"                # or set QI_REMOTE_TOKEN in qid's environment
```

```bash
QI_REMOTE_TOKEN="$(openssl rand -hex 32)" QI_REMOTE_ENABLED=1 qid
# then expose loopback to your tailnet:
tailscale serve --bg --https 443 127.0.0.1:7777
```

Endpoint:

```
POST /task     Authorization: Bearer <token>
               {"text": "Buy milk", "project": "home", "due": "2026-06-12"}
            -> 201 {"id": "qi-1a2b3c4d", "path": ".../home.md", "project": "home"}
GET  /healthz  -> 200 ok   (unauthenticated liveness check)
```

Body fields mirror `qi task add`: `text` (required), optional `project` **or** `client`
(mutually exclusive; `client` must be a configured name; `project` is restricted to the
tag charset `A-Za-z0-9_-/`), optional `due`/`schedule` (`YYYY-MM-DD`). `text` rejects
embedded newlines/control chars so a remote caller can't forge extra lines in the vault.

**iOS Shortcut:** add a *Get Contents of URL* action → Method `POST`, URL
`https://<your-mac>.<tailnet>.ts.net/task`, Header `Authorization: Bearer <token>`,
Request Body *JSON* `{ "text": <Shortcut Input / Ask Each Time>, "project": "inbox" }`.
Trigger it from the share sheet or a Home Screen / Lock Screen button.

**Security model:** the call arrives as the `remote` caller identity, which the policy
gate allows **only** for an explicit allowlist (`task.add`, `vault.capture`) — every other
mutation still routes through the approval queue. The shared secret is the authentication;
keep the bind on loopback/tailnet. See `internal/policy` (`RemoteDecider`) and
`internal/daemon/http.go`.

---

## Cross-vault task sync

`qi sync` reconciles project tasks between the main qi vault and per-project Obsidian
vaults, so the same task is editable in both places and the main vault keeps the full
picture. Configure clients and their projects under `[[client]]` (see Configuration).

- **Per-project files.** A tagged task lives in `10-tasks/<project>.md` (untagged →
  `inbox.md`). `qi task list` / `done` aggregate across all of them.
- **Stable IDs.** Each task line carries an Obsidian block ref, e.g.
  `- [ ] Ship it #acme 📅 2026-05-27 ^qi-a1b2c3d4`. It is append-only and ignorable —
  Obsidian, Tasks, and Dataview treat the line normally. A line with no `^qi-` ID added
  inside a project vault is treated as new and gets an ID minted on the next sync.
- **Bidirectional 3-way merge.** Adds, edits, completions, and deletes flow both ways.
  A genuine two-sided edit of the same task is never clobbered — both lines are kept and
  one is tagged `#sync-conflict` for you to resolve by hand.
- **Safe against Obsidian Sync.** Writes are atomic and guarded against a sync rewrite
  landing mid-reconcile; a sync that detects a concurrent change aborts and retries rather
  than losing data. The merge ancestor lives in the SQLite index — derived, rebuildable,
  never canonical.

`qi sync --dry-run` prints the plan without writing. Run sync on one always-on machine;
each vault syncs independently through Obsidian Sync, with qi as the only bridge between them.

> Markdown stays canonical: projection files are real editable task lists, and `qi` never
> needs to be running for the vaults to work.

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
│   ├── inbox.md    # untagged tasks
│   └── <project>.md  # per-project tasks (one file per #tag)
├── 20-notes/
└── 30-daily/       # local agenda source
```

Configured project vaults receive a projection file (default `10-tasks/<project>.md` in
that vault), reconciled by `qi sync`. See Cross-vault task sync.

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
- Cross-vault task sync (`qi sync`, `[[client]]` / `[[client.project]]`)

### Next
- Cross-vault sync via `qid` fsnotify watch (near-real-time, replaces manual `qi sync`)
- More skills (`skill.weekly-plan`, `skill.process-inbox`)
- Streaming planner output (`Messages.NewStreaming`)
- Conversation-history caching (second cache breakpoint mid-loop)
- Mobile capture → write to synced inbox or POST to `qid` over Tailscale
