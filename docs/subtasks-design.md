# Subtasks — Design

Status: **approved design, not yet built** (spike QI-3). Recommendation only — the
implementation lands as the follow-up Stories listed at the end.

This note recommends how `qi` should represent **parent/child task relationships
(subtasks)** for tasks created two ways: directly via `qi task add`, and via the
Cloudflare remote queue (`qi remote-drain`, `kind=task` → `TaskService.CreateTask`).
It covers the markdown representation, identity/linking, the two creation-path
APIs, the queue payload shape, and completion/recurrence semantics — with the
trade-offs that justify each choice.

Guiding invariants (from `CLAUDE.md` / `AGENTS.md`):

1. **Markdown is canonical** and must stay readable/usable in Obsidian without `qi`.
2. **The SQLite index is derived state** — never the source of truth for a relation.
3. **Obsidian task-line round-trip is load-bearing** (`ParseTaskLine`/`FormatTaskLine`).

## The core constraint

Obsidian's *native* way to express a subtask is an **indented** child list item
under its parent:

```markdown
- [ ] Ship release ^qi-1a2b3c4d
    - [ ] Cut changelog
    - [ ] Tag v2
```

But `qi`'s parser **destroys indentation today**. `ParseTaskLine`
(`internal/vault/tasks.go:42`) does `trimmed := strings.TrimSpace(line)` (line 43)
and parses the trimmed form; `taskPrefixRe = ` `` `^\s*-\s\[( |x)\]\s+` `` accepts
leading whitespace but the depth is never captured. `FormatTaskLine`
(`tasks.go:125`) emits at column 0, and `AppendTask` (`tasks.go:219`) only appends
at end-of-file. So any hand-authored indentation is flattened on the next write,
and there is no insert-under-parent write path. **Indentation cannot be the
canonical relation without reworking the parser, the formatter, and the writer.**

Meanwhile `qi` already has a **stable, round-trip-safe identity**: the block-ref id
`^qi-XXXXXXXX` (`MintID()` at `tasks.go:34`; `idRe = ` `` `\^(qi-[0-9a-f]{8})\s*$` ``).
Every task is addressable by id, and ids survive edits, moves, and re-drains. That
is the natural anchor for a relation.

## Goals

1. A child task records **which task is its parent**, durably in markdown.
2. The relation survives Obsidian round-trips and `qi` rewrites unchanged.
3. Both creation paths (`qi task add`, remote-drain) can set a parent.
4. Completion/recurrence semantics for parent↔child are explicit and predictable.
5. The result is concrete enough to spin out implementation Stories.

## Recommendation in one line

**Make the relation a block-ref link (`parent → parent's qi-id`) stored as a
Dataview inline field on the child line; treat indentation as optional presentation
(a later, separate enhancement), not as the source of truth.**

## Decisions

| Concern | Decision | Why |
|---|---|---|
| Canonical relation | **Child stores its parent's `qi-id`** (a block-ref link), not positional nesting | `qi`'s identity is already id-based and round-trip-stable; nesting is positional and fragile (moving a line silently reparents) |
| Markdown encoding | Dataview **inline field** `[parent:: qi-XXXXXXXX]` on the child line | Plain text, Obsidian-usable, Dataview-queryable, survives round-trip; no new emoji semantics to invent |
| Why not Obsidian Tasks `⛔`/`🆔` | **Reject** for parent/child | `⛔ dependsOn` / `🆔` model *blocking*, not hierarchy — overloading them would lie about semantics |
| Why not indentation as canonical | **Defer** to a later enhancement | Requires capturing depth in `Task`, an insert-under-parent writer, and ordering rules; high blast radius on load-bearing code for v1 |
| Task struct | Add `ParentID string` (empty = top-level) to `domain.Task` | Mirrors the existing flat-field style; `""` is the safe zero value |
| Marker order slot | Emit `[parent:: …]` **after `✅` done-date, before the `^qi-id` block-ref** | Keeps the canonical order deterministic; block-ref must stay trailing (anchored by `idRe`'s `$`) |
| Unknown / dangling parent id | **Preserve the field verbatim**; never auto-delete | A parent may live in another file or be created later; silently dropping a link is data loss |
| `qi task add` API | New `--parent <fuzzy-or-id>` flag → resolve to one parent's `qi-id` (picker on ambiguity), set `AddTaskInput.ParentID` | Matches the fuzzy+picker pattern already used by `task done`/`task schedule` |
| Queue payload | Add optional `parent_id` (`omitempty`) to the Worker/D1 contract and `remotequeue.Task` | Flat string field; no schema migration pain; absent = top-level (back-compat) |
| Drain routing | Map `parent_id` → `AddTaskInput.ParentID` in `DrainService`; **do not** id-dedup on it | Keeps the existing id-idempotency on the child's own id; parent is just an attribute |
| Parent completion | **No cascade.** Completing a parent does **not** complete children | Matches Obsidian Tasks (no cascade); cascading would hide unfinished work. Optionally *warn* when open children remain |
| Child completion | Independent; never touches the parent | Children are first-class tasks that happen to carry a link |
| Recurrence | Only a task with its **own `🔁`** recurs; a recurring parent spawns a **new childless parent** | Cloning a subtree on every recurrence is surprising and id-explosive; keep recurrence per-task |

## Markdown representation (with round-trip)

A child line, fully marked up, in canonical order:

```markdown
- [ ] Cut changelog #qi ⏳ 2026-06-25 [parent:: qi-1a2b3c4d] ^qi-9f8e7d6c
```

Round-trip rules to add to `internal/vault/tasks.go`:

- **Parse:** a new `parentRe = ` `` `\[parent::\s*(qi-[0-9a-f]{8})\]` `` extracted in
  `ParseTaskLine` *before* tag parsing (so it never leaks into `Text`/`Tags`),
  populating `task.ParentID`. Extract it in the same "strip then continue" style as
  the id and date fields (`tasks.go:57-70`).
- **Format:** in `FormatTaskLine`, append `"[parent:: "+task.ParentID+"]"` to
  `parts` after the `✅` block and before the trailing `^qi-id` (`tasks.go:171-186`).
- **Round-trip test:** extend `tasks_test.go` so a line with `[parent:: …]` parses
  to a populated `ParentID` and re-emits **byte-identical**. Add a case proving a
  **foreign** inline field (e.g. `[priority:: high]`) and a non-qi `^ref` still
  survive untouched (existing invariant #4).

Indentation stays *informational only* in v1: if a user indents children in
Obsidian, `qi` will still flatten on rewrite, but the `[parent::]` link keeps the
relation intact. Presentation-level indentation is a deferred enhancement (see
follow-ups).

## `qi task add` API

```
qi task add "Cut changelog" --parent "ship release"     # fuzzy → picker if ambiguous
qi task add "Cut changelog" --parent qi-1a2b3c4d         # explicit id, no lookup
```

- Resolve `--parent`: if the value matches `qi-[0-9a-f]{8}`, use it directly;
  otherwise fuzzy-match open tasks (`service.FuzzyMatch`) and disambiguate with
  `tui.PickTasks` (single-select), exactly like `task schedule`.
- A `--parent` that resolves to nothing is an **error** (don't silently create a
  top-level task the user thinks is nested).
- Thread `ParentID` through `AddTaskInput` (`task_service.go:49`) → `CreateTask`.
- The child writes to **its own** project/inbox file by the existing routing
  (`task_service.go:111-114`); it is **not** forced into the parent's file. The
  link is by id, so cross-file parent/child is fine.

## Cloudflare queue payload shape

Extend the Worker/D1 contract (`docs/cloud-queue-spec.md`) and the wire struct
`remotequeue.Task` (`internal/remotequeue/client.go:25`) with one optional field:

```go
// internal/remotequeue/client.go
type Task struct {
    ID        string `json:"id"`
    Text      string `json:"text"`
    Kind      string `json:"kind,omitempty"`
    ParentID  string `json:"parent_id,omitempty"` // NEW: child's parent qi-id; absent = top-level
    Project   string `json:"project,omitempty"`
    Client    string `json:"client,omitempty"`
    Due       string `json:"due,omitempty"`
    Scheduled string `json:"scheduled,omitempty"`
    // … source/created_at/status/reason unchanged
}
```

Mirror it on `service.RemoteTask` (`drain_service.go:15`) and map it in the
`kind=task` branch (`drain_service.go:~90`):

```go
if _, err := s.Tasks.CreateTask(AddTaskInput{
    Text:      rt.Text,
    Project:   tag,
    Due:       due,
    Scheduled: scheduled,
    ParentID:  rt.ParentID, // NEW
    ID:        rt.ID,       // child's own id — idempotency key, unchanged
}); err != nil { … }
```

Notes:

- `omitempty` keeps the change **back-compatible**: existing producers that never
  set `parent_id` keep working; absent = top-level.
- Idempotency stays keyed on the child's own `id` (`CreateTask`'s existing logic,
  `task_service.go:80-95`). `parent_id` is a plain attribute, not a dedup key.
- **Ordering caveat to document:** the queue does not guarantee a parent drains
  before its child. Because the link is just a stored id, a child may reference a
  parent that hasn't landed yet — that is fine (dangling link preserved, resolves
  once the parent arrives). No drain-time existence check; do **not** deadletter on
  an unknown `parent_id`.

## Completion & recurrence semantics

- **Completing a parent does not complete its children** (no cascade), matching
  Obsidian Tasks. Recommended nicety: when `CompleteTask` (`task_service.go:161`)
  marks a task done and open children exist, print a non-blocking warning
  (`N open subtask(s) remain`). Pure UX; no write.
- **Completing a child** is fully independent and never mutates the parent.
- **Recurrence is per-task.** A recurring parent (`🔁`) spawns a fresh **childless**
  parent on completion (the existing `NextRecurrence` flow, `recurrence.go:33`);
  children are **not** cloned. A child that needs to recur carries its own `🔁`.
  Cloning subtrees per recurrence is explicitly rejected (surprising; id-explosive).

## Out of scope (v1)

- Indentation-as-canonical-nesting (deferred presentation enhancement).
- Multi-level depth semantics beyond a single `parent_id` chain (grandchildren work
  transitively via chained links, but no special handling).
- A `qi task tree` viewer / rollup progress — separate Story.
- Cascade-complete or cascade-reschedule policies.

## Follow-up Stories (spin-out)

1. **Story: `Task.ParentID` round-trip** — add the field + `[parent:: …]`
   parse/format + round-trip tests in `internal/vault`. (Foundation; no UX.)
2. **Story: `qi task add --parent`** — flag, fuzzy/id resolution + picker, thread
   through `AddTaskInput`/`CreateTask`. Depends on #1.
3. **Story: remote-queue `parent_id`** — extend `cloud-queue-spec.md`,
   `remotequeue.Task`, `RemoteTask`, and drain mapping. Depends on #1.
4. **Story: parent-completion warning** — open-children warning in `CompleteTask`.
   Depends on #1.
5. **(Optional) Story: subtask presentation** — capture/emit indentation so children
   render nested in Obsidian; insert-under-parent writer. Depends on #1; larger.
6. **(Optional) Story: `qi task tree`** — read-only nested view + progress rollup.
   Depends on #1.
