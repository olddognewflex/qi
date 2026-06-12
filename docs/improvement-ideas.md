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
7. ~~**Unified `qi search`.**~~ ✅ **Done.** The FTS index already covered *all* vault markdown
   (`Rebuild` walks the whole vault), so this was a presentation gap, not storage. Added a
   top-level `qi search <query>` with `--kind`/`-k` and `--limit`/`-n` that labels each hit by
   kind (note/task/daily/inbox/other), and scoped `qi note search` to notes. Kind is derived
   from the vault subdir in each result's path at query time — no schema change, still derived
   state; invariant-safe.
8. ~~**Due-today notifications.**~~ ✅ **Done.** Opt-in via `[notify] due_today = true` (time
   `at`, default 08:00): qid's long-running daemon sends one macOS notification each morning
   listing tasks due/scheduled today. New `internal/notify` package (osascript banner on macOS
   with safely-escaped AppleScript, `*slog.Logger` fallback elsewhere; stdlib-only); pure
   `service.FilterDueToday` does the filtering. Deterministic, read-only, no policy gate, off
   by default.
9. ~~**Recurring tasks.**~~ ✅ **Done.** The Obsidian Tasks `🔁 <rule>` marker round-trips
   losslessly: `ParseTaskLine` extracts it into `Task.Recurrence`, `FormatTaskLine` re-emits it
   in canonical order (after tags, before `⏳`/`📅`) — invariant 4 round-trip tests extended, not
   weakened. Completing a recurring dated task spawns the next occurrence (`recurrence.go`
   `NextRecurrence`; supported subset `every [N] day|week|month|year`, optional `when done`;
   unsupported rules preserve the marker but don't spawn). New `qi task add --repeat "every week"`
   flag. The completed line keeps its marker; the spawned instance gets a fresh id + advanced dates.

### Big bets

10. ~~**Time-blocking: `qi plan`.**~~ ✅ **Done.** `qi plan [date]` packs open tasks into free
    slots of the daily note's `## Schedule` (ASCII-hyphen `- HH:MM-HH:MM Title #proj` ranges the
    local provider parses, so they appear in `qi agenda` for free). Hand-authored events are
    preserved and idempotent across runs — already-scheduled titles are skipped. Pure planning in
    `service.PlanBlocks` over reusable `calendar.ParseScheduleEntries`/`ScheduleEntry.Render` (the
    single source of truth for the line format, shared with `local.go`'s regexes); flags
    `--start`/`--block`/`--limit`/`--project`/`--all`/`--dry-run`. Defaults to tasks due/scheduled
    that day.
11. **Remote queue beyond tasks.** Enqueue notes/captures too; iOS shortcut variants.
    Worker D1 schema needs a `kind` column; drain routes by kind.
12. **Local embeddings search.** Ollama provider exists — embed notes, semantic search
    alongside FTS. Derived index, rebuildable, invariant-safe. Opt-in.
13. ~~**`skill.weekly-review`**~~ ✅ **Done.** Read-only `skill.weekly-review` aggregates the
    week's completed tasks (now scoped by the new `✅ YYYY-MM-DD` done-date the task line carries —
    `ParseTaskLine`/`FormatTaskLine` round-trip it into `Task.CompletedAt`, stamped by
    `CompleteTask`), capture volume (inbox + archive, bucketed by filename date), and daily
    `## Logs` highlights into a proposed review note (title + markdown body). Gated
    `skill.weekly-review-apply` (Mutating) writes the proposal as a note via `NoteService`. Both
    live in `internal/skills/weeklyreview.go` and pair with `skill.daily-review`. Deterministic,
    no LLM; the read-only skill never writes, the apply skill routes non-cli callers through the
    approval queue.
14. ~~**qid SourceSkill for in-session task/capture (revisit).**~~ ✅ **Done.** Both candidate
    shapes shipped as gated mutating `SourceSkill`s, giving AI/MCP callers a single composed
    workflow instead of wiring individual builtin tools. `skill.quick-task` adds a task and
    returns the refreshed open-task list in one call (composition is the value over the bare
    `task.add` builtin); `skill.session-log` appends a timestamped entry to today's daily note
    `## Logs` (headless equivalent of `qi daily cp`). Both live in `internal/skills/quicktask.go`
    and `internal/skills/sessionlog.go`, compose the existing `TaskService`/`vault` helpers, call
    no LLM, and are declared `Mutating: true` so non-cli callers route through the approval queue.

## Recommended order

~~**#1 builtin tools → #2 process-inbox → #5 triage TUI**~~ ✅ all done.

Theme: stop building capture/infrastructure, start building **processing**. Capture is
solved (CLI, phone, offline queue). The processing loop is now closed interactively
(`qi inbox`) and programmatically (`skill.process-inbox`), and the **entire medium tier
(#5–#9) is shipped**. Big-bets #10 (`qi plan` time-blocking), #13 (`skill.weekly-review`) and
#14 (`skill.quick-task` / `skill.session-log`) are now done too; next focus is the remaining big
bets (#11, #12).
