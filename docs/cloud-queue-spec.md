# Cloud Queue — Remote Task Capture (cloud-queue-only)

Status: **proposed** · Target: `qi` · Author: design pass, 2026-06-10

## Goal

Capture tasks from anywhere (iPhone, other machines) **even while the laptop is
closed/offline**, without weakening qi's invariants or its local performance.

The cloud holds *intent*; the laptop is the only writer of the canonical vault.
Markdown stays canonical (invariant #1). The vault write is done by a trusted
local process, so no non-cli caller ever mutates the vault — invariant #3 holds
with **no policy exception** (unlike the Tailscale/HTTP-endpoint path, which this
design replaces).

## Non-goals

- No real-time push to the laptop (it can be offline). Drain is pull-based.
- No second source of truth. The cloud is a **transient queue**, not a store:
  a row dies the moment its task lands in the vault.
- No impact on the local hot path. `qi capture <100ms` and all CLI commands are
  untouched; draining is a separate periodic command, off the hot path.

## Architecture

```
 iPhone Shortcut
   │  POST /enqueue   (Bearer ENQUEUE_TOKEN)
   ▼
 Cloudflare Worker  ──mints qi-id──▶ D1 (queue table)
   │  201 {id}                         ▲        │
   ▼                                   │        │
 (id shown/stored on phone)            │        │
                                       │ SELECT pending
 Laptop launchd timer (every 5 min,    │        │
 + on login/wake)                      │        │
   │  qi remote-drain                  │        │
   │    GET  /pull   (Bearer DRAIN_TOKEN) ───────┘
   │    ◀─ [{id,text,project,due,...}]
   │    for each: validate → idempotent write into vault (^qi-id)
   │    POST /ack         {ids:[...]}  ──▶ DELETE rows   (delete-on-ack)
   │    POST /deadletter  {ids,reason} ──▶ status='deadletter' (kept for review)
   ▼
 vault/10-tasks/<project>.md   - [ ] text #project 📅 … ^qi-xxxxxxxx
```

Tasks created **on** the laptop never touch the queue — the CLI writes the vault
directly. The queue is **inbound-only**: remote → cloud → laptop. No echo-back,
no sync-up, no loop.

---

## 1. Cloudflare Worker (the queue API)

Free tier is far more than enough (Workers 100k req/day, D1 5GB). Cost: **$0**.

### D1 schema

```sql
CREATE TABLE queue (
  id          TEXT PRIMARY KEY,                -- qi-xxxxxxxx, minted by the Worker
  text        TEXT NOT NULL,
  project     TEXT,                            -- free-form tag, or NULL
  client      TEXT,                            -- configured client name, or NULL
  due         TEXT,                            -- 'YYYY-MM-DD' or NULL
  scheduled   TEXT,                            -- 'YYYY-MM-DD' or NULL
  source      TEXT,                            -- 'ios-shortcut' etc. (audit only)
  created_at  INTEGER NOT NULL,                -- unix seconds
  status      TEXT NOT NULL DEFAULT 'pending'  -- 'pending' | 'deadletter'
);
CREATE INDEX idx_queue_status ON queue(status, created_at);
```

`drained` is not a status — drained == row deleted (privacy: task text does not
linger in the cloud). Only failures persist, as `status='deadletter'`.

### Routes

All routes require `Authorization: Bearer <token>` except `/healthz`. Compare the
token in **constant time**. Tokens are Worker secrets (`wrangler secret put …`).

| Method | Path           | Token scope | Behavior |
|--------|----------------|-------------|----------|
| POST   | `/enqueue`     | enqueue     | Validate; mint `qi-id`; INSERT pending; `201 {id}`. |
| GET    | `/pull?limit=N`| drain       | Up to N `pending` rows, oldest first; `200 {tasks:[…]}`. **Does not delete.** |
| POST   | `/ack`         | drain       | `DELETE … WHERE id IN (ids) AND status='pending'`; `200 {deleted:n}`. |
| POST   | `/deadletter`  | drain       | `UPDATE status='deadletter' … WHERE id IN (ids)`; store `reason`; `200`. |
| GET    | `/deadletter`  | drain       | List deadletter rows (for `qi remote-drain --show-failed`). |
| DELETE | `/deadletter`  | drain       | Purge resolved deadletter rows by id. |
| GET    | `/stats`       | drain       | Queue depth, no rows: `200 {pending:n, deadletter:m}` (for `qi remote-status`). |
| GET    | `/healthz`     | none        | `200 ok`. |

**Two tokens (recommended).** `ENQUEUE_TOKEN` for the phone (enqueue only) and
`DRAIN_TOKEN` for the laptop (pull/ack/deadletter). A leaked phone token then
cannot drain or delete your queue. The Worker checks scope per route.

### `/enqueue` validation (defense-in-depth + instant phone feedback)

The Worker is **not** the trust boundary (the laptop is), but validating here gives
the phone an instant `400` instead of a silent deadletter later:

- `text`: required, non-empty after trim, **no control chars** (`< 0x20 || == 0x7f`)
  — an embedded newline would forge extra markdown lines downstream.
- `project`: optional, charset `^[A-Za-z0-9_\-/]+$` (matches the vault tag charset;
  also keeps the derived filename from escaping the tasks dir).
- `client`: optional. The Worker **cannot** validate against config — it just
  records it; the laptop validates at drain.
- `project` and `client` are mutually exclusive.
- `due`, `scheduled`: optional, `^\d{4}-\d{2}-\d{2}$`.

### Id minting — the contract

The Worker mints the id in **exactly** `vault.MintID()`'s format: `qi-` + 8 lowercase
hex chars from a CSPRNG.

```js
const b = crypto.getRandomValues(new Uint8Array(4));
const id = 'qi-' + [...b].map(x => x.toString(16).padStart(2, '0')).join('');
```

The phone gets the id in the `201` response immediately. The laptop writes *that
exact id* as the `^qi-…` block ref — one stable id through the whole lifecycle, so
`qi sync` and Obsidian dedup work.

---

## 2. Laptop side

No `qid` daemon is required for this feature. `qi remote-drain` is a plain,
stateless CLI command (qi's normal shape). `qid` remains only for the AI/MCP work.

### 2a. Idempotent, provided-id task creation

This is the **one essential code change** and the crux of correctness.

Add an optional id to the service input and make creation idempotent:

```go
// internal/service/task_service.go
type AddTaskInput struct {
    Text      string
    Project   string
    Due       *time.Time
    Scheduled *time.Time
    ID        string // optional; if set, used instead of vault.MintID()
}
```

`CreateTask` gains an idempotency guard, keyed on the **qi-id** (the only dedup key
the markdown carries — the `^qi-…` block ref):

```
if input.ID != "" {
    existing, found := s.findByID(input.ID)   // scan ListAllTasks() for ^id
    if found {
        if textMatches(existing, input.Text) {
            return existing, nil               // already drained — no-op, safe re-drain
        }
        // id collision (same id, different task): re-mint a fresh local id
        input.ID = ""                          // fall through to MintID()
    }
}
// ... mint if input.ID == "", route by project, AppendTask ...
```

Why this shape:

- **Re-drain safety.** If a prior drain wrote the task but the `/ack` call dropped
  (network flapped on wake), the next drain re-pulls the same row and finds the id
  already in the vault → no-op, then re-acks. Converges, never duplicates.
- **Id-collision safety.** 8 hex = 32 bits. With idempotent skip-on-present, a
  cloud-minted id that coincidentally equals an existing (different) task would
  silently drop the new task. Detect "id present, different text" and re-mint
  locally instead of skipping. Astronomically rare, but the hole is closed.

`findByID` scans `ListAllTasks()` (already aggregates all `*.md` in `TasksDir`).
O(n) per drained task is fine off the hot path; batch the scan once per drain run
if it ever matters.

### 2b. Shared validation ("reuse task.add as the local writer")

Today the control-char + project-charset checks live in `internal/tools/builtin/task.go`.
Lift them into `internal/service` (business logic belongs there per the layering)
as `service.ValidateAddInput(input, clientValidator)`, and call it from **both**:

- the existing qid `task.add` tool, and
- the new `qi remote-drain` path.

The drainer is the real trust boundary. Client names are validated here against
config (the cloud can't). Result: one validation implementation, used everywhere a
task enters the vault.

### 2c. `qi remote-drain`

A new CLI command (registered in `internal/commands/root.go`, thin handler →
service). Flow:

1. Load config → queue `url` + `DRAIN_TOKEN`. No-op (exit 0) if `[remote_queue].enabled`
   is false or unset.
2. `GET /pull?limit=100`.
3. For each task:
   - **Validate** (`service.ValidateAddInput`, incl. client-against-config). Fail →
     collect `{id, reason}` for deadletter; do **not** write.
   - **Write** via idempotent `CreateTask` (provided id). Success → collect id for ack.
4. `POST /ack {ids: written}`.
5. `POST /deadletter {ids, reason}` for validation failures — **never silently drop**.
6. Print summary: `drained N, rejected M (see qi remote-drain --show-failed)`. Exit 0
   even with rejects (they're surfaced, not fatal).
7. Network error reaching the Worker → exit non-zero, no partial corruption:
   - pull fails → nothing written.
   - write succeeds but ack fails → rows stay `pending`; next drain re-pulls, dedup
     skips the already-written task, re-acks. Safe.

Flags: `--show-failed` (GET /deadletter), `--once` (default; single pass),
`--limit N`. No long-running mode — the launchd timer provides the cadence.

### 2c′. `qi remote-status`

A read-only companion to `remote-drain` (same config + `DRAIN_TOKEN`, registered in
`root.go`). Hits `GET /stats` and prints `pending N, deadletter M` — visibility into
the queue depth without pulling or mutating anything. No-op (exit 0) when
`[remote_queue].enabled` is false. The Worker computes both counts in one grouped
scan over the `(status, created_at)` index, so it stays cheap regardless of depth.

### 2d. Config

```toml
# config.toml
[remote_queue]
enabled = true
url     = "https://qi-queue.<subdomain>.workers.dev"
token   = "<DRAIN_TOKEN>"     # or env QI_QUEUE_TOKEN (preferred; keep it out of files synced to the vault)
```

Env overrides (consistent with existing `QI_*` pattern): `QI_QUEUE_URL`,
`QI_QUEUE_TOKEN`, `QI_QUEUE_ENABLED` (`"1"`/`"true"`). Parse a `remoteQueueTOML`
struct in `internal/config/config.go`, mirror the existing `[remote]` loader.

### 2e. launchd timer (the unattended drainer)

`~/Library/LaunchAgents/com.olddognewflex.qi-drain.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>            <string>com.olddognewflex.qi-drain</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/raymonddoran/go/bin/qi</string>
    <string>remote-drain</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>QI_VAULT_PATH</key>  <string>/path/to/vault</string>
    <key>QI_QUEUE_TOKEN</key> <string>__DRAIN_TOKEN__</string>
  </dict>
  <key>RunAtLoad</key>        <true/>   <!-- drain on login -->
  <key>StartInterval</key>    <integer>300</integer>  <!-- every 5 min -->
  <key>StandardOutPath</key>  <string>/Users/raymonddoran/.local/state/qi/drain.log</string>
  <key>StandardErrorPath</key><string>/Users/raymonddoran/.local/state/qi/drain.err.log</string>
</dict>
</plist>
```

- A `StartInterval` timer that misses firings during sleep fires **once on wake** —
  so closing the lid for 3 h → one drain on reopen. Exactly the desired behavior.
- It's a periodic one-shot, **not** a daemon — no `KeepAlive`.
- The plist is `0644` (world-readable). Prefer the token via the launchd
  `EnvironmentVariables` only if the laptop is single-user; otherwise read it from
  `config.toml` (0600) and drop it from the plist.

Load: `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.olddognewflex.qi-drain.plist`

---

## 3. iOS Shortcut

1. **Text** (Ask Each Time) → task text. Optional **Text** for project.
2. **Get Contents of URL**: `POST https://qi-queue.<sub>.workers.dev/enqueue`
   - Header `Authorization: Bearer <ENQUEUE_TOKEN>`
   - Request body (JSON): `{ "text": <text>, "project": <project>, "source": "ios-shortcut" }`
3. **Get Dictionary Value** `id` from the response → optionally show a notification
   or append to a log note. (The id is informational; the laptop is authoritative.)

Add to the Share Sheet so you can capture from any app. Works offline-of-laptop —
only your phone's own internet is needed.

---

## 4. Security & privacy

- **Two scoped tokens**, Worker secrets, constant-time compare. Phone = enqueue
  only; laptop = pull/ack/deadletter.
- **TLS** by default (Cloudflare).
- **Plaintext in the cloud, briefly.** Task text sits in D1 until the next drain
  deletes it. Mitigate: delete-on-ack (not soft-mark); frequent drain; a Worker
  cron that purges stale `pending` rows (> 7 days) to `deadletter`. Flag: don't put
  secrets in task text.
- **Token in the Shortcut** is readable by anyone with the unlocked phone. Rotate
  via `wrangler secret put` if leaked; the laptop token is unaffected.
- The laptop **re-validates everything** at drain — the cloud is untrusted input.

## 5. Failure matrix

| Failure | Result |
|---|---|
| Phone offline | Shortcut errors; user retries later. No state change. |
| Laptop offline/closed | Rows sit `pending`; drained on next wake. |
| `/pull` fails | Nothing written; drain exits non-zero; retried next interval. |
| Write OK, `/ack` fails | Rows stay `pending`; re-pulled next drain; dedup skips re-write; re-acked. No dup. |
| Bad client/project from phone | Validated at drain → `deadletter` + reported. Not written, not lost. |
| Id collision (cloud id == existing different task) | Drainer detects (id present, text differs) → re-mints local id, writes both. |
| Duplicate `/enqueue` (phone double-tap) | Two rows, two ids, two tasks. (Phone-side idempotency is out of scope v1.) |

## 6. Testing

- **Worker** (vitest + miniflare/`wrangler dev`): auth (401 no/wrong token, scope
  enforcement), `/enqueue` validation (control chars, project charset, mutual
  exclusion, date format), mint format `^qi-[0-9a-f]{8}$`, pull/ack/deadletter SQL.
- **Laptop** (`internal/commands` / `internal/service`, `httptest` mock queue —
  reuse the `http_test.go` pattern):
  - happy drain → vault line with the cloud's id.
  - **idempotent re-drain**: pull the same task twice → exactly one vault line.
  - ack-fail then re-drain → still one line.
  - validation failure → `/deadletter` called, nothing written.
  - network failure → exit non-zero, no partial write.
  - id-collision → second task written under a re-minted id.

## 7. Rollout

1. Deploy Worker + D1 + two secrets; smoke-test with `curl`.
2. Laptop changes: `AddTaskInput.ID` + idempotent `CreateTask`; lift validation into
   `service.ValidateAddInput`; `[remote_queue]` config; `qi remote-drain`.
3. Install the launchd timer.
4. Build the iOS Shortcut.
5. Resolve the existing HTTP-endpoint PR (`feat/remote-http-task-add`) — see below.

## Open decisions

1. **Two tokens vs one** — ✅ **DECIDED: two scoped tokens.** `ENQUEUE_TOKEN`
   (phone, enqueue only) + `DRAIN_TOKEN` (laptop, pull/ack/deadletter).
2. **Fate of the Tailscale/HTTP-endpoint PR** — ✅ **DECIDED: revert/abandon.**
   PR #16 (merge `def9530`) reverted on branch `revert/remote-http-task-add`;
   `main` returns to its pre-endpoint state. Cloud-queue re-introduces the pieces
   it needs (`CreateTask` with provided id, validation lifted into `service`)
   fresh, designed for this path rather than patched from the endpoint code.
3. **Drain interval** — rec 5 min (`StartInterval 300`). Network-change-triggered
   drain is possible but more complex; skip v1.
4. **Stale-row TTL purge** in the Worker — rec 7 days `pending` → `deadletter`.
5. **Phone-side enqueue idempotency** (dedupe double-taps) — out of scope v1.
