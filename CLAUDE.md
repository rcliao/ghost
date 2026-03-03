# agent-memory

Persistent memory system for AI agents. Single binary, SQLite-backed, zero server dependencies.

## Architecture

- `cmd/agent-memory/main.go` — Entrypoint, delegates to `cli.RootCmd`
- `internal/cli/` — Cobra commands (put, get, list, search, context, rm, gc, export/import, etc.)
- `internal/store/` — `Store` interface + `SQLiteStore` implementation (SQLite with FTS5)
- `internal/model/` — Data types: `Memory`, `Chunk`, `FileRef`
- `internal/chunker/` — Text chunking for search indexing (400 char target)
- `internal/embedding/` — Pluggable vector embeddings (Ollama, OpenAI)
- `internal/ingest/` — Markdown file parsing into memories
- `memory.go` — Public API re-exports for library use

## Build & Test

```bash
make build     # Build ./agent-memory binary
make test      # Run all Go tests
make vet       # Run go vet
make install   # Install to $GOPATH/bin
bash test/acceptance.sh  # End-to-end CLI tests
```

## Key Patterns

- Memories indexed by (namespace, key) with automatic versioning
- Search: FTS5 ranked → LIKE fallback → vector embeddings (if configured)
- Context assembly fills a token budget scored by relevance, recency, and priority
- Soft-delete (recoverable) vs hard-delete (permanent)
- TTL/expiration support with auto-GC on startup
- Memory links (relates_to, contradicts, depends_on, refines) and file references
- DB path: `--db` flag → `$AGENT_MEMORY_DB` env → `~/.agent-memory/memory.db`
- Pure Go SQLite (modernc.org/sqlite), WAL mode, no CGo

## Conventions

See `CONVENTIONS.md`. Key rules:
- Backwards compatibility required — existing data must remain readable
- Max 8 files per task (excluding tests), max 3 packages touched
- No new dependencies without human approval
- No schema changes unless task explicitly requires it
- Human approval needed for: new tables, Store interface changes, new CLI subcommands, JSON output format changes
- All new code must have tests; use `cmd.OutOrStdout()` for testability
