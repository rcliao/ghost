# ghost

Persistent memory for AI agents. Text in, text out. SQLite-backed, single binary, no server.

## Install

```bash
# Homebrew (macOS / Linux)
brew install rcliao/tap/ghost

# Or from source (requires Go)
go install github.com/rcliao/ghost/cmd/ghost@latest
```

Pre-built binaries for all platforms are available on [GitHub Releases](https://github.com/rcliao/ghost/releases).

## Quick Start

```bash
# Store a memory
ghost put -n agent:mybot -k "auth-design" "JWT with RSA256, 24h token expiry"

# Store with importance and tags
ghost put -n agent:mybot -k "deploy-gotcha" --importance 0.8 \
  --tags "learning,project:api" "Redis cache needs manual flush after deploy"

# Store with TTL (auto-expires)
ghost put -n agent:mybot -k "session-token" --ttl 24h "abc123"

# Pipe content from stdin
cat session-notes.md | ghost put -n agent:mybot -k "session-2026-03-13" --kind episodic

# Retrieve
ghost get -n agent:mybot -k "auth-design"

# Search
ghost search "JWT authentication" -n agent:mybot

# Assemble context within a token budget
ghost context "how does auth work" -n agent:mybot --budget 2000

# Run lifecycle maintenance (promote, decay, link similar, prune)
ghost reflect --dry-run
ghost reflect

# Discover clusters of related memories
ghost clusters -n agent:mybot

# Consolidate a cluster into a summary
ghost consolidate -n agent:mybot --summary-key auth-overview \
  --keys "auth-jwt,auth-expiry,auth-cookies" \
  --content "Auth: JWT+RSA256, 24h expiry, refresh via httpOnly cookies"

# Curate individual memories
ghost curate -n agent:mybot -k "auth-design" --op promote   # stm → ltm
ghost curate -n agent:mybot -k "auth-design" --op pin       # always in context
ghost curate -n agent:mybot -k "old-pattern" --op archive   # move to dormant

# Manage edges between memories
ghost edge -n agent:mybot --from-key auth-jwt --to-key auth-overview -r depends_on
ghost edge -n agent:mybot --from-key auth-jwt --list

# Manage reflect rules
ghost rule list
ghost rule set --name "fast-promote" --cond-tier stm --cond-age-gt 12 \
  --cond-access-gt 5 --action PROMOTE
```

## Key Features

- **Three-phase context assembly**: pinned memories + search + edge expansion (spreading activation)
- **DAG-based retrieval**: weighted edges between memories, auto-linked on put via embedding similarity
- **Cognitive memory model**: Tulving's taxonomy (semantic/episodic/procedural) with kind-specific scoring
- **Lifecycle management**: sensory → stm → ltm → dormant tiers with rule-based reflect system
- **Hierarchical summaries**: `ghost consolidate` creates summary parents that suppress children in context
- **Vector embeddings**: all-MiniLM-L6-v2 (pure Go, no CGo) fused with FTS5 via Reciprocal Rank Fusion
- **MCP server**: `ghost mcp-serve` exposes 9 tools for Claude Code and other MCP clients

## Namespace Conventions

Namespaces represent agent identity. Each namespace is one agent's isolated memory space.

| Namespace | Purpose |
|-----------|---------|
| `agent:<name>` | Per-agent memory space (e.g. `agent:claude-code`, `agent:mybot`) |

Tags provide categorization within a namespace: `identity`, `lore`, `project:<name>`, `chat:<id>`, `learning`, `convention`, `user:<name>`.

## Commands

### Core

| Command | Description |
|---------|-------------|
| `put` | Store or update a memory (auto-links similar via edges) |
| `get` | Retrieve by namespace + key |
| `list` | List memories (filterable by ns, kind, tags) |
| `rm` | Soft-delete a memory (or hard-delete with `--hard`) |
| `search` | Full-text + semantic search |
| `context` | Assemble relevant memories within token budget |

### Edges & DAG

| Command | Description |
|---------|-------------|
| `edge` | Create, remove, or list weighted edges between memories |
| `clusters` | Discover groups of similar memories connected by edges |
| `consolidate` | Create a summary memory with `contains` edges to sources |

### Lifecycle

| Command | Description |
|---------|-------------|
| `curate` | Single-memory lifecycle actions (promote, demote, boost, diminish, archive, delete, pin, unpin) |
| `reflect` | Run lifecycle rules (promote, decay, link similar, prune edges) |
| `rule` | Manage reflect rules (set, get, list, delete) |
| `gc` | Garbage collect expired/stale memories |

### Inspection

| Command | Description |
|---------|-------------|
| `peek` | Lightweight index of memory state |
| `history` | Full version history of a key |
| `stats` | Database statistics |

### Organization

| Command | Description |
|---------|-------------|
| `tags` | List, rename, or remove tags |
| `ns` | Namespace operations (list, rm) |
| `files` | Find memories linked to a file path |
| `embed` | Manage vector embeddings (backfill, stats) |
| `link` | Create/remove relationships (legacy — use `edge` instead) |

### Data

| Command | Description |
|---------|-------------|
| `export` / `import` | JSON export/import |
| `ingest` | Parse markdown files into memories |
| `mcp-serve` | Start MCP server on stdio |

## Edge System (DAG-Based Retrieval)

Memories are connected by weighted, typed edges for associative retrieval:

| Edge Type | Default Weight | Behavior |
|-----------|---------------|----------|
| `relates_to` | 0.5 | General association |
| `contradicts` | 0.9 | Force-included in context (80% of seed score) |
| `depends_on` | 0.7 | Pull in dependency |
| `refines` | 0.8 | Newer version of another memory |
| `contains` | 0.6 | Parent summary → child detail (children suppressed) |
| `merged_into` | 0.0 | Audit trail only |

Edges are auto-created on `put` when embedding similarity exceeds threshold (default 0.80, configurable via `GHOST_EDGE_THRESHOLD`). Edges strengthen through co-retrieval (Hebbian learning) and decay when unused.

```bash
# Manual edge creation
ghost edge -n agent:mybot --from-key auth-jwt --to-key auth-overview -r depends_on

# List edges for a memory
ghost edge -n agent:mybot --from-key auth-jwt --list

# Discover similar memory clusters
ghost clusters -n agent:mybot

# Consolidate a cluster into a summary
ghost consolidate -n agent:mybot --summary-key auth-overview \
  --keys "auth-jwt,auth-expiry,auth-cookies" \
  --content "Auth: JWT+RSA256, 24h expiry, refresh via httpOnly cookies"
```

## MCP Server (Claude Code Integration)

```bash
# Add as user-scoped MCP server
claude mcp add --scope user --transport stdio ghost -- ghost mcp-serve
```

Exposes 9 tools: `ghost_put`, `ghost_get`, `ghost_search`, `ghost_context`, `ghost_expand`, `ghost_consolidate`, `ghost_edge`, `ghost_curate`, `ghost_reflect`.

See [Claude Code Setup](docs/quickstart-claude-code.md) for full setup including hooks and CLAUDE.md instructions.

## Storage

Database location (in order of precedence):
1. `--db` flag
2. `$GHOST_DB` environment variable
3. `~/.ghost/memory.db`

Pure Go SQLite (`modernc.org/sqlite`), WAL mode, no CGo.

## Documentation

| Doc | Content |
|-----|---------|
| [Claude Code Setup](docs/quickstart-claude-code.md) | MCP server, hooks, CLAUDE.md instructions, `/ghost-learn` skill |
| [Integration Guide](docs/integration-guide.md) | Go library, CLI, Python — for custom agents and bots |
| [Architecture](docs/ARCHITECTURE.md) | System design, data model, retrieval pipeline |
| [Cognitive Inspirations](docs/cognitive-inspirations.md) | Science behind the design |
| [Eval Framework](docs/eval.md) | Retrieval benchmarks |
| [Conventions](CONVENTIONS.md) | Contribution rules |
| [Research](docs/research/) | LCM comparison, memory edges design |

## Dependencies

- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — Pure Go SQLite (no CGo)
- [github.com/oklog/ulid/v2](https://github.com/oklog/ulid) — ULID generation
- [github.com/spf13/cobra](https://github.com/spf13/cobra) — CLI framework
- [github.com/modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) — MCP server protocol
- [github.com/knights-analytics/hugot](https://github.com/knights-analytics/hugot) — Local sentence embeddings (all-MiniLM-L6-v2)

## License

MIT
