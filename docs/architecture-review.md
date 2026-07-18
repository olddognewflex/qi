# qi — Senior Architecture Review

**Scope:** architecture, ease of use, AI implementation and usage.
**Method:** full-source review (three parallel deep-dives + independent verification of load-bearing claims). All cited paths verified against the working tree at commit `302af68`.
**Snapshot:** 27,834 Go LOC across 167 files (≈15.5k source / ≈12.3k test), 26 packages, 61 test files. `go test ./...` — all pass. `go vet` clean.

---

## Executive summary

qi is a genuinely well-architected local-first system. The stated invariants — markdown is canonical, capture stays under 100ms, AI is opt-in and approval-gated, Obsidian task lines round-trip — are not aspirational README copy; they are visible in the code as compiler-enforced layering, a disciplined derived-state model, a single policy chokepoint in the daemon, and load-bearing round-trip tests. For a ~15k-LOC personal tool, the engineering rigor (TOCTOU-guarded file writes, debounce race handling, audit-log replay, a reflection-based anti-drift test) is well above the norm.

The two places where the project falls short of its own standard:

1. **Onboarding is broken at the front door.** `config.Load()` runs inside `NewRootCommand` and exits on error (`internal/commands/root.go:17-21`), so a fresh install with no config cannot run *any* command — including `qi --help` and `qi doctor`, the exact tools meant to rescue a new user. There is no `qi init`.
2. **The AI-safety story is oversold in its own docs.** The approval gate is real and structural *inside qid*, but it rests on two conventions: a client-asserted `caller` string (any same-UID process can claim `"cli"`) and a self-declared `Mutating` flag with no drift guard. The docs say "enforced structurally… can never mutate"; the accurate claim is "enforced for honest clients, bounded by 0600 socket permissions."

Both are cheap to fix relative to their impact.

### Scorecard

| Area | Verdict |
|---|---|
| Layering & dependency direction | **Strong** |
| Three-binary split (CLI / daemon / MCP) | **Strong** |
| State management & robustness | **Strong** |
| Testing | **Strong** |
| Extensibility | **Strong** |
| Package cohesion | Adequate (one god-file) |
| CLI surface design | Adequate |
| Onboarding / first run | **Weak** |
| Diagnostics & error messages | **Strong** |
| Docs | Strong reference, weak first-run path |
| Config (advanced) | Weak — complexity cliff, no introspection |
| AI safety architecture | **Strong**, with a threat-model caveat |
| AI planner | Adequate |
| MCP (both directions) | **Strong** |
| Deterministic skills | **Strong** |
| Semantic search | Adequate — staleness gap |
| Agent-facing surface | Strong, missing `--json` |

---

## 1. Architecture

### 1.1 Layering — strong, and real

The documented dependency direction (`cmd → commands → service → {vault, index, calendar} → domain`) holds with zero violations in the full internal import graph. `domain` imports nothing internal; `vault` imports only `domain`; `notify` deliberately takes plain `func()` and strings rather than importing `service`; `index` never imports `embed` (the command embeds the query and passes `[]float32` in). The only wide importers are the two composition roots (`internal/commands`, `cmd/qid/main.go`), which is exactly where width belongs.

Commands are genuinely thin — flag parsing and output formatting; business logic lives in `internal/service` as documented. The CLI path and the daemon path do **not** duplicate logic: builtin daemon tools (`internal/tools/builtin/task.go`) wrap the same `service.TaskService` the CLI uses. The duplication that does exist is *wiring*: `service.TaskService{...}` is constructed literal-style at 5 call sites with no constructor, so a future required field is a latent drift surface. Cheap fix: `service.NewTaskService(cfg)`.

### 1.2 Package cohesion — one god-file

`internal/config/config.go` is 815 lines with 31 type declarations — 14 pairs of public-struct/private-TOML-wire twins plus load, validation, flattening, and launch resolution. The wire/domain separation is the right idea; the concentration is the problem. Split into `config_calendar.go`, `config_client.go`, `config_launch.go`. Everything else is well-scoped; second-largest files (`calendar/caldav.go` at 602, `commands/task.go` at 512) are cohesive.

### 1.3 State management — exemplary derived-state discipline

- FTS rows, embeddings, and task-sync state are all explicitly rebuildable-from-markdown; nothing lives only in SQLite (invariant #1 holds).
- The `last_indexed` marker exists precisely because the db *file's* mtime lies (task-sync writes bump it without touching FTS — issue #44); `qi doctor` compares against the marker via a read-only open (`internal/index/index.go:303`, `?mode=ro`). This is the kind of honest freshness tracking most projects skip.
- Concurrency is careful where it matters: registry under `sync.RWMutex` with all-or-nothing dynamic registration; per-connection write serialization in the daemon (`internal/daemon/server.go:110`); the watcher debounce correctly drains a fired-but-unreceived timer tick so no phantom reconcile fires (`internal/watcher/watcher.go:118-133`).
- Approval durability: in-memory queue + append-only JSONL audit, replayed by `Queue.Restore` (`internal/approval/queue.go:226`) on restart — interrupted mutations re-materialize as pending. The chosen trade-off (no-silent-loss over no-double-execute) is documented in code and correct for a confirm-gated system. Known small gap: a crash between approve and result-record can re-queue an already-executed mutation; for id-minting `task.add` that means a duplicate task, which is visible and reversible.

### 1.4 Robustness — strong

Task-file mutations are file-locked across the whole read-verify-write with re-parse and stable-ID matching, so a concurrently renamed task still updates the right line and never flips the wrong checkbox (`internal/vault/tasks.go:266-320`). Appends are locked too (unlocked `O_APPEND` could hit a renamed-away inode). Writes are temp+rename atomic. The calendar builder degrades per-calendar into `BuildWarning`s instead of sinking the agenda. Error taxonomy in the daemon maps typed sentinels to distinct JSON-RPC codes. Exactly one `panic` in non-test code (crypto/rand failure — legitimate). 64% of `fmt.Errorf` sites wrap with `%w`.

### 1.5 Testing — strong shape

24 of 26 packages have tests; the untested ones (`cmd/*` mains, `domain` pure types, `tui`) are exactly where tests add least. The load-bearing tests exist and are substantial: vault round-trip (690 lines, guards Obsidian invariant #4), recurrence/fuzzy-match in service (525), RRULE + RECURRENCE-ID override precedence (627), plus daemon, policy, approval, watcher, and sync coverage.

### 1.6 Extensibility — a standout pattern worth copying

Adding a command, tool, or skill is a one-file-plus-one-line affair. The highlight is the **reflection-based anti-drift test** for calendar providers (`internal/calendar/build_test.go:72-131`): it walks `config.Config` by reflection for `*Calendars` fields and fails with a prescriptive error naming the exact wiring steps if a new calendar kind reaches config without reaching the single `ProviderBuilder.Build`. It encodes an actual historical bug (qid once wired only `LocalProvider`, silently hiding every remote calendar behind the daemon) as a permanent guard.

The glaring asymmetry: the `Mutating` flag — which the entire AI-approval invariant depends on — has **no equivalent guard** (see §3.1).

### 1.7 Platform hygiene — clean

All six platform-split files carry correct build constraints with complete darwin/!darwin and unix/!unix pairs (`dataless_{darwin,other}.go`, `launch_exec_{unix,windows}.go`, `vault/lock_{unix,other}.go`). No OS falls through a gap.

---

## 2. Ease of use

### 2.1 First run — the single worst problem in the project

`NewRootCommand` calls `config.Load()` and `os.Exit(1)` on failure before any command is registered (`internal/commands/root.go:17-21`). A brand-new user with no config gets `config error: vault_path is required…` from **every** invocation — `qi --help`, `qi doctor`, `qi config edit` included. The diagnostic tool designed to explain setup problems cannot run when the setup problem exists. There is no `qi init`; `qi config edit` creates an *empty* file, not a commented template, and errors without `$EDITOR`.

The error message itself is excellent ("set vault_path in config.toml or QI_VAULT_PATH env var") — the machinery around it is what fails.

**Fix (highest ROI in this review):**
1. Defer `config.Load()` out of the constructor (per-command `PreRunE`, or exempt `help`/`doctor`/`config`/`init`).
2. Add `qi init`: prompt for vault path, write a commented starter config.
3. Seed `config edit` with a template.

### 2.2 CLI surface — coherent core, fraying edges

18 top-level commands. The core verb set is consistent (`add/list/done/new/search`, uniform `-p`/`-c`), defaults are sensible, and long help on the complex commands is genuinely good. The fraying:

- `remote-drain` / `remote-status` are hyphenated top-levels instead of `qi remote drain|status` — inconsistent with every other command family.
- Bare `qi note` silently creates an untitled inbox note **and opens Obsidian** — a side-effecting surprise where every sibling group command shows help. Undocumented anywhere.
- `qi note search <q>` duplicates `qi search <q> --kind note`. Keep one, alias the other.
- `-s` means `--schedule`, `--status`, or `--semantic` depending on the command — muscle-memory hazard.
- No cobra `Example` blocks anywhere; the date/repeat syntax especially needs them.

### 2.3 Feedback, failures, diagnostics — strong

Error messages consistently name the remedy (`"qid not reachable at %s; start it with \`qid &\`"`). `qi doctor` is a model diagnostics story: per-component ok/warn/fail, warn (not fail) for optional/lazy components, iCloud-evicted-file detection that is deliberately stat-only because reading would trigger the very download it warns about, and a watcher-status RPC distinguishing "socket up" from "watcher actually running." `--dry-run` exists on every command where it matters. Two doctor gaps: unreachable on a fresh install (§2.1), and calendar/client config semantics aren't validated (agenda surfaces build warnings; doctor doesn't).

### 2.4 TUIs — good, but no non-TTY fallback

Both Bubble Tea components (task picker, inbox triage) are clean: vim keys, help footers, abort semantics, and a nice touch where enter with nothing selected acts on the cursor row. But `task done`/`schedule`/`breakdown` invoke the picker with no TTY guard — in a pipe, an ambiguous match dies with a raw bubbletea error. `qi inbox` has `--dry-run` as its headless path; the pickers have nothing. Detect no-TTY and print candidates with an instruction instead.

### 2.5 Config — one-line floor, cliff after that

Basic use is a single `vault_path` line: excellent floor. Advanced use is a genuine cliff: ~15 top-level TOML sections; the client/project model with `path`-relative resolution where absolute values escape the base; a four-tier launch-inheritance chain (`[client.project.launch]` > `[client.launch]` > `[launch]` > env); synthetic client-projection projects deliberately excluded from name resolution. Load-time validation is specific and good — but there is **no introspection**: no `qi config show`, no `qi launch harness --print`, no way to ask "which harness/vault/cwd will `$WORK_CONTEXT` get me?" without running it for real. For a system this resolution-heavy, inspectability is the missing feature, not more docs.

### 2.6 Docs & DX

README (630 lines) is comprehensive and accurate — architecture diagrams, full CLI reference, config schema, AI trust model. But it is the *only* home of the config schema (nothing in-terminal), there is no "first task in 60 seconds" path that survives §2.1, and bare-`qi note` behavior is documented nowhere. Developer experience is strong: plain `go build`, no CGO (pure-Go SQLite), broad tests, a Makefile that even solves the macOS codesigning/TCC-grant problem with an inline explanation.

---

## 3. AI implementation and usage

### 3.1 Safety architecture — structurally real, honestly bounded

**What's genuinely structural:** every tool call from every surface (CLI client, planner, MCP bridge) funnels through one chokepoint — `Server.toolsCall` runs `policy.Decide` before `tools.Execute` (`internal/daemon/server.go:252-278`), and the only other `Execute` site runs post-human-approval (`server.go:317`). The default policy is fail-closed and four lines of logic: empty caller → deny; `cli` → allow; read-only → allow; mutating + non-cli → confirm (`internal/policy/policy.go:71-82`). Approval executes server-side from **stored** params, so a planner cannot swap arguments between proposal and approval. External MCP tools are hard-coded `Mutating: true` (`internal/mcp/manager.go:117`) — the right conservative default.

**The two caveats that should be stated in the project's own docs:**

1. **Caller identity is client-asserted.** `caller` is a JSON field the client sends (`internal/daemon/server.go:226`); the server trusts it verbatim. The actual trust boundary is the 0600 socket — any same-UID process can dial and claim `"cli"` (indeed `qi ai tools call --caller cli` is a documented flag). The gate therefore defends against *honest* AI clients and other users, not against compromised same-user code. That is a perfectly defensible threat model for a single-user local daemon — but "can never mutate" in CLAUDE.md/AGENTS.md overstates it. One paragraph documenting the real boundary fixes this.
2. **`Mutating` is self-declared with no drift guard.** A new skill or builtin that forgets `Mutating: true` silently bypasses the approval queue — a direct hole in invariant #3, and unlike the calendar wiring there is no reflection test watching it. This is the cheapest high-value fix in the AI layer: a registry-walk test asserting every tool whose handler touches the vault declares mutation, or an API split (`RegisterMutating` / `RegisterReadOnly`) that makes the choice explicit.
3. **The agent-driven CLI is the widest practical gap.** The `.claude/skills` files are commendably honest that CLI mutations are "real and immediate — confirm with the user first," but that confirmation is prose convention, not structure. An agent with shell access that ignores the skill text mutates freely. Inherent to shipping a CLI, worth acknowledging as the boundary of the model.

**Durability:** audit-log replay on restart is solid (§1.3). The audit log is a history record, not a tamper-evident trail (no hash chain, same-user-writable) — fine, but say so.

### 3.2 Planner — clean loop, no multi-step future yet

The tool-use loop is provider-neutral (`LLM` interface), bounded (8 iterations), handles Anthropic's empty-input edge case and tool-name sanitization with collision detection. Pending-approval results are correctly fed back as terminal tool errors telling the model to stop.

Weaknesses, in order:

- **No resume across approval.** Planner sessions are ephemeral; when a human approves, the tool executes directly (`server.go:317`) and the LLM never sees the result. "Add a task, then act on its id" cannot complete. This caps qi's AI at single-mutation errands — acceptable today, but it's the ceiling on the whole `qi ai run` feature.
- **Stale default model.** `DefaultAnthropicModel = "claude-sonnet-4-6"` (`internal/ai/anthropic.go:14`) is not a current model id (current lineup: `claude-sonnet-5`, `claude-opus-4-8`, `claude-haiku-4-5-*`). Likely 404s at runtime for anyone who hasn't set `[ai] model`. Pin a current id and add a doctor check.
- **Prompt-cache economics:** only the system block is cache-marked; the tools array re-pays input tokens every iteration. Low-effort win.
- Ollama tool-call correlation is order-based with fabricated ids — documented, fragile only under parallel calls.

### 3.3 MCP — strong in both directions

Consuming: namespaced `mcp.<serverID>.<tool>`, all-or-nothing dynamic registration with cross-source collision rejection, raw schema pass-through, `IsError` surfaced properly. Republishing: `qimcp.Bridge` forwards `caller="mcp:<sessionID>"` so every downstream mutation lands in the approval queue, and renders pending as a clear MCP error containing the approve command. Minor: `qi-mcp` snapshots the catalog at startup; tool changes need a bridge restart.

### 3.4 Deterministic skills — the best pattern in the codebase

Verified LLM-free (no ai/http imports anywhere in `internal/skills` or `builtin`). The **read-only proposer + separate gated applier** pair (`process-inbox` / `process-inbox-apply`, `weekly-review` / `weekly-review-apply`) — both driving the same service the human TUI uses — is exactly how propose-then-confirm should be built. The LLM only ever sees proposals; policy gates precisely the write half. This pattern deserves to be named in AGENTS.md as the template for future skills.

### 3.5 Semantic search — honest opt-in, stale in the shadows

The opt-in UX is right: hard error when disabled, hint to run `qi embed` when empty, visible scores. Clean layering (index stores/ranks vectors, never imports the embed client). The gaps:

- **Invisible staleness.** The watcher keeps FTS fresh per-file but never re-embeds; only deletion drops a vector. After editing a note, FTS is current and its embedding is stale until the next manual full `qi embed` (which is always a full rebuild — `ClearEmbeddings` + walk). `qi doctor` checks FTS freshness only, so the drift is undetectable. Add an embedding-freshness check to doctor, and either re-embed on `UpsertFile` (behind the enabled flag) or document the lag.
- **Dim mismatch is silently tolerated.** `dim` is stored but never validated; `cosine` compares the shorter prefix. Switching embed models without a rebuild yields garbage rankings instead of an error. Reject mismatched dims.
- Whole-note embedding (no chunking) — long notes silently truncate at the model's context; linear cosine scan — fine for a personal vault (thousands), not tens of thousands. Both acceptable at this scope; both worth a comment.

### 3.6 Agent-facing surface

The `qi` / `qi-triage` skills are well-written for coding agents: exact TTY gotchas, "resolve relative dates yourself," "never edit vault markdown directly." The material missing piece is **`--json` output on the read verbs** (`task list`, `agenda`, `search`) — today agents parse human-formatted prose. That one flag would make qi markedly better as an agent substrate, which is clearly a direction the project cares about.

### 3.7 Philosophy coherence

"Deterministic, explicit, no silent mutation" holds where it counts. The gaps, ranked by how much they undercut the stated philosophy: (1) same-UID caller trust vs. "structurally enforced" wording; (2) un-gated agent-driven CLI; (3) self-declared `Mutating`; (4) no planner resume; (5) invisible embedding staleness; (6) stale model default. None is a live hole for the intended single-user local design — but items 1–3 are the difference between the documented claim and the actual guarantee.

---

## 4. Prioritized recommendations

**P0 — do these first**
1. Make the CLI survive a missing config: defer `config.Load()` out of `NewRootCommand`; `--help` and `doctor` must work on a fresh install (`root.go:17-21`).
2. Add `qi init` (prompt for vault path, write commented starter config); seed `config edit` with a template.
3. Add a drift guard for `Mutating` (registry-walk test or `RegisterMutating`/`RegisterReadOnly` API) — match the rigor of `build_test.go`.
4. Fix `DefaultAnthropicModel` to a current id; add a doctor check for it.

**P1 — high value**
5. Document the real AI trust boundary (0600 socket + asserted caller) in AGENTS.md/CLAUDE.md; soften "can never mutate."
6. `qi config show` (resolved, redacted) + `qi launch harness --print` — make client/project/launch resolution inspectable.
7. `--json` on read verbs (`task list`, `agenda`, `search`) for agent consumption.
8. Embedding freshness: doctor check + re-embed on watcher upsert (or documented lag); reject dim mismatches.

**P2 — polish**
9. Split `config.go` (815 lines) into calendar/client/launch files; add `service.New*Service` constructors (5 duplicate construction sites).
10. Group `remote-*` under `qi remote`; fix bare-`qi note` surprise; collapse `note search` into `search --kind note`; add cobra `Example` blocks.
11. No-TTY fallback for the task pickers.
12. Planner resume after approval (feed executed result back into a persisted session) — the unlock for multi-step AI flows, larger effort.

---

## 5. Closing assessment

qi's core bet — markdown canonical, SQLite derived, AI behind a human gate — is executed with unusual discipline. The layering is real, the derived-state hygiene is exemplary, the propose/apply skill pattern is the correct shape for human-gated AI, and the reflection anti-drift test shows a team that converts incidents into permanent guards. The weaknesses cluster at the edges: the first five minutes of a new user's experience, and the gap between the *documented* strength of the AI gate and its *actual* (still reasonable) trust model. Both are addressable in days, not weeks. Fix P0 and the project's weakest dimension moves from "weak" to consistent with the rest of the codebase — which is to say, strong.
