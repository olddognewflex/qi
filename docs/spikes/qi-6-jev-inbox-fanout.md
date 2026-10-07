# QI-6 spike: Jev fan-out for full inbox triage proposals

**Question.** Can the opt-in TypeSafe inbox classifier propose a *whole*
triage — action, destination project/client, due/scheduled date, and "you
already have this task" — instead of just task|note|archive, at acceptable
cost and accuracy?

**Answer.** Yes for destination and dates, with a confidence floor and the
"lean" request shape; yes for duplicates with a high threshold. Note
destination is deferred (no data). Separately, the real inbox exposed a bigger
problem the spike was not looking for: today's action question is miscalibrated
for machine-generated email captures.

## What was built

All in `internal/typesafe` (still a stdlib-only leaf; nothing wired into
`qi inbox` yet):

- `InboxClassifier.ProposeInbox` (`inbox_fanout.go`): the existing per-capture
  action question plus, in the same request, a destination Choice over the
  configured projects/clients (+ `none`) and the date-extraction cookbook's
  date-part Choices (mode, role due|scheduled, month, day, relative anchor,
  weekday, week). With nothing enabled the request is byte-identical to
  `ClassifyInbox`'s.
  - `Lean` mode: destination descriptions move into shared state (criteria
    point at `destinations.<key>`), and only `date_mode` rides in the first
    request; the other date parts go in a second request **only for captures
    whose mode found a date**.
  - Each capture carries `captured_on`, and relative dates resolve against
    *capture* time, not triage time — "tomorrow" written last week means last
    week's tomorrow. (The capture filename already records this.)
- `ResolveDate` (`dateparts.go`): all calendar math in Go. Confidence = min
  over the parts used. Impossible dates (incl. a Feb 29 rolled into a
  non-leap year), missing parts, a missing role, an unknown capture time, or a
  date before the capture was written → `review` (never a guess).
  - A failed Lean date-detail request is a per-capture `DateErr`, not a batch
    failure; destination keys are validated (no `none`, no duplicates, path-
    safe characters).
- `InboxClassifier.CheckDuplicates` (`inbox_dup.go`): one Noul per (capture,
  shortlisted open task) pair, one request per batch. The shortlist is a
  lexical Jaccard top-5 in code.
- Live eval harness (`inbox_eval_test.go`, build tag `typesafe_eval`, never in
  `go test ./...`) + a 42-capture synthetic labelled set in `testdata/`.

## Results

Model `jev-latest`, batches of 25, October 2026.

### Synthetic set (43 hand-style captures, 12 open tasks, fictional clients)

| judgment | full | lean |
|---|---|---|
| action | 100% | 100% |
| destination (task captures) | 97% (28/29) | 97% (28/29) |
| date: no-date → none | 96% | 93% |
| date: dated → exact date + role (incl. explicit year) | 86% (12/14) | 86% (12/14) |
| date: must-review (Feb 30, day w/o month) | 2/2 | 2/2 |
| dup: true duplicates found (≥0.5) | 7/7 | 7/7 |
| dup: non-duplicates left alone (≥0.5) | 33/36 | 33/36 |
| **input tokens per capture** | **2,549 (11.5×)** | **1,515 (6.8×)** |
| wall time per 25-capture batch | ~0.6 s | ~0.57 s (2 requests) |

Action-only today: 222 input tokens/capture on this set. Absolute dates
carry a year part (capture year −1 … +5, `none`, `other`); a stated year is
honoured, `other` goes to review.

Remaining date misses are **role** (due vs scheduled: "pay water bill
tomorrow" — arguably either) and **review false alarms** on captures adjacent
to one with a real date (see *cross-capture bleed*); all below 0.7
confidence, and a review shows no date rather than a wrong one.

Destination: the one miss ("friday - ship TPS contract amendment" → none) was
at 0.42 confidence, below the floor. In an earlier run with real project names,
the one miss was a side project that is not a configured client matching a
configured project's description — descriptions need to say what a project is
*not* when names overlap.

Duplicates (top candidate probability):

| threshold | full: caught / false flags | lean: caught / false flags |
|---|---|---|
| 0.5 | 7/7 / 3 | 7/7 / 3 |
| 0.85 | 4/7 / 0 | 4/7 / 1 |
| 0.9 | 4/7 / 0 | 4/7 / 0 |

### Real inbox (49 labelled captures, lean; data kept out of the repo)

The real `00-inbox/` is ~1,950 captures, **all** machine-written email digests
(`Email: <subject> — <sender> [read_now|reply_needed|…]`) — no hand-typed
captures at all.

| judgment | result |
|---|---|
| action | 49–55% overall across runs; 85–96% at confidence ≥ 0.5–0.6 (about half covered), 100% at ≥ 0.8–0.9 |
| destination (task captures) | 83% overall; 100% at ≥ 0.5 (10/12 covered) |
| dates (task proposals only) | 1 confident false date ("renewal in 3 days" → tomorrow, 0.95): the `in_n_days` gap |
| input tokens per capture | 867 (3.6× this set's 240 action-only) |

Every action miss is an archive-worthy email (PR bot comments, promos, alerts)
proposed as task/note **at low confidence**. And the action answer differed
between the action-only request and the fan-out request on 11/49 real
captures (12/49 in the latest run) — all near-ties, so extra questions in
the request can tip them.

## Findings

1. **Lean is the shape to ship.** About the same accuracy as full for ~60% of
   the tokens; gating date parts on `date_mode` also removed a cross-capture
   bleed (below), because the second request only holds dated captures.
2. **Confidence floors do the work.** Dest ≥ 0.7, date ≥ 0.5, dup ≥ 0.85–0.9:
   below the floor the TUI should show *no* proposal rather than a wrong one.
3. **Ask for and apply dates only on captures proposed as tasks.** In the
   first real-data run, three of four confident false dates were on promo or
   system-notice emails the action question correctly archived/noted. Lean
   now skips date details for non-task proposals, which removed them and cut
   tokens further.
4. **Cross-capture bleed exists.** With ~225 questions per request, a few
   low-confidence answers took a neighbouring capture's date ("friday"
   answered with the previous capture's "tomorrow"). Lean + floors suppressed
   every confident case seen; batch size is a lever if it reappears.
5. **Relative dates must anchor to capture time** — now done via
   `captured_on`; triage often happens days later.
6. **The lexical shortlist is enough at this scale** (7/7 true duplicates were
   in the top 5). Note `internal/index` is file-granular — one FTS row per task
   *file* — so it cannot shortlist individual task lines; a Go-side lexical
   shortlist over `TaskService` open tasks is the practical path.
7. **Gap:** "in 3 days" / "within a week" have no anchor option, so they read
   as "none" or (once, confidently) as "tomorrow". An `in_n_days` anchor + a
   small-number Choice would cover them.
8. **Biggest real-world finding (outside this ticket's four questions):** the
   inbox is ~100% email digests, ~46% of them **exact repeats** (1,952
   captures, 1,060 distinct subject lines), and today's action prompt ("a
   quick capture … written by the user to themselves") doesn't fit them. Two
   cheap fixes, no extra model calls: collapse exact repeats in Go before
   triage, and give email captures their own context (or use the upstream
   `[read_now|reply_needed|…]` tag as the heuristic).
9. **Real-data dup eval not run**: exporting the real open-task list for the
   harness was not permitted in the spike session; the synthetic set covers it.
   Re-run with `QI_EVAL_OPEN_TASKS` pointed at a local export.

## Go / no-go

| question | verdict | follow-up |
|---|---|---|
| destination (project/client) | **go** (lean, floor 0.7, needs `description` in config) | Story A |
| dates | **go** (lean, floor 0.5, task-only, capture-time anchor) | Story B |
| duplicates vs open tasks | **go** (floor ~0.85, Go lexical shortlist) | Story C |
| note destination | **deferred** — no note-like captures in the real inbox; needs a NoteService append path | — |
| email-digest triage (new) | **go, do first** — deterministic, no model calls | Story D |

## Reproducing

```sh
TYPESAFE_API_KEY=... go test -tags typesafe_eval -run TestInboxFanoutEval -v ./internal/typesafe
# variants / private data (keep real captures, tasks and config out of the repo):
QI_EVAL_LEAN=1 QI_EVAL_SET=/path/real.jsonl QI_EVAL_OPEN_TASKS=/path/open.txt \
  QI_EVAL_DESTINATIONS=/path/destinations.json \
  QI_EVAL_REPORT=/path/report.md go test -tags typesafe_eval -run TestInboxFanoutEval ./internal/typesafe
```

Labels are one annotator's judgment; action labels for email captures are the
most subjective.
