# ghost

Persistent memory for AI agents. Text in, text out. SQLite-backed, single binary, no server.

**LLM-free by design.** Capture, retrieval, ranking, linking, and lifecycle all run on
local models and deterministic rules — no API key, no network, nothing to page out to a
provider. Retrieval is SOTA-competitive (LongMemEval_S **Recall@5 0.908 / MRR 0.827**, beating
published BM25/Contriever) using only local embeddings + an optional local cross-encoder.

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

# Capture memories from raw text/transcript with NO LLM (heuristic extraction)
cat session.md | ghost capture -n agent:mybot --source session:abc
# ...or feed LLM-produced candidates through the same dedup/store path
echo '[{"content":"...","key":"...","kind":"semantic","importance":0.7}]' | ghost capture -n agent:mybot --json

# Induce recurring workflows into procedural memories (one session per line)
printf 'edit build test commit\nbuild test commit\n' | ghost mine-procedures -n agent:mybot

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

- **LLM-free capture**: `ghost capture` turns raw text/transcripts into memories via deterministic
  heuristics (entity salience + intent classifiers) — no LLM needed. Also ingests LLM-produced
  candidates via `--json` (two-tier: mechanical always-on, LLM as an optional quality upgrade).
- **Procedural workflow induction**: `ghost mine-procedures` mines recurring usage sequences into
  procedural memories — the agent learns "how you work" by frequency, no LLM.
- **Three-phase context assembly**: pinned memories + search + edge expansion (spreading activation)
- **DAG-based retrieval**: weighted edges between memories, auto-linked on put; `reflect --relink`
  backfills a dense multi-signal graph (cosine OR shared entities OR topics) even without embeddings
- **Personalization-aware ranking**: freshness/supersede detection (default on), utility-into-ranking,
  and spaced-repetition ease so proven-useful memories resist idle decay
- **Cognitive memory model**: Tulving's taxonomy (semantic/episodic/procedural) with kind-specific scoring
- **Lifecycle management**: sensory → stm → ltm → dormant tiers with rule-based reflect system
- **Hierarchical summaries**: `ghost consolidate` creates summary parents that suppress children in context
- **Vector embeddings**: all-MiniLM-L6-v2 (pure Go, no CGo) fused with FTS5 via Reciprocal Rank Fusion,
  plus an optional local cross-encoder reranker
- **Storage compaction**: `ghost gc --purge-deleted` reclaims soft-deleted rows + orphaned embeddings
- **MCP server**: `ghost mcp-serve` exposes 11 tools for Claude Code and other MCP clients

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
| `patch` | Apply a unified diff to a memory's content (safer than `put` for edits) |
| `capture` | Extract + store memories from raw text/transcripts (deterministic, no LLM; `--json` ingests LLM candidates) |
| `mine-procedures` | Induce recurring workflows from usage sequences into procedural memories (no LLM) |
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
| `reflect` | Run lifecycle rules (promote, decay, prune edges); `--relink` backfills the multi-signal edge graph |
| `rule` | Manage reflect rules (set, get, list, delete) |
| `gc` | Garbage collect expired/stale memories; `--purge-deleted <age\|all> [--vacuum]` reclaims soft-deleted rows + orphaned chunks |

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
| `source` | Manage source-identity aliases (who a memory came from) |
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

Edges are auto-created on `put` when embedding similarity exceeds threshold (default 0.85, configurable via `GHOST_EDGE_THRESHOLD`). Edges strengthen through co-retrieval (Hebbian learning) and decay when unused.

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

Exposes 11 tools: `ghost_put`, `ghost_get`, `ghost_search`, `ghost_context`, `ghost_expand`, `ghost_consolidate`, `ghost_edge`, `ghost_edge_candidates`, `ghost_curate`, `ghost_reflect`, `ghost_patch`.

See [Claude Code Setup](docs/quickstart-claude-code.md) for full setup including hooks and CLAUDE.md instructions.

## Storage

Database location (in order of precedence):
1. `--db` flag
2. `$GHOST_DB` environment variable
3. `~/.ghost/memory.db`

Pure Go SQLite (`modernc.org/sqlite`), WAL mode, no CGo.

## Tuning (environment variables)

All defaults are sensible; these tune the personalization and retrieval behavior. None require an LLM.

| Variable | Default | Effect |
|----------|---------|--------|
| `GHOST_FRESHNESS` | `1` (on) | Supersede detection: a change announcement ("now use X instead of Y") that shares an entity with an existing memory demotes the old one and links them. Set `0` to disable. |
| `GHOST_UTILITY_WEIGHT` | `0` (off) | Blend proven usefulness (`utility_count/access_count`) into context ranking. |
| `GHOST_EDGE_THRESHOLD` | `0.85` | Cosine threshold for auto-linking edges on `put`. |
| `GHOST_RELINK_MAX` | `8` | Max edges per memory kept by `reflect --relink` (0 = uncapped). |
| `GHOST_PPR` | `0` (off) | Personalized-PageRank multi-hop context expansion (pure-Go) instead of single-hop — reaches 2–3 hops and rewards multi-path convergence. Tune with `GHOST_PPR_ALPHA` (restart, 0.5) / `GHOST_PPR_ITERS` (20). Measured on LongMemEval_S context-mode: regresses R@5 (0.502→0.399) — off for good reason; only worth trying on graphs rich in curated entity/topic edges. |
| `GHOST_BITEMPORAL` | `0` (off) | Bi-temporal validity: supersede detection also stamps `valid_to` on the replaced fact, default search hard-retires invalidated facts, and `AsOf` recall reconstructs past belief states. |
| `GHOST_MIN_SCORE` | `0` (off) | Context confidence floor for callers that don't set one — drops low-score candidates instead of returning confident-looking noise for absent topics. `0.3` measured regression-free; recommended for deployments. |
| `GHOST_MMR_LAMBDA` | `0` (off) | MMR diversity re-rank of context candidates (token-Jaccard, works FTS-only). Measured: regresses LongMemEval (relevant near-dup chunks get demoted); only worth trying on redundancy-heavy personal DBs. |
| `GHOST_ACTR` | `0` (off) | ACT-R base-level activation replaces the recency+frequency scoring terms (τ/s via `GHOST_ACTR_TAU`/`GHOST_ACTR_S`). Uses exact logged access history where available (see `GHOST_ACCESS_LOG`), the three-point approximation otherwise (measured below baseline — the log is what gives the theory a fair shot). |
| `GHOST_ACCESS_LOG` | `1` (on) | Per-access timestamp log (`memory_accesses` table): the raw retrieval history that `access_count`/`last_accessed_at` compress away. Feeds exact ACT-R activation and future utility feedback. Append-only, no read-path effect; pruned by `gc` (120d retention, 100 rows/memory cap). |
| `GHOST_PRF` | `0` (off) | RM3 pseudo-relevance feedback: expand the query with terms shared by top-3 hits and re-search. Measured: +2.3pt R@5 but −3.6pt MRR on LongMemEval_S, at ~10× query cost — a recall-leaning tradeoff, not a default. |
| `GHOST_SEARCH_MMR` | `0` (off) | Embedding-based MMR diversification of search results (requires embedder). Measured −42% MRR on LoCoMo — experimental only. |
| `GHOST_EMBED_PROVIDER` | `local` | Embedding backend: `local` (all-MiniLM, pure Go), `ollama`, `openai`, or `none`. |
| `GHOST_RERANKER` | `auto` | Cross-encoder reranker (ms-marco-MiniLM, pure Go). **`auto` (default): on when the model is already in `~/.ghost/models` — a fresh install never touches the network.** `local` forces it (downloads on first use); `none` disables. When active, Search runs **expand-then-rerank**: retrieval is the high-recall stage (pool `GHOST_RERANK_POOL`, default 20), the cross-encoder restores precision, results are cut to the requested k. Fast profile by default (`GHOST_RERANK_CHUNKS_PER_DOC=2`, 0.2–1s/query on personal-scale memories; raise for full-coverage MaxP on long documents). Adaptive skip on confident queries is on by default (`GHOST_RERANK_ADAPTIVE=0` disables). Temporal-intent queries skip the model (blind to recency); `GHOST_RERANK_TEMPORAL=1` overrides. Measured: personal meanMRR 0.790→0.861, MemoryAgentBench single-hop hit@5 0.91→0.99 on top of embeddings. |
| `GHOST_DB` | `~/.ghost/memory.db` | Database path. |

Lifecycle also applies **spaced-repetition ease** automatically during `reflect`: memories that repeatedly
prove useful (`utility_count`) become decay-resistant and survive idle stretches longer before demotion.

## Benchmarks

Retrieval is measured against published long-term-memory benchmarks — all LLM-free (local models only):

| Benchmark | Metric | Ghost |
|-----------|--------|-------|
| LongMemEval_S (470q) | Recall@5 / MRR | **0.908 / 0.827** (hybrid retrieval) · **0.873 / 0.878** (+ cross-encoder fast profile: trades recall@5 for MRR; preference-slice MRR 0.43→0.68) |
| LoCoMo (best config) | Recall@5 / MRR | 0.750 / 0.595 |
| HaluMem-Medium (retrieval, +rerank) | MRR gain | +64% |

A separate **personal-agent eval** (`internal/store/eval_personal*`) measures what public benchmarks don't —
preference / procedural / decision recall, same-day recall, freshness-suppression, contradiction-surfacing,
edge-based multi-hop, and abstention — with an A/B matrix over the personalization knobs and an embedded
(vector-path) variant. See [Eval Framework](docs/eval.md).

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
