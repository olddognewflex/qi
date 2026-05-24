# `qi daily` — Design

Status: **approved design, not yet built**. Three subcommands: `daily start`, `daily cp`, `daily end`.

`qi daily` is the first code in the repo that **writes** daily notes. Today nothing in `qi` writes
the daily note; the user hand-authors `## Schedule`, and `LocalProvider`
(`internal/calendar/local.go`) only reads it to feed `AgendaService`.

> **Bundled bug fix.** Daily notes live at a nested, date-formatted path (`30-daily/YYYY/MMMM/YYYY-MM-DD.md`,
> e.g. `30-daily/2026/May/2026-05-24.md`), but `local.go:42` hardcodes a flat `30-daily/YYYY-MM-DD.md`.
> `eventsForDay` errors on the missing path and `Events` swallows it (`continue`), so **local `## Schedule`
> events are silently dropped** from the agenda today. This change introduces a shared path resolver and
> fixes `LocalProvider` to use it.

## Goals

1. `daily start` — compute today's agenda, write it into the daily note, open Obsidian to that note.
2. `daily cp <text>` — append a timestamped checkpoint to the note's `## Logs` section.
3. `daily end` — summarize `## Logs` via the AI planner, confirm, write a `## Summary`.

## Decisions

| Concern | Decision | Why |
|---|---|---|
| Daily file creation | **Obsidian owns it** (Templater `<% %>` template); qi never scaffolds | qi can't run Templater JS in Go; one template source of truth, no drift |
| qi writes | **Filesystem** (append/replace markdown sections) | Fast, confirmable, offline, clean clipboard |
| Missing note at write time | **Trigger Obsidian + poll** for the file (3s timeout), then write; error if it never appears | Keeps Templater as creator while letting qi proceed |
| `start` ordering | **Open Obsidian → wait for file → append `## Agenda`** | Obsidian creates with the real template before qi touches the file |
| Agenda destination | Separate `## Agenda` section | `## Schedule` is hand-authored input; writing agenda there would pollute/duplicate it (it's the parse source) |
| Agenda re-run | **Overwrite** the `## Agenda` body | Idempotent; reflects latest calendar |
| Checkpoint line | `- [HH:MM] text` under `## Logs` | Scannable log style |
| Summary destination | `## Summary` block, **above `## Logs`**; overwrite on re-run | At-a-glance recap kept with the day's narrative |
| `end` AI model | Planner (via qid) **summarizes read-only**; qi-cli shows summary → `y/N` → writes | Satisfies invariant #3: the LLM never mutates the vault; a cli caller does the confirmed write |
| Daily-note path | **Folder + filename split**, Obsidian tokens in `config.toml` | Matches the Obsidian Periodic Notes setting verbatim; nested date dirs supported |

## Daily-note path resolution

The path is **config-driven** and resolved in one place, used by both `LocalProvider` and the `daily` command.

`config.toml`:

```toml
daily_dir_format  = "30-daily/YYYY/MMMM"   # folder, relative to vault, with date tokens
daily_file_format = "YYYY-MM-DD"           # filename stem (".md" appended)
```

Defaults preserve today's flat behavior: `daily_dir_format = "30-daily"`, `daily_file_format = "YYYY-MM-DD"`.

Token translation (Obsidian/moment → Go layout), applied longest-token-first to avoid partial matches:

| Token | Go layout | Example (2026-05-24) |
|---|---|---|
| `YYYY` | `2006` | `2026` |
| `MMMM` | `January` | `May` |
| `MMM` | `Jan` | `May` |
| `MM` | `01` | `05` |
| `DD` | `02` | `24` |

Resolver (in `internal/config`):

```go
func (c Config) DailyNotePath(day time.Time) string {
    dir  := day.Format(translateTokens(c.DailyDirFormat))   // "30-daily/2006/January"
    file := day.Format(translateTokens(c.DailyFileFormat))  // "2006-01-02"
    return filepath.Join(c.VaultPath, dir, file+".md")
}
```

`LocalProvider` gains the resolver (replacing the flat `DailyDir` join at `local.go:42`); both `agenda.go:55`
and `cmd/qid/main.go:112` wiring pass it from config.

## Architecture (respects the layered CLI: `cmd → commands → service → vault`)

### `internal/vault/daily.go` — section-aware markdown helpers (pure I/O, no business logic)

Path resolution lives in `internal/config` (`DailyNotePath`); these helpers take a resolved `path`.

- `ReadSection(path, heading string) (body string, found bool, err error)` — text between `## heading` and the next `## ` (or EOF)
- `ReplaceSection(path, heading, body string) error` — overwrite the section body; create the section if absent (see placement rule)
- `AppendToSection(path, heading, line string) error` — append a line; create the section on demand (used by `cp`)

**Section placement when creating a managed section.** Managed order is `## Agenda` → `## Summary` → `## Logs`.
Insert above `## Logs` if it exists, else append at end of file. Never reorder or rewrite content
Obsidian/Templater produced — only the three managed sections are qi's to manage.

### `internal/service/daily_service.go`

```go
type DailyService struct {
    PathFor    func(time.Time) string // cfg.DailyNotePath
    Agenda     service.AgendaService
    VaultName  string                 // for the obsidian:// URI
    OpenFunc   func(string) error     // injectable (openURL) for tests
    CreateWait time.Duration          // 3s
}
```

Responsibilities: ensure-note (trigger + poll), render agenda lines, append checkpoint, read logs.
Holds the poll-for-file loop so commands stay thin.

### `internal/commands/daily.go`

`newDailyCommand(cfg config.Config) *cobra.Command` with subcommands `start` / `cp` / `end`.
Register via `root.AddCommand(newDailyCommand(cfg))` in `root.go`. Thin handlers: parse, call service, format.

## Subcommand flows

### `qi daily start`
1. Fire `obsidian://adv-uri?vault=<v>&daily=true` (creates today via Templater) and `openURL` it.
   (Advanced URI plugin scheme is `adv-uri`, not `advanced-uri`.)
2. Poll `cfg.DailyNotePath(today)` until it exists or **3s** timeout → error if never appears
   (likely Advanced URI plugin missing or Obsidian cold-starting).
3. `Agenda.Today()` → render lines `- HH:MM[–HH:MM] Title [#project]` (reuse the `agenda` command's format).
4. `ReplaceSection(path, "Agenda", rendered)`.

### `qi daily cp <text>`
1. Resolve `cfg.DailyNotePath(today)`; ensure it exists (same trigger + poll if missing).
2. `AppendToSection(path, "Logs", "- [" + now.Format("15:04") + "] " + text)`.

### `qi daily end`
1. `ReadSection(path, "Logs")`. Empty/absent → print "nothing to summarize", exit 0.
2. `dialClient` → `buildPlanner` → `planner.Run(ctx, "Summarize these daily logs:\n" + logs)`.
   Read-only: no tool mutation, never hits the approval queue.
3. Print the summary; prompt `y/N`.
4. On `y`: `ReplaceSection(path, "Summary", summary)` — lands above `## Logs`.

## Dependencies & risks

- **Advanced URI plugin required** for the `daily=true` creation trigger. If absent, the poll times out;
  surface a clear error ("daily note not created — install the Advanced URI plugin or open the note manually").
- Poll is best-effort; a cold Obsidian launch may exceed 3s → timeout error.
- `daily end` needs qid running and `[ai]` configured; reuse the existing dial/error paths from `ai.go`.

## Invariants honored

- **Markdown canonical** — all writes are plain markdown to the vault; nothing in SQLite.
- **AI opt-in, confirm-before-write** — the planner only summarizes (read-only); the human confirms; qi-cli
  performs the write as a `cli` caller. No non-cli mutation path is added.
- **Obsidian compatibility** — `## Schedule` (the parse source) is never written by qi; agenda goes to `## Agenda`.

## Tests

- `internal/config/config_test.go` — `translateTokens` (YYYY/MMMM/MMM/MM/DD, longest-first) and
  `DailyNotePath` for both nested (`30-daily/2026/May/2026-05-24.md`) and default flat layouts.
- `internal/calendar/local_test.go` — `LocalProvider` reads `## Schedule` from a nested date path
  (regression for the flat-path bug); events parse correctly from the resolved location.
- `internal/vault/daily_test.go` — section read/replace/append round-trips; placement above `## Logs`;
  create-on-demand; idempotent overwrite; no network.
- `internal/service/daily_service_test.go` — fake `AgendaService` + temp dir: agenda render, checkpoint append,
  poll-timeout path (inject an `OpenFunc` that never creates the file).
