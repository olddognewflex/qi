# 🧠 Qi — v2 Technical Blueprint (Go, Local-First, CLI-Native)

---

## 🔥 Core Philosophy (Non-Negotiable)

1. **Markdown is the source of truth**

   * All tasks, notes, captures live as files in your Obsidian vault
   * If `qi` disappears, your system still works with `grep` and Neovim

2. **SQLite is a derived index (per machine)**

   * Fast querying, FTS, caching
   * Fully rebuildable from markdown
   * Never the only copy of anything important

3. **Capture latency is sacred (<100ms)**

   * No network calls
   * No AI
   * No blocking operations

4. **Deterministic by default, AI on demand**

   * `qi task add` → no tokens
   * `qi ask` → tokens

5. **LLM suggests, never commits**

   * All AI mutations require confirmation

6. **Local-first, cross-platform**

   * macOS primary, Linux supported, Windows later

---

## 🧱 System Architecture

### Binaries

* `qi` → CLI (fast, stateless)
* `qid` → daemon (sync, watchers, jobs)
* `qi-mcp` → MCP server (AI interface)

### Flow

User → CLI → Services → Vault (truth)
→ SQLite (index)

AI → MCP → Services
Daemon → Sync + Index + Queue

---

## 🗂️ Storage Model

### Vault (Synced via Obsidian)

```
vault/
├── 00-inbox/
├── 10-tasks/
├── 20-notes/
├── 30-daily/
└── .qi/
    └── config.toml
```

### Local Machine State

```
~/.local/share/qi/
├── qi.db
├── cache/
├── logs/
└── queue/

~/.config/qi/
├── config.toml
└── prompts/
```

Secrets → keychain

---

## 🧠 Domain Models

### Task

```go
type Task struct {
    ID          string
    Text        string
    Project     string
    Tags        []string
    Due         *time.Time
    Priority    string
    Completed   bool
    CompletedAt *time.Time

    FilePath    string
    LineNumber  int
}
```

### Note

```go
type Note struct {
    Path       string
    Title      string
    Tags       []string
    ModifiedAt time.Time
}
```

### Event

```go
type Event struct {
    ID      string
    Source  string
    Title   string
    Start   time.Time
    End     time.Time
    Project string
}
```

---

## 📁 Repo Structure

```
qi/
├── cmd/
|    ├── qi/ |
| --- |
|    ├── qid/ |
|    └── qi-mcp/ |
├── internal/
|    ├── commands/ |
| --- |
|    ├── service/ |
|    ├── domain/ |
|    ├── vault/ |
|    ├── index/ |
|    ├── calendar/ |
|    ├── llm/ |
|    ├── platform/ |
|    ├── queue/ |
|    └── config/ |
├── mcp/
├── raycast/
├── migrations/
└── Makefile
```

---

## ⚡ CLI Commands (v1)

### Core

```
qi capture "text"
qi c "text"

qi task add "text"
qi task list
qi task done <fuzzy>

qi agenda
qi agenda week

qi note new "title"
qi note search "query"
```

### AI (Explicit)

```
qi ask "question"
qi digest
qi triage
```

### System

```
qi sync
qi doctor
qi service install|start|stop
```

---

## 🧩 Service Layer

Commands are thin.

All logic lives in services:

* TaskService
* CaptureService
* AgendaService
* NoteService
* SyncService
* TriageService
* CalendarService

---

## 📄 Vault Write Rules

### Capture

* Always writes to `00-inbox/`

### Task Add

* Writes to project file or inbox

### Notes

* Full markdown file

### Triage

* Suggest only (no auto apply)

---

## ⚙️ Indexing

SQLite tables:

* tasks
* notes (FTS5)
* events
* sync_state

Rebuild:

```
qi index rebuild
```

---

## 📡 Daemon (`qid`)

Responsibilities:

* file watcher
* calendar sync
* queue processing
* index updates

---

## 🔌 MCP Server (`qi-mcp`)

Tools:

* search_notes
* get_note
* add_note
* search_tasks
* add_task
* get_agenda
* capture

Strict schemas required.

---

## 🤖 AI Usage

Allowed:

* summarization
* triage
* synthesis

Forbidden:

* silent writes
* auto task creation

---

## 📱 Mobile Strategy

Phase 1:

* write to synced inbox

Phase 2:

* POST to local endpoint (Tailscale)

---

## 🧭 Build Phases

1. Core CLI (tasks, capture)
2. Calendar
3. Notes + search
4. MCP
5. Daemon
6. AI
7. Mobile

---

## ⚠️ Risks

* OAuth token issues
* timezone bugs
* Obsidian sync conflicts
* Raycast caching
* AI hallucinations

---

## 🎯 Success Criteria

* No need for calendar UI
* Capture is frictionless
* Tasks centralized
* AI adds value (not noise)
* System runs without constant changes

---

## 🧨 Final Rule

If it slows capture or adds friction — **cut it**.


