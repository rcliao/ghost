# Architecture

Ghost is a persistent memory system for AI agents. Single binary, SQLite-backed, zero server dependencies.

## High-Level Overview

```
┌─────────────────────────────────────────────────┐
│                  Consumers                       │
│  CLI (ghost put/get/search)  │  MCP Server       │
│  Library (memory.go)         │  (stdio transport) │
└──────────────┬───────────────┴──────┬────────────┘
               │                      │
       ┌───────▼───────┐     ┌────────▼────────┐
       │  internal/cli  │     │ internal/mcpserver│
       │  (Cobra cmds)  │     │ (MCP tools)      │
       └───────┬────────┘     └────────┬─────────┘
               │                       │
               └───────────┬───────────┘
                           │
                  ┌────────▼────────┐
                  │  internal/store  │
                  │  Store interface │
                  │  SQLiteStore     │
                  └──┬───────┬───┬──┘
                     │       │   │
          ┌──────────▼┐  ┌──▼──┐ ┌▼──────────────┐
          │  chunker   │  │model│ │  embedding     │
          │ (text→     │  │     │ │ (Ollama/OpenAI)│
          │  chunks)   │  │     │ │                │
          └────────────┘  └─────┘ └────────────────┘
```

## Package Map

| Package | Purpose | Size |
|---------|---------|------|
| `cmd/ghost` | Entrypoint — delegates to `cli.RootCmd.Execute()` | 13 LOC |
| `internal/cli` | Cobra commands for all CLI subcommands | ~1900 src / ~2400 test |
| `internal/store` | `Store` interface + `SQLiteStore` implementation | ~4400 src / ~3700 test |
| `internal/model` | Core data types: `Memory`, `Chunk`, `FileRef` | 68 LOC |
| `internal/chunker` | Markdown-aware text splitting (~400 char targets) | 193 LOC |
| `internal/embedding` | Pluggable vector embeddings (local/Ollama/OpenAI) | ~320 LOC |
| `internal/ingest` | Markdown file parser (H2 → sections → memories) | 154 LOC |
| `internal/mcpserver` | MCP server over stdio (5 tools: put, search, context, curate, reflect) | ~260 LOC |
| `memory.go` | Public library API — re-exports from internal packages | 102 LOC |

## Data Model

### Memory

The core unit. Indexed by `(namespace, key)` with automatic versioning.

```
Memory {
  ID             string      // ULID
  NS             string      // hierarchical namespace (e.g. "project:ghost")
  Key            string      // unique within namespace
  Content        string      // the actual memory text
  Kind           string      // semantic | episodic | procedural
  Tier           string      // sensory | stm | ltm | identity | dormant
  Priority       string      // low | normal | high | critical
  Importance     float64     // 0.0–1.0 continuous score
  Version        int         // auto-incremented on update
  Supersedes     string      // ID of previous version
  Tags           []string
  AccessCount    int         // incremented on every retrieval
  UtilityCount   int         // incremented when memory was actually useful
  EstTokens      int         // rough token estimate (len/4 + 20)
  TTL/ExpiresAt              // optional expiration
}
```

### Chunk

Text segments (~400 chars) for search indexing. Each memory is split into chunks on store.

### FileRef

Links a memory to a file path with a relationship type (modified, created, deleted, read).

### Link

Semantic relationships between memories: `relates_to`, `contradicts`, `depends_on`, `refines`.

## Storage Layer

### SQLite (pure Go, no CGo)

Uses `modernc.org/sqlite` with WAL mode. Single file at `~/.ghost/memory.db` (configurable via `--db` flag or `$GHOST_DB`).

### Schema

| Table | Purpose |
|-------|---------|
| `memories` | Core records with all metadata columns |
| `chunks` | Text segments with optional embedding vectors |
| `chunks_fts` | FTS5 virtual table for full-text search |
| `memory_links` | Directed edges between memories |
| `memory_files` | File path references |
| `reflect_rules` | Condition→action rules for the reflect engine |

### Migrations

Schema evolution uses `ALTER TABLE ADD COLUMN` with safe defaults — executed on every startup, idempotent. No migration framework; the `migrate()` method in `sqlite.go` handles everything.

FTS5 is kept in sync with chunks via `AFTER INSERT/DELETE/UPDATE` triggers.

## Namespace Conventions

Namespaces are hierarchical strings using `:` as separator. Ghost doesn't enforce naming, but recommends these conventions for interoperability across apps.

### Well-Known Namespaces (Agent Profile)

These top-level namespaces define the agent's core identity and are typically always injected into the system prompt. They are app-agnostic — any app using the same ghost DB shares them.

| Namespace | Purpose | Example |
|-----------|---------|---------|
| `identity` | Who the agent is — name, personality, pronouns, appearance | `"Atlas — a helpful AI assistant"` |
| `lore` | Background knowledge, family facts, relationships, fun trivia | `"The team does weekly retros every Friday"` |
| `user:<name>` | Per-user preferences and context | `user:alice`, `user:bob` |

### App-Scoped Namespaces

Apps prefix with their name to avoid collisions. These are managed by the owning app.

| Pattern | Purpose | Example |
|---------|---------|---------|
| `<app>:chat:<id>` | Per-conversation memories | `shell:chat:12345` |
| `<app>:heartbeat:<id>` | Periodic reflection learnings | `shell:heartbeat:12345` |
| `<app>:capabilities` | What the agent can do in this app | `shell:capabilities` |
| `<app>:conventions` | Coding/writing conventions | `coder:conventions` |
| `<app>:learnings` | Accumulated insights | `coder:learnings` |

### Design Rationale

- **Well-known namespaces are app-agnostic.** A Telegram bridge, Discord bot, or CLI tool all share the same `identity` and `lore` — the agent is the same entity across surfaces.
- **App namespaces are isolated.** `shell:chat:*` belongs to shell; a different app won't write there.
- **Wildcard queries** (`shell:chat:*`) let apps query all their namespaces without listing each one.
- **No enforced schema.** These are conventions, not constraints. Apps can define any namespace they need.

## Retrieval Pipeline

### Search

Three methods fused via **Reciprocal Rank Fusion** (RRF, k=60):

1. **FTS5** — Full-text search with stop-word filtering and priority/recency scoring
2. **LIKE fallback** — Per-term substring matching across content, keys, and chunks
3. **Vector embeddings** — Cosine similarity via all-MiniLM-L6-v2 (enabled by default, pure Go)

RRF merges ranked results: `score = Σ 1/(60 + rank_i)` across methods.

### Context Assembly

Two-phase greedy packing within a token budget:

1. **Phase 1 (Pinned)**: Load `identity` + `ltm` tier memories, ordered by importance. Fills up to `budget/3`.
2. **Phase 2 (Search)**: Query-relevant memories scored by composite metric. Fills remaining budget.

**Composite scoring** (5 factors, kind-specific weights):

Weights vary by memory kind to match cognitive retrieval patterns:

| Factor | Semantic | Episodic | Procedural |
|--------|----------|----------|------------|
| Relevance | 0.40 | 0.25 | 0.30 |
| Recency | 0.05 | 0.30 | 0.05 |
| Importance | 0.25 | 0.15 | 0.15 |
| Access freq | 0.15 | 0.10 | 0.35 |
| Tier boost | 0.15 | 0.20 | 0.15 |

Tier boost values: identity=1.0, ltm=0.75, stm=0.25, sensory=0.05, dormant=0.1

Memories that don't fully fit get excerpted (truncated with "...") if at least 25 tokens remain.

## Reflect System

Rule-based lifecycle management. No LLM calls — purely deterministic.

Each rule has a **condition** (AND-joined fields) and an **action**:

| Built-in Rule | Condition | Action |
|---------------|-----------|--------|
| `sys-pin-identity` | tier=identity | PIN (blocks other rules) |
| `sys-promote-sensory` | sensory, >1h old, >1 access | PROMOTE to STM |
| `sys-decay-sensory` | sensory, >4h old | DELETE |
| `sys-decay-unaccessed` | STM, >72h old, <3 accesses | DECAY importance ×0.95 (min 0.1) |
| `sys-promote-to-ltm` | STM, >24h old, >3 accesses | PROMOTE to LTM |
| `sys-demote-stale-ltm` | LTM, >7d unaccessed, <2 accesses | DEMOTE to dormant |
| `sys-prune-low-utility` | >5 accesses, utility ratio <0.2 | DELETE |

Rules are evaluated in priority order (first-match-wins). Sensory rules run at higher priority to quickly promote attended memories or discard unattended ones. Custom rules can be added via `ghost rule set`.

## MCP Server

Exposes 5 tools over stdio transport using `github.com/modelcontextprotocol/go-sdk`:
- `ghost_put` — Store/update a memory
- `ghost_search` — Full-text search with ranking
- `ghost_context` — Budget-aware context assembly (includes `compaction_suggested` signal)
- `ghost_curate` — Instance-level lifecycle actions on individual memories (promote, demote, boost, diminish, archive, delete)
- `ghost_reflect` — Run lifecycle rules across all memories (promote, decay, prune)

Started via `ghost mcp-serve` subcommand. See [LLM Integration Guide](llm-integration.md) for setup and usage patterns.

## Library API

`memory.go` at the package root provides a public Go API by re-exporting internal types:

```go
import memory "github.com/rcliao/ghost"

store, _ := memory.NewSQLiteStore("path/to/db")
store.Put(ctx, memory.PutParams{NS: "project:x", Key: "fact", Content: "..."})
results, _ := store.Search(ctx, memory.SearchParams{Query: "something"})
```

The public `Store` interface is a subset of the internal one — core CRUD, search, context, reflect, and GC.

## CLI Commands

| Command | Description |
|---------|-------------|
| `put` | Store or update a memory |
| `get` | Retrieve by namespace + key |
| `list` | List memories (filterable by ns, kind, tags) |
| `rm` | Soft-delete (or hard-delete) |
| `search` | Full-text search |
| `context` | Assemble relevant memories within token budget |
| `peek` | Lightweight index of memory state |
| `history` | Full version history of a key |
| `link` | Create/remove semantic relationships |
| `files` | Manage file references |
| `tags` | List, rename, or remove tags |
| `ns` | Namespace operations (list, rm) |
| `reflect` | Run lifecycle rules |
| `gc` | Garbage collect expired/stale memories |
| `stats` | Database statistics |
| `export` / `import` | JSON export/import |
| `ingest` | Parse markdown files into memories |
| `mcp` | Start MCP server on stdio |

## Dependencies

| Dependency | Purpose |
|------------|---------|
| `modernc.org/sqlite` | Pure Go SQLite (no CGo) |
| `github.com/spf13/cobra` | CLI framework |
| `github.com/oklog/ulid/v2` | Time-sortable unique IDs |
| `github.com/modelcontextprotocol/go-sdk` | MCP server protocol |
| `github.com/knights-analytics/hugot` | Local sentence embeddings (all-MiniLM-L6-v2 via GoMLX, pure Go) |

## Key Design Decisions

1. **No LLM calls inside the library.** Ghost is a storage and retrieval layer. Intelligence lives in the calling agent.

2. **Namespace over kind.** The 3-kind taxonomy classifies _what type_ of knowledge. Namespaces organize _who_ or _what project_ it belongs to.

3. **Explicit over implicit.** Importance, utility, and tier transitions are set by the caller or by deterministic rules — never silently inferred.

4. **Budget-aware retrieval.** Context assembly respects token budgets with greedy packing and excerpting.

5. **Backwards compatible storage.** Schema changes use `ALTER TABLE ADD COLUMN` with safe defaults. Existing data always remains readable.

## Embedding Providers

Ghost supports pluggable embedding providers via `GHOST_EMBED_PROVIDER`:

| Provider | Value | Description |
|----------|-------|-------------|
| **Local** (default) | `local` or unset | all-MiniLM-L6-v2 via hugot/GoMLX. Pure Go, no CGo. Model (~86MB) downloaded on first use to `~/.ghost/models/`. ~80ms per embedding. |
| Ollama | `ollama` | Local Ollama instance. Configurable model via `GHOST_EMBED_MODEL`. |
| OpenAI | `openai` | OpenAI-compatible API. Requires `OPENAI_API_KEY`. |
| Disabled | `none` | No vector embeddings. Search uses FTS5 + LIKE only. |

## DB Path Resolution

Priority order:
1. `--db` flag
2. `$GHOST_DB` env var
3. `$AGENT_MEMORY_DB` (legacy fallback)
4. `~/.ghost/memory.db`
