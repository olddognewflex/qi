# qi — Improvement Ideas & Brainstorm

*Date: 2026-06-11. Source: codebase survey + brainstorm session.*

## Review verdict

Project is healthy — clean code, no TODOs, strong invariants, good test coverage in core logic.
The real gap *was* **utilization**: the orchestration layer (qid / policy / approval / MCP)
was built but nearly empty. The quick wins below have since filled it — 5 builtin tools
(`vault.capture`, `task.add`, `task.list`, `note.search`, `agenda.today`) and 3 skills
(`daily-review`, `process-inbox`, `process-inbox-apply`). Engine now carries cargo; remaining
focus shifts to the medium/big items (closing the capture→processing loop).

**Strengths**
- Strict layering (`cmd → commands → service → {vault, index, calendar} → domain`)
- Invariants enforced structurally (policy gate, round-trip task-line tests)
- Capture hot path protected (<100ms, no network/AI)
- Cloud-queue design correct: laptop is the sole vault writer, cloud holds intent only

**Weak spots**
- `internal/calendar` undertested (1 test file / 8 source files)
- MCP surface near-useless until more tools exist
- Cross-vault sync still manual (`qi sync`)

## Ideas — grouped by effort

### Quick wins (days; infrastructure already exists) — ✅ all shipped

1. ~~**More builtin tools.**~~ ✅ **Done.** `task.add` (mutating), `task.list`, `note.search`,
   `agenda.today` live in `internal/tools/builtin/`, registered in `cmd/qid/main.go`.
   `qi-mcp` now exposes a usable catalog; writes gated by the approval queue.
2. ~~**`skill.process-inbox`**~~ ✅ **Done.** `skill.process-inbox` (read-only triage proposal)
   + `skill.process-inbox-apply` (mutating, gated) in `internal/skills/processinbox.go`.
3. ~~**`qi remote status`**~~ ✅ **Done** as `qi remote-status` — pending + deadletter counts,
   read-only (`internal/commands/remote_status.go`).
4. ~~**`qi doctor`**~~ ✅ **Done.** Health checks for config, vault, qid socket, index freshness,
   Worker reachability (`internal/commands/doctor.go`); non-zero exit only on hard fail.

### Medium (week-ish)

5. ~~**Inbox triage TUI.**~~ ✅ **Done.** `qi inbox` Bubble Tea flow: per capture →
   task / note / archive / delete (`--dry-run` prints proposals). Triage core extracted to
   `service.InboxService`, shared with the `skill.process-inbox` tools. Closes the
   capture→processing loop.
6. ~~**fsnotify-driven sync.**~~ ✅ **Done.** Opt-in via `[sync] watch = true`: qid watches the
   canon + projection task dirs and runs the existing `sync` reconcile (debounced via
   `debounce_ms`, default 750) on `.md` change. New `internal/watcher` package; reconcile
   injected as a closure so the watcher stays decoupled. Eliminates manual `qi sync` runs.
7. **Unified `qi search`** — FTS across notes + tasks + dailies. Index currently covers
   notes only. Still derived state; invariant-safe.
8. **Due-today notifications.** qid is long-running already — morning macOS notification
   listing due/scheduled tasks. Deterministic, read-only, no policy gate needed.
9. **Recurring tasks** — `🔁 every week` marker, Obsidian Tasks plugin format.
   Caution: invariant 4 — `ParseTaskLine`/`FormatTaskLine` round-trip tests are load-bearing.

### Big bets

10. **Time-blocking: `qi plan`.** Pick open tasks, write them into the daily note's
    `## Schedule` block. Closes the loop between task list and agenda — the local calendar
    provider already parses that block, so scheduled tasks appear in `qi agenda` for free.
11. **Remote queue beyond tasks.** Enqueue notes/captures too; iOS shortcut variants.
    Worker D1 schema needs a `kind` column; drain routes by kind.
12. **Local embeddings search.** Ollama provider exists — embed notes, semantic search
    alongside FTS. Derived index, rebuildable, invariant-safe. Opt-in.
13. **`skill.weekly-review`** — completed tasks, capture volume, daily log highlights →
    propose a review note (gated write). Pairs with `skill.daily-review`.
14. **qid SourceSkill for in-session task/capture (revisit).** A Claude Code skill
    (`.claude/skills/qi/`) now teaches agents to drive the `qi` CLI in sessions. Open
    question: also add a deterministic qid `SourceSkill` (like `daily-review`/`process-inbox`)
    that composes task-add + capture so AI/MCP callers get a single gated workflow instead of
    individual builtin tools. Candidate shapes: `skill.quick-task` (add task → return updated
    open list) or `skill.session-log` (append a journal entry to today's daily note). Must
    stay deterministic, no silent LLM, mutations declared `Mutating: true`.

## Recommended order

~~**#1 builtin tools → #2 process-inbox → #5 triage TUI**~~ ✅ all done.

Theme: stop building capture/infrastructure, start building **processing**. Capture is
solved (CLI, phone, offline queue). The processing loop is now closed interactively
(`qi inbox`) and programmatically (`skill.process-inbox`). Next focus shifts to the
remaining medium item — #7 unified `qi search`.
