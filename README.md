# Qi Starter

This is a practical Phase 1 starter for **qi**, a Go-first, local-first personal assistant.

Included:
- `docs/qi-blueprint-v2.md`
- `AGENTS.md` for Codex guardrails
- Cobra CLI bootstrap
- Task domain model
- Obsidian task line parser/formatter
- `qi task add`
- `qi task list`
- unit tests

## Quick start

```bash
go mod tidy
go test ./...
go run ./cmd/qi task add "Fix qi parser" --project qi --due 2026-04-30
go run ./cmd/qi task list
```

## Notes

- Markdown is canonical storage.
- SQLite is intentionally not included yet.
- Calendar, MCP, daemon, and AI features are intentionally deferred.
