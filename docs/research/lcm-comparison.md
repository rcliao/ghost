# LCM (Lossless Context Management) vs Ghost

**Paper:** Ehrlich & Blackman, "LCM: Lossless Context Management," Voltropy PBC, February 2026.
**Reviewed:** 2026-03-13

## Summary of LCM

LCM is a deterministic architecture for intra-session context management that outperforms Claude Code on long-context tasks (OOLONG benchmark). It decomposes the RLM (Recursive Language Models) paradigm into two engine-managed mechanisms: **recursive context compression** and **recursive task partitioning**.

### Core Architecture

**Dual-state memory:**
- **Immutable Store** — every user message, assistant response, and tool result persisted verbatim. Never modified. Source of truth.
- **Active Context** — what's actually sent to the LLM. Mix of recent messages and precomputed **summary nodes** (compressed representations of older messages).

**Hierarchical DAG** — summaries are materialized views over the immutable history. Leaf summaries compress a span of messages; condensed summaries compress other summaries. Every summary retains stable IDs pointing back to its source messages. Any summary can be expanded to recover the originals via `lcm_expand`.

### Key Mechanisms

**Context Control Loop** (soft/hard thresholds):
- Below τ_soft: zero overhead, store is a passive logger
- Between τ_soft and τ_hard: async compaction (no user-facing latency)
- Above τ_hard: blocking compaction (forced before next turn)

**Three-Level Summarization Escalation** (guaranteed convergence):
1. Normal: LLM summarize with "preserve_details"
2. Aggressive: LLM summarize with "bullet_points" at half target tokens
3. Deterministic truncation to 512 tokens (no LLM, always converges)

**Large File Handling:**
- Files above a token threshold (~25k) stored externally on the filesystem
- Replaced in active context with an opaque ID + **Exploration Summary**
- Exploration summaries are type-aware: schema extraction for JSON/CSV/SQL, function signatures for code, LLM summary for unstructured text
- File IDs propagate through the DAG so file awareness survives compaction

**Operator-Level Recursion:**
- `llm_map`: engine-managed parallel map over JSONL datasets with schema validation and retry
- `agentic_map`: spawns full sub-agent sessions per item (tools, I/O, code execution)
- Database-backed execution with pessimistic locking, exactly-once semantics
- Context isolation via file-based I/O (model never sees raw dataset)

**Scope-Reduction Invariant** (guards against infinite delegation):
- Sub-agents must declare `delegated_scope` and `retained_work`
- Engine rejects delegation if caller would delegate 100% of its responsibility
- Ensures recursion is well-founded without imposing a fixed depth limit

### Tools Exposed to the Model

| Category | Tool | Purpose |
|----------|------|---------|
| Memory-access | `lcm_grep` | Regex search across full immutable history |
| Memory-access | `lcm_describe` | Metadata for any LCM identifier (file or summary) |
| Memory-access | `lcm_expand` | Expand summary to original messages (sub-agents only) |
| Operator | `llm_map` | Stateless parallel map with schema validation |
| Operator | `agentic_map` | Full sub-agent per item |
| Delegation | Sub-agent spawning | With scope-reduction invariant |

### Benchmark Results (OOLONG)

Volt (LCM-augmented agent) vs Claude Code, both using Opus 4.6:
- Avg absolute score: Volt 74.8 vs Claude Code 70.3 (+4.5 points)
- Improvement over raw Opus 4.6: Volt +29.2 vs Claude Code +24.7
- Gap widens at larger contexts: at 512K, Volt leads by 12.6 points
- LCM's architecture partially insulates against data contamination effects

---

## Different Problem Domains

LCM and Ghost solve different halves of the memory problem. They are complementary, not competing.

| | LCM | Ghost |
|--|-----|-------|
| **Scope** | Intra-session context management | Cross-session knowledge persistence |
| **Source of truth** | Immutable message history (full transcript) | Extracted knowledge (memories) |
| **Core structure** | Hierarchical DAG of summaries with lossless pointers | Flat memory store with lifecycle, search, and scoring |
| **LLM dependency** | Summarization requires LLM (with deterministic fallback) | No LLM calls — purely deterministic |
| **Activation** | Automatic as context fills up | Explicit by agent, hooks, or periodic reflect |
| **Persistence** | Session-scoped (dies with the session) | Indefinite with lifecycle management |

---

## Where Ghost Does Better

### Cross-Session Persistence

LCM's immutable store is session-scoped. When the session ends, the knowledge is gone (unless the agent explicitly saved it somewhere). Ghost persists knowledge indefinitely with a full lifecycle: sensory → stm → ltm → dormant, with promotion, decay, and pruning rules.

### Cognitive Memory Model

Ghost uses Tulving's taxonomy with kind-specific retrieval weights:

| Factor | Semantic | Episodic | Procedural |
|--------|----------|----------|------------|
| Relevance | 0.45 | 0.30 | 0.35 |
| Recency | 0.10 | 0.40 | 0.05 |
| Importance | 0.30 | 0.15 | 0.15 |
| Access freq | 0.15 | 0.15 | 0.45 |

LCM has no memory typing — everything is "messages" and "summaries."

### Lifecycle Engine

Ghost's reflect system provides deterministic lifecycle management: promotion based on access patterns, decay of unused memories, similarity-based merge, pruning of low-utility memories. Seven built-in rules plus custom rule support.

LCM has no lifecycle beyond summarization. Old summaries accumulate without curation.

### Semantic Search

Ghost fuses three retrieval methods via Reciprocal Rank Fusion (RRF, k=60):
1. FTS5 full-text search
2. LIKE substring fallback
3. Vector embeddings (all-MiniLM-L6-v2, cosine similarity)

Plus temporal intent detection and tier-aware scoring.

LCM relies on `lcm_grep` (regex over immutable store) and hierarchical DAG traversal. No vector search, no semantic ranking.

### Budget-Aware Context Assembly with Scoring

Ghost's two-phase assembly:
1. Phase 1: pinned memories (always-on, up to budget/3)
2. Phase 2: search results scored by composite metric with kind-specific weights and multiplicative tier modifiers

LCM packs context greedily from the DAG without relevance scoring — the "most recent" messages and their summaries fill the window.

### Pinned Memories

Ghost supports pinned memories that are always loaded in context and exempt from all lifecycle rules. Core identity, critical preferences, and must-know facts can be guaranteed present.

LCM has no equivalent — everything is subject to compaction.

### Rich Metadata

Ghost memories carry: tags, namespaces, importance scores, access/utility counts, priority levels, kind, tier, TTL, file references, and semantic links. This metadata drives retrieval scoring and lifecycle rules.

LCM metadata is minimal: role, timestamp, token count.

---

## Where LCM Does Better

### Lossless Retrievability

LCM's defining feature. Every original message is preserved verbatim. Summaries are derived views with stable IDs pointing back. `lcm_expand` recovers the original content at any time.

Ghost's hook-based capture is lossy: the LLM extracts learnings from the transcript, but the original conversational context (who said what, in response to what, what was tried and failed) is gone. The extracted memory is an interpretation, not a faithful record.

### Hierarchical Summarization DAG

Multi-resolution summaries that can summarize other summaries. Drill from a high-level overview → leaf summary → original messages. This gives the agent a "zoom" capability over session history.

Ghost has flat memories with `relates_to`/`refines`/`merged_into` links but no hierarchical summarization structure. When 50 memories exist about the same project, there's no way to get a single overview without reading all of them.

### Guaranteed Convergence

Three-level escalation ensures compaction always reduces token count. The deterministic truncation fallback (level 3, no LLM) is the key guarantee.

Ghost's context assembly respects budgets via greedy packing with excerpting, but the reflect system has no formal convergence guarantee — a misconfigured rule set could theoretically loop without making progress.

### Zero-Cost Continuity

Below the soft threshold, LCM adds zero overhead — the store is a passive logger and the user experiences raw model latency. Only when context pressure builds does the system activate.

Ghost always requires explicit tool calls or hook scripts to participate. Even the `SessionStart` hook adds latency on every session start.

### Type-Aware File Summaries

LCM generates Exploration Summaries when large files enter context:
- JSON/CSV/SQL → schema and shape extraction
- Code → function signatures, class hierarchies
- Unstructured text → LLM-generated summary

Ghost's `FileRef` is a metadata pointer (path + relationship type) with no content awareness. It tells you a file was touched, not what's in it.

### Dual-Threshold Compaction

Adaptive pressure management: soft threshold triggers async compaction (zero latency), hard threshold forces synchronous compaction. The system self-regulates based on context usage.

Ghost's reflect is triggered manually, via hooks at session boundaries, or periodically via `/loop`. There's no memory-pressure-based triggering — the system doesn't know when it's "full."

### Engine-Managed Parallel Processing

`llm_map`/`agentic_map` let the model process unbounded datasets via a single tool call. The engine handles iteration, concurrency (16 workers), schema validation, retry, and context isolation. The model never sees the raw dataset.

Ghost has no batch processing primitive.

---

## Ideas for Ghost

### High Priority

**1. Hierarchical Memory Summaries**

When memories accumulate on a topic (e.g., 20+ memories tagged `project:ghost`), auto-generate a summary memory that links to the originals via `refines` links. The summary would be a condensed overview; the originals remain searchable.

This could be a new reflect rule action (`SUMMARIZE`) or a dedicated `ghost consolidate` command. The summary becomes the primary context candidate, with originals available for drill-down.

*Inspired by:* LCM's hierarchical DAG
*Impact:* High — reduces context budget pressure, improves signal-to-noise in `ghost_context`

**2. Pressure-Based Reflect Triggering**

Add soft/hard thresholds to ghost that auto-trigger reflect:
- Soft threshold (e.g., namespace exceeds 500 memories or 100k tokens): log a warning, set `compaction_suggested: true` in context responses
- Hard threshold (e.g., 1000 memories or 200k tokens): auto-run reflect before accepting new `ghost_put`

This mirrors LCM's dual-threshold approach but applied to the persistent store rather than the active context.

*Inspired by:* LCM's soft/hard threshold compaction
*Impact:* High — prevents unbounded growth, makes reflect automatic

### Medium Priority

**3. Exploration Summaries for FileRef**

When storing a file reference, optionally generate a type-aware summary:
- Go/Python/JS files → exported function/type signatures
- JSON/YAML → schema shape
- Markdown → heading outline
- Other → first N lines

Store the summary as the FileRef's content so it's useful in context assembly, not just a path pointer.

*Inspired by:* LCM's large file handling
*Impact:* Medium — makes file references useful in context, not just metadata

**4. Immutable Session Log Mode**

Optional mode where raw session transcript chunks are stored as episodic memories with `session:<id>` tags. Hook-extracted learnings become semantic memories that link back to the episodic source via `refines` links.

This provides lossless drill-down: the semantic memory says "Redis GET returns nil for missing keys," and the linked episodic memory preserves the full debugging exchange that discovered it.

*Inspired by:* LCM's immutable store + derived summaries
*Impact:* Medium-high — enables "show me the original context" for any learning

### Lower Priority

**5. Batch Map Over Memories**

A `ghost map` command that processes matching memories through an LLM in parallel:
```
ghost map --query "project:X" --prompt "summarize into themes" --concurrency 8
```

Useful for periodic consolidation: "take all 50 project:ghost memories and produce 5 thematic summaries."

*Inspired by:* LCM's `llm_map`
*Impact:* Medium — useful for maintenance but not core retrieval

**6. Convergence Guarantee for Reflect**

Add a max-iterations bound to reflect and ensure each rule pass strictly reduces the actionable memory set. If a pass produces zero actions, terminate. If iterations exceed the bound, log a warning and stop.

*Inspired by:* LCM's three-level escalation guarantee
*Impact:* Low — defensive measure, current rules don't loop in practice

---

## Potential Integration: LCM + Ghost

The two systems could work together in a Claude Code session:

- **LCM** manages the active context window (intra-session compaction, lossless history)
- **Ghost** manages cross-session knowledge (persistent learnings, lifecycle, semantic search)
- **Bridge:** At compaction time or session end, LCM summaries feed into Ghost as episodic memories. Ghost's `SessionStart` hook loads relevant cross-session knowledge into LCM's active context.

This would give an agent both lossless intra-session history *and* curated cross-session knowledge — the best of both approaches.
