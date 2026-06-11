---
name: qi-triage
description: Process / triage the user's qi inbox from inside a session — review each unprocessed capture in 00-inbox and turn it into a task, a note, or archive it. Use when the user says "process my inbox", "triage my captures", "clear my inbox", "what's in my inbox", "sort out my captures", or "inbox zero". This is the multi-step processing workflow that complements the one-shot qi capture/task/note verbs (see the `qi` skill). It drives the same InboxService the `qi inbox` TUI uses, but non-interactively via qid.
---

# qi-triage

Triage is the **processing** side of qi: captures pile up in `00-inbox/` via `qi capture`, and
each one eventually becomes a task, a note, or gets archived. This skill walks that pile from
inside a session. It pairs with the `qi` skill — that one *records*, this one *processes*.

The interactive command for this is `qi inbox`, a Bubble Tea TUI. **You cannot drive a TUI**
in a Bash call, so this skill uses the deterministic daemon path instead: the same heuristics
and apply logic (`InboxService`), exposed as two qid tools.

## When to use

Trigger on: "process my inbox", "triage captures", "clear/sort my inbox", "inbox zero",
"what's piled up", "go through my captures". For *recording* a single new item, use the `qi`
skill (`qi capture` / `qi task add` / `qi note new`) — not this one.

## Prerequisite: qid must be running

The non-interactive path goes through the qid daemon. Check it:
```bash
qi doctor          # look for the qid socket line; or:
qi ai tools list   # errors "qid not reachable" if the daemon is down
```
If qid is down, you cannot apply triage programmatically. Either:
- ask the user to start it (`qid &`), then proceed; or
- run `qi inbox --dry-run` to show them the proposed triage and let them run `qi inbox`
  themselves (the interactive TUI) to apply it.

## Workflow

### 1. List proposals (read-only)
```bash
qi ai tools call skill.process-inbox
```
Returns JSON: `{count, items:[{path, summary, proposed_action, reason}, ...]}`.
`proposed_action` is one of `task` | `note` | `archive`, from deterministic heuristics:
- empty capture → **archive** ("nothing to action")
- a line with a task marker (`- [ ]`, `[ ]`, `todo:`, `todo `, `task:`) → **task**
- a single short line (≤80 chars) → **task** ("reads as a todo")
- longer / multi-line content → **note**

> `qi inbox --dry-run` prints the same proposals in a human-readable table, but **without
> file paths** — use it to *show* the user, but use `skill.process-inbox` (JSON, has `path`)
> when you intend to apply, because apply needs the exact `path`.

### 2. Confirm with the user
Summarize the proposals and ask the user to confirm or adjust before writing. Triage mutates
the vault (creates tasks/notes, moves files to `00-inbox/archive/`). Don't auto-apply a whole
inbox without a confirm — let them veto or re-route individual items.

### 3. Apply, one item at a time (mutating)
```bash
qi ai tools call skill.process-inbox-apply \
  --args '{"path":"<exact path from step 1>","action":"task","title":"<optional>","project":"<optional>"}'
```
- `action`: `task` | `note` | `archive` (the apply tool does not delete — that's TUI-only).
- `title`: optional override for the task text / note title (defaults to the capture's first line).
- `project`: optional `#project` tag when `action` is `task`.
- Each successful apply **creates the task/note (if any) and then archives the original
  capture** into `00-inbox/archive/`, so it won't resurface next run.
- The default caller is `cli`, so the mutation runs immediately — no approval queue. (This is
  the human-in-the-terminal path; it's exactly what the `qi inbox` TUI does under the hood.)

Loop step 3 over the items the user approved. Report what was created and archived.

## Gotchas

- **No paths from `--dry-run`.** Always pull paths from `skill.process-inbox` JSON before applying.
- **`qi inbox` (bare) is a TUI** — never run it in a non-interactive Bash call; it will hang
  or error. Use `--dry-run` for preview, the daemon tools for apply.
- **Apply is per-item.** There's no batch-apply tool; iterate. If the user says "just do the
  obvious ones", apply only the items whose `proposed_action` you and they agree on.
- **`delete` is not available here.** The apply tool supports `task`/`note`/`archive` only;
  outright deletion is a TUI-only action. To discard a capture non-interactively, `archive`
  it (reversible) rather than deleting.
- **Path safety is enforced server-side** — apply rejects any `path` not sitting directly in
  the inbox, so don't try to point it elsewhere.
