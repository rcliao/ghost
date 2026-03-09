# Memory System Design

How ghost organizes, stores, retrieves, and manages agent memory.

---

## Core Data Model

A memory is the fundamental unit. Each has these attributes:

| Field | Type | Description | Example |
|-------|------|-------------|---------|
| **ns** | string | Hierarchical namespace (`:` separated) | `identity`, `lore`, `user:alice`, `shell:chat:12345` |
| **key** | string | Unique identifier within namespace | `name`, `timezone`, `deploy-2026-03-01` |
| **content** | string | The actual memory text | `"Atlas — a helpful AI assistant"` |
| **kind** | string | Knowledge type: `semantic`, `episodic`, `procedural` | auto-detected from tier |
| **tier** | string | Persistence level: `sensory`, `stm`, `ltm`, `identity`, `dormant` | `stm` (default) |
| **priority** | string | Retrieval urgency: `low`, `normal`, `high`, `critical` | `normal` (default) |
| **importance** | float64 | Continuous 0.0–1.0 score for ranking | `0.5` (default) |
| **tags** | []string | Freeform labels for filtering | `["deploy", "infra"]` |
| **version** | int | Auto-incremented on update to same (ns, key) | `1` |
| **est_tokens** | int | Rough token estimate (`len/4 + 20`) | `45` |
| **meta** | string | Freeform JSON metadata | `{"source": "heartbeat"}` |

### Lifecycle fields (managed automatically)

| Field | Description |
|-------|-------------|
| **access_count** | Incremented on every retrieval |
| **utility_count** | Incremented when memory was actually useful |
| **last_accessed_at** | Timestamp of last retrieval |
| **expires_at** | Optional TTL-based expiration |
| **supersedes** | ID of previous version |
| **deleted_at** | Soft-delete timestamp (null = active) |

### Example: A complete memory

```bash
ghost put -n "identity" -k "name" \
  --tier identity -p high --importance 0.9 \
  --tags "core,personality" \
  "Atlas — a helpful AI assistant built for developer workflows"
```

This creates:
```json
{
  "ns": "identity",
  "key": "name",
  "content": "Atlas — a helpful AI assistant built for developer workflows",
  "kind": "semantic",
  "tier": "identity",
  "priority": "high",
  "importance": 0.9,
  "tags": ["core", "personality"],
  "version": 1,
  "est_tokens": 35
}
```

---

## Namespaces

Namespaces organize _who_ or _what_ a memory belongs to. They use `:` as a separator and support wildcard queries (`shell:chat:*`).

### Well-Known Namespaces (Agent Profile)

These top-level namespaces define the agent's core identity. They are **app-agnostic** — any app sharing the same ghost DB inherits them.

| Namespace | Purpose | Example content |
|-----------|---------|-----------------|
| `identity` | Who the agent is — name, personality, appearance | `"Atlas — a helpful AI assistant"` |
| `lore` | Background knowledge, relationships, trivia | `"The team does weekly retros every Friday"` |
| `user:<name>` | Per-user preferences and context | `"Timezone: America/Los_Angeles"` |

### App-Scoped Namespaces

Apps prefix with their name to avoid collisions. Owned and managed by the app.

| Pattern | Purpose | Example |
|---------|---------|---------|
| `<app>:chat:<id>` | Per-conversation memories | `shell:chat:12345` |
| `<app>:heartbeat:<id>` | Periodic reflection learnings | `shell:heartbeat:12345` |
| `<app>:capabilities` | What the agent can do in this app | `shell:capabilities` |
| `<app>:conventions` | Coding/writing conventions | `coder:conventions` |
| `<app>:learnings` | Accumulated insights | `coder:learnings` |

### Design Rationale

- **Well-known namespaces are shared.** A Telegram bridge, Discord bot, or CLI tool all read the same `identity` and `lore`. The agent is one entity across surfaces.
- **App namespaces are isolated.** `shell:chat:*` belongs to shell; other apps won't collide.
- **No enforced schema.** These are conventions, not constraints. Apps can define any namespace.

---

## Core Concepts

Ghost models memory after cognitive science with three orthogonal dimensions:

| Dimension | What it describes | Values |
|-----------|------------------|--------|
| **Kind** | What type of knowledge | `semantic`, `episodic`, `procedural` |
| **Tier** | How persistent/important | `sensory`, `stm`, `ltm`, `identity`, `dormant` |
| **Priority** | Urgency for retrieval | `low`, `normal`, `high`, `critical` |

These dimensions are independent. A memory can be any combination (e.g., `kind=procedural, tier=ltm, priority=high`).

---

## Memory Kinds

Kinds classify _what_ a memory represents. Borrowed from cognitive science:

### `semantic`
Fact-based, context-independent knowledge. The "what" of memory. Default kind for `ltm` and `identity` tier memories.

```bash
ghost put -n "identity" -k "role" "Helpful AI assistant for developer workflows"
ghost put -n "coder:conventions" -k "auth" "JWT with refresh tokens, 15min access / 7d refresh"
```

### `episodic` (default for sensory/stm)
Event or experience-based memories with temporal context. The "when and what happened." Default kind for `sensory` and `stm` tier memories — new observations start as episodic and can be reclassified as they mature.

```bash
ghost put -n "shell:chat:12345" -k "deploy-2026-03-01" --kind episodic \
  "Deployed v2.3. Migration took 4min. Redis cache needed manual flush."
```

### `procedural`
How-to, process, or instruction-based knowledge. The "how."

```bash
ghost put -n "coder:learnings" -k "deploy-steps" --kind procedural \
  "1. Run migrations: make db-migrate
   2. Build: make build
   3. Deploy: make deploy-prod
   4. Verify: curl https://api.example.com/health"
```

### Organizing by namespace, not kind

Facts, events, and procedures can live in any namespace. Use **namespaces** for organizational structure and **kind** for the cognitive type:

```
identity            → core agent identity (always semantic)
lore                → background knowledge (mostly semantic)
user:<name>         → per-user context (semantic + episodic)
<app>:chat:<id>     → conversation memories (episodic)
<app>:learnings     → accumulated insights (semantic + procedural)
```

This keeps the kind taxonomy clean (3 values from cognitive science) while allowing flexible organization through the namespace hierarchy.

---

## Memory Tiers

Tiers describe _how persistent_ a memory is and control its lifecycle through the reflect system.

```
                    ┌──────────┐
                    │ identity │  Core knowledge. Never decayed.
                    └────┬─────┘
                         │ PIN
                    ┌────┴─────┐
        PROMOTE ──► │   ltm    │  Proven useful. Preserved.
                    └────┬─────┘
                         │ DEMOTE
                    ┌────┴─────┐
                    │   stm    │  Recent. Subject to decay.
                    └────┬─────┘
                         │ PROMOTE (from sensory)
                    ┌────┴─────┐
  new inputs ────►  │ sensory  │  Ultra-short-lived buffer.
                    └────┬─────┘
                         │ DELETE (unattended)
                    ┌────┴─────┐
                    │ dormant  │  Archived. Recoverable but inactive.
                    └──────────┘
```

### `sensory`
Ultra-short-lived buffer for raw context window observations (e.g., conversation exchanges). Sensory memories that receive attention (accessed >1 time within 1 hour) are promoted to STM. Unattended sensory memories are deleted after 4 hours. Default kind is `episodic`.

### `stm` (short-term memory) — default
Where most new memories start. Subject to importance decay and promotion rules. Default kind is `episodic`.

### `ltm` (long-term memory)
Memories that have proven their value through repeated access. Protected from routine decay. Promoted automatically when accessed 3+ times over 24+ hours.

### `identity`
Core system/agent memories. Exempt from all decay. Always included in context assembly (budget permitting). Use for foundational knowledge that should never be forgotten.

```bash
ghost put -n "identity" -k "role" --tier identity \
  "You are Atlas, a helpful AI assistant for developer workflows."
```

### `dormant`
Archived memories. Not included in search or context by default, but recoverable. Memories land here when they haven't been accessed in a long time.

---

## Priority Levels

Priority provides an urgency signal for retrieval ranking. Orthogonal to tier.

| Priority | Weight | Use case |
|----------|--------|----------|
| `low` | 0.25 | Background info, nice-to-have |
| `normal` | 0.50 | Standard knowledge (default) |
| `high` | 0.75 | Important, should appear in most contexts |
| `critical` | 1.00 | Must-have, safety-relevant |

```bash
ghost put -n "user:alice" -k "allergies" -p critical "Allergic to peanuts"
```

---

## Importance Score

A continuous 0.0–1.0 score that complements the discrete priority levels. Used as the primary signal in context assembly ranking.

- Set explicitly at write time via `--importance`
- Defaults to 0.5
- Decayed by reflect rules for unaccessed memories
- Higher importance = more likely to be included in context

---

## Storage Model

### Database
SQLite (pure Go, no CGo) with WAL mode. Single file at `~/.ghost/memory.db`.

### Tables

| Table | Purpose |
|-------|---------|
| `memories` | Core memory records with metadata |
| `chunks` | Text segments (~400 chars) for search indexing |
| `chunks_fts` | FTS5 virtual table for full-text search |
| `memory_links` | Semantic relationships between memories |
| `memory_files` | File path references |
| `reflect_rules` | Lifecycle rules for the reflect engine |

### Versioning
Storing to an existing `(namespace, key)` creates a new version. Old versions are preserved and linked via `supersedes`. Retrieve history with `ghost get --history`.

### Soft Delete
`ghost rm` sets `deleted_at` (recoverable). `ghost rm --hard` permanently removes.

### TTL
Memories can expire: `--ttl 7d`, `--ttl 24h`. Expired memories are filtered from all queries. Cleaned up by `ghost gc`.

---

## Retrieval

### Search Pipeline

Three methods, fused via Reciprocal Rank Fusion (RRF):

1. **FTS5** — Full-text search with stop-word filtering. Primary method.
2. **LIKE fallback** — Substring matching when FTS5 yields no results.
3. **Vector embeddings** — Cosine similarity using all-MiniLM-L6-v2 (enabled by default).

RRF combines ranks: `score = Σ 1/(60 + rank_i)` across methods.

### Vector Embeddings

Ghost generates 384-dimensional vector embeddings for each memory chunk using [all-MiniLM-L6-v2](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2), a sentence transformer optimized for semantic similarity. This enables semantic search — finding memories by meaning, not just keyword overlap.

**How it works:**
- On `ghost put`, each chunk gets an embedding vector stored alongside its text
- On `ghost search`, the query is also embedded and compared via cosine similarity
- Vector results are fused with FTS5 and LIKE results via RRF

**Providers** (configured via `GHOST_EMBED_PROVIDER`):

| Provider | Value | Notes |
|----------|-------|-------|
| Local (default) | `local` or unset | all-MiniLM-L6-v2 via hugot/GoMLX. Pure Go, no external service. Model (~86MB) auto-downloaded on first use to `~/.ghost/models/`. |
| Ollama | `ollama` | Uses a local Ollama instance. Set `GHOST_EMBED_MODEL` for model selection. |
| OpenAI | `openai` | OpenAI-compatible API. Requires `OPENAI_API_KEY`. |
| Disabled | `none` | No vector embeddings. Search uses FTS5 + LIKE only. |

**Backfilling existing data:**

Memories stored before embeddings were enabled have `NULL` embeddings. Run once to backfill:

```bash
ghost embed backfill
```

### Context Assembly

Two-phase greedy packing within a token budget:

1. **Phase 1 (Pinned):** Load memories from pinned tiers (`identity`, `ltm`) ordered by importance. Fills `PinBudget` (default: budget/3).
2. **Phase 2 (Search):** Query-relevant memories scored by composite metric. Fills remaining budget.

**Composite scoring** (kind-specific weights):

Weights vary by memory kind to match cognitive retrieval patterns:

| Factor | Semantic | Episodic | Procedural |
|--------|----------|----------|------------|
| Relevance | 0.40 | 0.25 | 0.30 |
| Recency | 0.05 | 0.30 | 0.05 |
| Importance | 0.25 | 0.15 | 0.15 |
| Access freq | 0.15 | 0.10 | 0.35 |
| Tier boost | 0.15 | 0.20 | 0.15 |

Episodic memories favor recency (recent events matter most). Procedural memories favor access frequency (well-practiced skills surface first). Semantic memories favor relevance (factual accuracy over timing).

```bash
ghost context "deploy the API to production" --budget 4000
```

---

## Memory Relationships

### Links
Connect two memories with a semantic relationship:

| Relationship | Meaning |
|-------------|---------|
| `relates_to` | General association |
| `contradicts` | Conflicting information |
| `depends_on` | Sequential dependency |
| `refines` | Improvement of prior memory |

```bash
ghost link --from-ns "coder:conventions" --from-key "auth-v2" \
           --to-ns "coder:conventions" --to-key "auth-v1" \
           --rel refines
```

### File References
Link memories to file paths with a relationship:

| Relationship | Meaning |
|-------------|---------|
| `modified` | Memory mentions edits to this file |
| `created` | Memory is about file creation |
| `deleted` | Memory notes file deletion |
| `read` | Memory references reading a file |

```bash
ghost put -n "coder:learnings" -k "refactor-auth" "Refactored auth middleware" \
  --files src/middleware/auth.go --file-rel modified
```

---

## Reflect System

Automated lifecycle management via condition→action rules. No LLM involved — purely rule-based.

### How It Works

```bash
ghost reflect              # Run all rules against all memories
ghost reflect --dry-run    # Preview what would change
```

### Built-in Rules

| Rule | Condition | Action |
|------|-----------|--------|
| `sys-promote-sensory` | sensory, >1h old, >1 access | PROMOTE to STM |
| `sys-decay-sensory` | sensory, >4h old | DELETE |
| `sys-decay-unaccessed` | STM, >72h old, <3 accesses | DECAY importance by 0.95 |
| `sys-promote-to-ltm` | STM, >24h old, >3 accesses | PROMOTE to LTM |
| `sys-demote-stale-ltm` | LTM, >7d unaccessed, <2 accesses | DEMOTE to dormant |
| `sys-prune-low-utility` | >5 accesses, utility ratio <0.2 | DELETE |

### Rule Actions

| Action | Effect |
|--------|--------|
| `DECAY` | Reduce importance by factor (e.g., `{"factor": 0.95, "min": 0.1}`) |
| `DELETE` | Soft-delete the memory |
| `PROMOTE` | Move to higher tier (e.g., `{"to_tier": "ltm"}`) |
| `DEMOTE` | Move to lower tier (e.g., `{"to_tier": "dormant"}`) |
| `ARCHIVE` | Move to dormant tier |
| `TTL_SET` | Set expiration (e.g., `{"ttl": "30d"}`) |
| `PIN` | Move to identity tier (permanent) |

### Custom Rules

```bash
ghost rule set \
  --name "Archive old session memories" \
  --cond-tier stm \
  --cond-age-gt 168 \
  --cond-kind episodic \
  --action ARCHIVE
```

---

## Utility Tracking

Tracks whether retrieved memories actually helped the agent succeed.

- `access_count` — incremented on every retrieval (Get, Search, Context)
- `utility_count` — incremented explicitly when a memory contributed to success
- **Utility ratio** = `utility_count / access_count`

Memories with high access but low utility (retrieved often, rarely helpful) are candidates for pruning by the reflect system.

```bash
ghost utility-inc <memory-id>
```

---

## Design Principles

1. **Text in, text out.** No embedded LLM calls. The library is a storage and retrieval layer. Intelligence lives in the calling agent.

2. **Namespace over kind.** Use the 3-kind taxonomy for _what_ type of knowledge it is. Use namespaces for _who_ or _what_ it belongs to. Well-known namespaces (`identity`, `lore`, `user:*`) are app-agnostic; app-scoped namespaces (`<app>:*`) are isolated by prefix.

3. **Explicit over implicit.** Importance, utility, and tier transitions are either set by the caller or by deterministic rules — never inferred silently.

4. **Budget-aware.** Context assembly respects token budgets. Every memory has an estimated token count. The agent gets the most valuable memories that fit.

5. **Backwards compatible.** Existing data always remains readable. Schema changes use `ALTER TABLE ADD COLUMN` with safe defaults.
