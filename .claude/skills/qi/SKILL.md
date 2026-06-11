---
name: qi
description: Drive the qi CLI from inside a coding session to manage the user's personal vault — capture a thought, add/complete/list tasks, create or search notes, and check the agenda. Use whenever the user says things like "capture this", "add a task", "remind me to", "note that", "what's on my agenda", "mark that done", "what are my open tasks", or otherwise wants something recorded in their qi vault without leaving the session. qi writes Obsidian-compatible markdown; it is local-first, deterministic, and has no AI on the hot path.
---

# qi

`qi` is the user's local-first personal assistant. It reads and writes an Obsidian-compatible
markdown vault. Use it to record tasks, notes, and quick captures, and to read back the
agenda — all from within a session, without making the user switch context.

`qi` is already on PATH (`/Users/raymonddoran/.local/bin/qi`). Invoke it directly with `Bash`.
If `qi` is ever not found, build it: `go build -o bin/qi ./cmd/qi` and call `./bin/qi`.

## When to use this skill

Trigger on phrasing like:
- "capture this / jot this down / save this idea" → `qi capture`
- "add a task / remind me to / I need to / TODO" → `qi task add`
- "mark X done / I finished X / complete that" → `qi task done`
- "what are my tasks / what's open / show my todos" → `qi task list`
- "make a note / note that / write up X" → `qi note new`
- "find my note about X / search notes" → `qi note search`
- "what's on my agenda / what's today / this week" → `qi agenda`
- "process my inbox / triage captures" → see Gotchas (needs a TTY; prefer `--dry-run` here)

When the user's intent is ambiguous between a fleeting thought and a tracked task, prefer
`qi capture` (zero-friction, reversible) and tell them you captured it — they can triage later.

## Core commands

### Capture — fast, no friction
```bash
qi capture "<text>"          # writes a timestamped .md to 00-inbox/. Alias: qi c
```
Use for half-formed thoughts. Hot path is <100ms, no network. Always safe and reversible.

### Tasks
```bash
qi task add "<text>"                          # add to the task inbox
qi task add "<text>" --project <tag>          # tag with a free-form #project  (-p)
qi task add "<text>" --client <name>          # route to a configured client's task file (-c)
qi task add "<text>" --due 2026-06-30         # due date, YYYY-MM-DD  (-d)
qi task add "<text>" --schedule 2026-06-20    # scheduled/start date, YYYY-MM-DD  (-s)
qi task list                                   # list open tasks
qi task done "<fuzzy>"                          # complete a task by fuzzy match (see Gotchas)
```
- `--project` and `--client` are mutually exclusive; `--client` must be a name from the config.
- Dates are strict `YYYY-MM-DD`. Resolve relative dates ("next Friday") to an absolute date
  yourself before calling — the current date is in the session memory context.
- Task lines stay Obsidian-compatible: `- [ ] text #project 📅 YYYY-MM-DD`.

### Notes
```bash
qi note new "<title>"                       # create a note in the main vault's notes dir
qi note new "<title>" --body "<text>"       # with an initial body  (-b)
qi note new "<title>" --project <tag>       # write into that project vault's notes  (-p)
qi note new "<title>" --client <name>       # write into that client vault's notes   (-c)
qi note search "<query>"                     # full-text search (FTS index, notes only)
qi note list                                 # list all notes
```
If `qi note search` returns nothing right after creating notes, the index may be stale —
`qi index rebuild` regenerates it from markdown (derived state, always rebuildable).

### Agenda (read-only)
```bash
qi agenda            # today's events (default)
qi agenda today
qi agenda week
```
Aggregates the local daily-note `## Schedule` block plus any configured calendars.

## Gotchas

- **Interactive commands need a TTY and will error in a non-interactive Bash call.**
  - `qi task done` with no arg (or an ambiguous fuzzy match) opens a picker — instead pass a
    specific query that matches exactly one task; it then asks a `[y/N]` confirm you also
    can't answer non-interactively. If you can't get an exact single match, list the tasks
    with `qi task list`, show the user the candidates, and ask which to complete.
  - `qi inbox` is a Bubble Tea TUI — don't run it bare. Use `qi inbox --dry-run` to print the
    proposed triage (task/note/archive per capture) without writing, then relay it.
- **Mutations are real and immediate from the CLI.** `qi` (cli caller) writes to the vault
  directly — there is no approval gate on the CLI path. Confirm with the user before
  completing or routing tasks if intent isn't explicit. Captures and note/task *adds* are
  low-risk and reversible; completing tasks edits existing files.
- **Vault path must resolve.** Every `qi` command loads config at startup; if `QI_VAULT_PATH`
  / config is unset it exits with `config error:`. That's an environment problem, not a usage
  error — surface it rather than retrying.
- **Don't edit vault markdown directly to do these operations.** Go through `qi` so task-line
  format and the inbox/notes layout stay consistent.

## Quick recipes

- User: "remind me to email the client tomorrow" →
  `qi task add "Email the client" --due <tomorrow-as-YYYY-MM-DD>`
- User: "capture: idea for the onboarding flow" →
  `qi capture "idea for the onboarding flow"`
- User: "what do I have open?" → `qi task list` (then summarize)
- User: "note down the meeting decisions" →
  `qi note new "Meeting decisions" --body "<the decisions>"`
- User: "what's my week look like?" → `qi agenda week`
