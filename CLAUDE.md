# ghost

Persistent memory system for AI agents. Single binary, SQLite-backed, zero server dependencies.

## Architecture

- `cmd/ghost/main.go` — Entrypoint, delegates to `cli.RootCmd`
- `internal/cli/` — Cobra commands (put, get, list, search, context, edge, clusters, consolidate, curate, rm, gc, export/import, etc.)
- `internal/store/` — `Store` interface + `SQLiteStore` implementation (SQLite with FTS5)
- `internal/model/` — Data types: `Memory`, `Chunk`, `FileRef`
- `internal/chunker/` — Text chunking for search indexing (400 char target)
- `internal/embedding/` — Pluggable vector embeddings (local all-MiniLM-L6-v2 default, Ollama, OpenAI)
- `internal/ingest/` — Markdown file parsing into memories
- `memory.go` — Public API re-exports for library use

## Build & Test

```bash
make build     # Build ./ghost binary
make test      # Run all Go tests
make vet       # Run go vet
make install   # Install to $GOPATH/bin
```

## Key Patterns

- Memories indexed by (namespace, key) with automatic versioning
- Namespace = agent identity (`agent:pikamini`), tags = categorization (`identity`, `project:ghost`, `chat:123`)
- Tier = lifecycle stage: sensory → stm → ltm → dormant (no more `identity` tier)
- Pinned = chronic accessibility: always loaded in context, exempt from decay (replaces old `identity` tier)
- Search: FTS5 ranked → LIKE fallback → vector embeddings, all support tag filtering
- Context assembly: Phase 1 pinned, Phase 2 search, Phase 3 edge expansion (spreading activation)
- Edges: weighted directed associations (`memory_edges` table) with auto-linking on put, co-retrieval strengthening, and decay in reflect
- Edge types: `relates_to`, `contradicts` (force-include), `depends_on`, `refines`, `contains` (suppresses children), `merged_into`
- `ghost consolidate` creates summary memories with `contains` edges for hierarchical compression (LCM-like lossless compaction)
- `ghost clusters` discovers groups of similar memories connected by `relates_to` edges for consolidation review
- Reflect uses non-destructive `link_only` strategy by default: similar memories get edges instead of being merged (preserves content)
- Parent boosting: when a child is a search seed, its `contains` parent is pulled into context and children are suppressed
- Soft-delete (recoverable) vs hard-delete (permanent)
- TTL/expiration support with auto-GC on startup
- DB path: `--db` flag → `$GHOST_DB` env → `~/.ghost/memory.db`
- Pure Go SQLite (modernc.org/sqlite), WAL mode, no CGo

## Conventions

See `CONVENTIONS.md`. Key rules:
- Backwards compatibility required — existing data must remain readable
- Max 8 files per task (excluding tests), max 3 packages touched
- No new dependencies without human approval
- No schema changes unless task explicitly requires it
- Human approval needed for: new tables, Store interface changes, new CLI subcommands, JSON output format changes
- All new code must have tests; use `cmd.OutOrStdout()` for testability
