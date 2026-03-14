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
| `internal/mcpserver` | MCP server over stdio (6 tools: put, search, context, curate, reflect, edge) | ~260 LOC |
| `memory.go` | Public library API — re-exports from internal packages | 102 LOC |

## Data Model

### Memory

The core unit. Indexed by `(namespace, key)` with automatic versioning.

```
Memory {
  ID             string      // ULID
  NS             string      // agent namespace (e.g. "agent:pikamini")
  Key            string      // unique within namespace, descriptive
  Content        string      // the actual memory text
  Kind           string      // semantic | episodic | procedural
  Tier           string      // sensory | stm | ltm | dormant (lifecycle stage)
  Pinned         bool        // always loaded in context, exempt from decay
  Priority       string      // low | normal | high | critical
  Importance     float64     // 0.0–1.0 continuous score
  Version        int         // auto-incremented on update
  Supersedes     string      // ID of previous version
  Tags           []string    // first-class filtering (identity, lore, project:ghost, chat:123)
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

### Edge (DAG)

Weighted, typed associations between memories for graph-based retrieval (spreading activation). Edges are first-class objects with their own lifecycle metadata.

```
Edge {
  FromID         string     // source memory ID
  ToID           string     // target memory ID
  Rel            string     // relates_to | contradicts | depends_on | refines | contains | merged_into
  Weight         float64    // 0.0–1.0, strength of association
  AccessCount    int        // incremented on co-retrieval
  LastAccessedAt *time.Time // for future decay calculation
}
```

Default weights by relation type: contradicts=0.9, refines=0.8, depends_on=0.7, contains=0.6, relates_to=0.5, merged_into=0.0 (audit trail only).

**Auto-linking**: On `put`, Ghost computes embedding similarity against existing memories in the same namespace. Memories above the threshold (default 0.80, configurable via `GHOST_EDGE_THRESHOLD`) automatically get `relates_to` edges.

**Re-linking**: When a memory is versioned (new ID), all edges referencing the old ID are updated to point to the new ID.

### Link (legacy)

Semantic relationships between memories: `relates_to`, `contradicts`, `depends_on`, `refines`. Migrated to `memory_edges` table on startup.

## Storage Layer

### SQLite (pure Go, no CGo)

Uses `modernc.org/sqlite` with WAL mode. Single file at `~/.ghost/memory.db` (configurable via `--db` flag or `$GHOST_DB`).

### Schema

| Table | Purpose |
|-------|---------|
| `memories` | Core records with all metadata columns |
| `chunks` | Text segments with optional embedding vectors |
| `chunks_fts` | FTS5 virtual table for full-text search |
| `memory_edges` | Weighted directed edges for DAG retrieval |
| `memory_links` | Legacy directed edges (migrated to memory_edges) |
| `memory_files` | File path references |
| `reflect_rules` | Condition→action rules for the reflect engine |

### Migrations

Schema evolution uses `ALTER TABLE ADD COLUMN` with safe defaults — executed on every startup, idempotent. No migration framework; the `migrate()` method in `sqlite.go` handles everything.

FTS5 is kept in sync with chunks via `AFTER INSERT/DELETE/UPDATE` triggers.

## Namespace & Tag Conventions

### Namespaces

Namespaces represent **agent identity** — each namespace is one agent's isolated memory space.

| Namespace | Purpose |
|-----------|---------|
| `agent:<name>` | Per-agent memory space (e.g. `agent:pikamini`, `agent:coder`) |

Memories are isolated by namespace — no cross-namespace visibility.

### Tags (First-Class Filtering)

Tags replace the old namespace hierarchy for categorization. Use tags to classify what a memory is about:

| Tag | Purpose |
|-----|---------|
| `identity` | Core agent persona (name, personality, appearance) |
| `lore` | Background knowledge (relationships, fun facts) |
| `chat:<id>` | Per-conversation context |
| `project:<name>` | Project knowledge |
| `learning` | Accumulated insights |
| `convention` | Coding/writing rules |
| `capability` | What the agent can do |
| `user:<name>` | Per-user preferences |

### Design Rationale

- **Namespace = agent identity.** One DB can host multiple agents. Each is isolated.
- **Tags = flexible metadata.** Categories are tags, not namespace segments.
- **Pinned = chronic accessibility.** Replaces the old `identity` tier. Based on cognitive science — self-knowledge is chronically accessible LTM, not a separate memory store.
- **No enforced schema.** These are conventions, not constraints.

## Retrieval Pipeline

### Search

Three methods fused via **Reciprocal Rank Fusion** (RRF, k=60):

1. **FTS5** — Full-text search with stop-word filtering and priority/recency scoring
2. **LIKE fallback** — Per-term substring matching across content, keys, and chunks
3. **Vector embeddings** — Cosine similarity via all-MiniLM-L6-v2 (enabled by default, pure Go)

RRF merges ranked results: `score = Σ 1/(60 + rank_i)` across methods.

**Default tier exclusion**: `dormant` and `sensory` tiers are excluded from search results by default. Use `IncludeAll: true` to search all tiers.

**Temporal intent detection**: Queries containing time-related keywords (yesterday, recent, latest, etc.) trigger temporal-aware ranking — FTS weight drops from 0.5→0.2, recency weight rises from 0.3→0.7, and episodic-kind memories get a +0.3 boost in RRF fusion.

### Context Assembly

Three-phase greedy packing within a token budget:

1. **Phase 1 (Pinned)**: Load all `pinned = true` memories, ordered by importance. Fills up to `budget/3`.
2. **Phase 2 (Search)**: Query-relevant memories scored by composite metric, filterable by tags.
3. **Phase 3 (Edge Expansion)**: Implements spreading activation (Collins & Loftus, 1975). For each seed from Phase 2, follow top-K outgoing edges (sorted by weight). Neighbors enter the candidate pool with propagated scores: `propagated = seed_score × edge_weight × damping`. Memories that appear as both direct hits and edge neighbors get additive boost (capped at `MaxBoostFactor × direct_score`). **`contradicts` edges** bypass the normal cap — contradicting memories get a minimum score of 80% of the seed's score, ensuring conflicts are always surfaced. **Containment suppression**: before packing, children of `contains` parents in the candidate pool are suppressed to avoid redundancy. Final pool is re-sorted and greedy-packed into remaining budget.

**Edge expansion defaults** (`EdgeExpansionConfig`):

| Parameter | Default | Purpose |
|-----------|---------|---------|
| `Enabled` | `true` | Enable/disable edge expansion |
| `Damping` | `0.3` | Caps propagated score relative to seed |
| `MaxEdgesPerSeed` | `5` | Limits fanout per seed memory |
| `MinEdgeWeight` | `0.1` | Edges below this are not traversed |
| `MaxExpansionTotal` | `50` | Total cap on new candidates from edges |
| `MaxBoostFactor` | `0.5` | Cap on additive boost for direct+edge hits |

**Composite scoring** (4 additive factors + multiplicative tier modifier):

Additive weights vary by memory kind to match cognitive retrieval patterns:

| Factor | Semantic | Episodic | Procedural |
|--------|----------|----------|------------|
| Relevance | 0.45 | 0.30 | 0.35 |
| Recency | 0.10 | 0.40 | 0.05 |
| Importance | 0.30 | 0.15 | 0.15 |
| Access freq | 0.15 | 0.15 | 0.45 |

The composite score is then multiplied by a **tier modifier** so that tier transitions
have meaningful impact on ranking (not just a small additive boost):

Tier multipliers: ltm=1.0, stm=0.8, dormant=0.15, sensory=0.1

Memories that don't fully fit get excerpted (truncated with "...") if at least 25 tokens remain.

## Reflect System

Rule-based lifecycle management. No LLM calls — purely deterministic.

Each rule has a **condition** (AND-joined fields) and an **action**:

Pinned memories (`pinned = true`) are exempt from all lifecycle rules — they skip rule evaluation entirely.

| Built-in Rule | Condition | Action |
|---------------|-----------|--------|
| `sys-promote-sensory` | sensory, >1h old, >1 access | PROMOTE to STM |
| `sys-decay-sensory` | sensory, >4h old | DELETE |
| `sys-decay-unaccessed` | STM, >48h old, <10 accesses | DECAY importance ×0.95 (min 0.1) |
| `sys-promote-to-ltm` | STM, >24h old, >10 accesses | PROMOTE to LTM |
| `sys-demote-stale-ltm` | LTM, >168h since last access | DEMOTE to dormant |
| `sys-prune-low-utility` | >5 accesses, utility ratio <0.2 | DELETE |
| `sys-merge-similar` | STM, embedding similarity >0.9 | MERGE (keep highest importance) |

Rules are evaluated in two passes:
1. **Per-memory pass** — evaluated in priority order (first-match-wins). Sensory rules run at higher priority to quickly promote attended memories or discard unattended ones.
2. **Similarity merge pass** — rules with `cond_similarity_gt` run pairwise embedding comparison and consolidate similar memories. Survivor keeps highest importance, inherits union of tags and summed access/utility counts. Absorbed memories are soft-deleted with `merged_into` links.

Custom rules can be added via `ghost rule set`.

### Edge Lifecycle

Edges have their own lifecycle managed alongside memory nodes:

- **Co-retrieval strengthening** (Hebbian learning — "fire together, wire together"): When two connected memories appear together in a `ghost_context` response, their shared edge is strengthened: `weight += 0.05 × (1 - weight)` (diminishing returns approaching 1.0), `access_count++`, `last_accessed_at` updated.
- **Auto-linking on put**: When a memory is stored, Ghost computes embedding similarity against existing memories in the same namespace. Memories above the threshold (default 0.80, configurable via `GHOST_EDGE_THRESHOLD`) automatically get `relates_to` edges with weight = similarity score.
- **Edge re-linking**: When a memory is versioned (new ID supersedes old), all edges referencing the old ID are updated to point to the new ID within the same transaction.
- **Edge decay**: During reflect, edges not accessed in 30+ days with <3 accesses have their weight multiplied by 0.9.
- **Edge pruning**: Edges with weight < 0.05 are automatically deleted during reflect.

## MCP Server

Exposes 6 tools over stdio transport using `github.com/modelcontextprotocol/go-sdk`:
- `ghost_put` — Store/update a memory
- `ghost_search` — Full-text search with ranking
- `ghost_context` — Budget-aware context assembly with edge expansion (includes `compaction_suggested` signal)
- `ghost_curate` — Instance-level lifecycle actions on individual memories (promote, demote, boost, diminish, archive, delete, pin, unpin)
- `ghost_edge` — Create, remove, or list weighted edges between memories for DAG-based retrieval
- `ghost_reflect` — Run lifecycle rules across all memories (promote, decay, prune, merge similar, edge decay)

Started via `ghost mcp-serve` subcommand. See [LLM Integration Guide](llm-integration.md) for setup and usage patterns.

### Automated Memory via Hooks

Claude Code hooks can automate memory capture without relying on the agent remembering to call `ghost_put`. Both hooks use `type: "command"` shell scripts that pipe the session transcript to `claude -p` (headless mode) for analysis, then execute the resulting `ghost put` CLI commands.

- **PreCompact hook (async)** — fires before context compression on long sessions. Reads the last ~100 transcript lines, extracts learnings, stores them via `ghost` CLI. Zero latency cost.
- **Stop hook (async)** — fires after each agent turn. Same pattern but reads more transcript (last ~200 lines) as the final chance to capture learnings. Zero latency cost.

See the [Automated Memory via Claude Code Hooks](llm-integration.md#automated-memory-via-claude-code-hooks) section in the LLM Integration Guide.

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
| `edge` | Create, remove, or list weighted edges between memories |
| `consolidate` | Create a summary memory with `contains` edges to source memories |
| `link` | Create/remove semantic relationships (legacy) |
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
