# Memory Edges: DAG-Based Retrieval for Ghost

**Status:** Fully implemented (Slices 1-3: schema, auto-link, edge expansion, CLI, MCP tool, co-retrieval strengthening, edge decay/pruning, contradicts force-include, consolidate with containment suppression)
**Date:** 2026-03-13

## Motivation

Ghost models memory *nodes* through a cognitive science lens (Tulving's taxonomy, Atkinson-Shiffrin tiers, kind-specific retrieval weights), but the *edges* between memories are inert. The `memory_links` table exists but is never used during search or context assembly.

Human memory retrieval is fundamentally associative. The dominant model is **spreading activation** (Collins & Loftus, 1975): when you recall a memory, activation spreads along associative links to related memories. The stronger the link, the more activation spreads. Related memories become easier to recall — not because you searched for them, but because they were *pulled in* by the activated memory.

Ghost is missing this. Today, `ghost_context` scores each memory independently. There's no "this memory activates that one." If you search for "auth," you get memories about auth ranked by relevance — but you don't get "JWT refresh rotation" pulled in because it's strongly associated with auth, even if the word "auth" doesn't appear in it.

Edges give us **retrieval by association, not just by content match.**

### Cognitive Science Grounding

Three properties of associative links in human memory:

| Property | In the brain | In Ghost (proposed) |
|----------|-------------|-------------------|
| **Strength** | Frequently co-activated memories form stronger links | `weight` — 0.0 to 1.0 |
| **Type** | Different relationships (causal, temporal, categorical, contradictory) | `rel` — relates_to, contradicts, depends_on, refines, contains, merged_into |
| **Directionality** | Some associations are symmetric, some aren't ("dog→pet" stronger than "pet→dog") | Some rels are directional (contains, refines), some aren't (relates_to) |

Additionally, association strength is **dynamic** — frequently co-activated links strengthen, unused ones weaken. Ghost edges should have their own lifecycle, mirroring how memory nodes already have access counts and decay.

---

## Design Principles

1. **Memory stays flat.** No `parent_key` or `depth` on the Memory struct. All structure lives in edges. This keeps the node model clean and allows true DAG topology (multiple parents).

2. **Edges are first-class.** They have weight, lifecycle metadata, and retrieval semantics. They're not an afterthought bolted onto a flat store.

3. **Retrieval is two-phase.** Find seed candidates, expand via edges, re-rank the combined pool uniformly. Edge-expanded memories compete on equal footing with direct matches.

4. **Agent-in-the-loop for edge creation.** The system suggests associations (via embedding similarity on `put`); the agent confirms them. No silent auto-linking.

5. **Dynamic edges.** Co-retrieval strengthens edges. Unused edges decay. Edges have their own lifecycle alongside memory nodes.

---

## Data Model

### Edge (replaces `memory_links`)

```go
type Edge struct {
    FromID         string    `json:"from_id"`
    ToID           string    `json:"to_id"`
    Rel            string    `json:"rel"`
    Weight         float64   `json:"weight"`          // 0.0–1.0, strength of association
    AccessCount    int       `json:"access_count"`     // incremented on co-retrieval
    LastAccessedAt *time.Time `json:"last_accessed_at"` // for decay calculation
    CreatedAt      time.Time `json:"created_at"`
}
```

### Schema

```sql
CREATE TABLE IF NOT EXISTS memory_edges (
    from_id         TEXT NOT NULL REFERENCES memories(id),
    to_id           TEXT NOT NULL REFERENCES memories(id),
    rel             TEXT NOT NULL,
    weight          REAL NOT NULL DEFAULT 0.5,
    access_count    INTEGER NOT NULL DEFAULT 0,
    last_accessed_at TEXT,
    created_at      TEXT NOT NULL,
    PRIMARY KEY (from_id, to_id, rel)
);
CREATE INDEX idx_edges_to ON memory_edges(to_id);
CREATE INDEX idx_edges_weight ON memory_edges(weight DESC);
```

### Edge Types and Default Behavior

| Rel | Direction | Default Weight | Retrieval Behavior |
|-----|-----------|---------------|-------------------|
| `relates_to` | symmetric | 0.5 | Optionally pull in neighbor |
| `contradicts` | symmetric | 0.9 | Force-include (agent must see conflicts) |
| `depends_on` | dependent → dependency | 0.7 | Pull in dependency if budget allows |
| `refines` | new → old | 0.8 | Pull in; deprioritize the original |
| `contains` | parent → child | 0.6 | Retrieving parent suppresses children; retrieving child boosts parent |
| `merged_into` | absorbed → survivor | 0.0 | Audit trail only, never propagates activation |

Weight is a default per rel type, overridable per edge instance.

### Memory Struct

**Unchanged.** No `parent_key`, `depth`, or `source_keys`. All graph structure lives in edges.

The `contains` edge type handles hierarchical relationships (summaries containing sources). A memory can have multiple `contains` parents — true DAG, not a tree.

### Put Response (new field)

```go
type PutResult struct {
    Memory          *model.Memory    `json:"memory"`
    RelatedMemories []RelatedMemory  `json:"related_memories,omitempty"` // NEW
}

type RelatedMemory struct {
    NS         string  `json:"ns"`
    Key        string  `json:"key"`
    Content    string  `json:"content"`
    Similarity float64 `json:"similarity"` // embedding cosine similarity
}
```

On `put`, Ghost computes the embedding for the new memory (already happens for search indexing) and returns the top-N most similar existing memories. The agent can then create edges via a follow-up call.

---

## Retrieval: Two-Phase with Edge Expansion

### Current Flow (unchanged)

```
Phase 1: Load pinned memories (up to budget/3)
Phase 2: Search-based candidates scored by composite metric
         score = composite(relevance, recency, importance, access_freq) × tier_multiplier
```

### New Flow

```
Phase 1: Load pinned memories (unchanged)

Phase 2: Find seed candidates
         Search and score as today.
         score = composite(relevance, recency, importance, access_freq) × tier_multiplier

Phase 3: Edge expansion
         For each seed, follow top-K edges (sorted by weight, default K=5).
         Each neighbor enters the candidate pool with a propagated score.

Phase 4: Unified re-rank
         Merge seeds + neighbors into one pool.
         For memories that appear as both direct hit and neighbor, combine scores.
         Sort by final score, greedy-pack into budget (or top-k).
```

### Scoring Formula

**Direct candidates (seeds):**
```
direct_score = composite(relevance, recency, importance, access_freq) × tier_multiplier
```
Unchanged from today.

**Edge-expanded candidates (neighbors):**
```
propagated_score = parent_score × edge_weight × damping
```
Where `damping = 0.3` (configurable). This ensures a neighbor's propagated score is always a fraction of the seed's score.

**Combined score (when a memory is both a direct hit and a neighbor):**
```
final_score = direct_score + Σ(parent_score_i × edge_weight_i × damping)
```

Additive boost — a memory that is both directly relevant AND connected to other relevant memories surfaces higher. This mirrors spreading activation in cognitive science, where activation accumulates from multiple sources.

**Example:**

```
Memory A: direct_score = 0.8 (strong search match for "auth")
Memory B: direct_score = 0.4 (weak match for "auth")
          Edge from A → B, rel=depends_on, weight=0.7
          propagated from A: 0.8 × 0.7 × 0.3 = 0.168
          final_score = 0.4 + 0.168 = 0.568

Memory C: direct_score = 0.0 (no search match)
          Edge from A → C, rel=relates_to, weight=0.9
          propagated from A: 0.8 × 0.9 × 0.3 = 0.216
          final_score = 0.216

Memory D: direct_score = 0.3 (weak match)
          Edge from A → D, weight=0.6: 0.8 × 0.6 × 0.3 = 0.144
          Edge from B → D, weight=0.5: 0.568 × 0.5 × 0.3 = 0.085
          final_score = 0.3 + 0.144 + 0.085 = 0.529 (hub boost)
```

Memory D benefits from being connected to multiple relevant memories — it's a "hub" that sits at the intersection of related concepts.

### Practical Bounds

| Parameter | Default | Purpose |
|-----------|---------|---------|
| `damping` | 0.3 | Caps edge contribution relative to parent score |
| `max_edges_per_seed` | 5 | Limits expansion fanout per seed memory |
| `min_edge_weight` | 0.1 | Edges below this threshold are not traversed |
| `max_expansion_candidates` | 50 | Total cap on edge-expanded candidates |

---

## Edge Lifecycle

Edges are dynamic — they strengthen with use and decay without it.

### Co-Retrieval Strengthening

When two connected memories are both returned in the same `ghost_context` response, their shared edge is strengthened:

```
edge.access_count += 1
edge.last_accessed_at = now
edge.weight = min(1.0, edge.weight + 0.05)  // small increment per co-retrieval
```

This mirrors Hebbian learning: "neurons that fire together wire together." Memories that are frequently useful together become more strongly associated.

### Edge Decay

Edges that haven't been co-activated decay over time. This can be handled by the reflect system alongside memory decay:

```
# Proposed built-in rule: decay unused edges
If edge.last_accessed_at > 30 days ago AND edge.access_count < 3:
    edge.weight *= 0.9  (gradual weakening)

If edge.weight < 0.05:
    DELETE edge  (association too weak to be useful)
```

### Edge Reflect Rules

The existing reflect rules engine can be extended to evaluate edges:

| Rule | Condition | Action |
|------|-----------|--------|
| `sys-strengthen-coactive` | co-retrieved > 3 times | weight += 0.1 |
| `sys-decay-unused-edges` | not accessed in 30 days, access < 3 | weight *= 0.9 |
| `sys-prune-weak-edges` | weight < 0.05 | DELETE |

---

## Edge Creation

### Method 1: Agent-Initiated (primary)

`ghost_put` returns related memories in its response. The agent reviews them and creates edges via a new tool/command:

```bash
# CLI
ghost edge -n agent:claude --from-key jwt-config --to-key auth-overview --rel depends_on --weight 0.8

# MCP (new tool: ghost_edge)
ghost_edge(ns="agent:claude", from_key="jwt-config", to_key="auth-overview", rel="depends_on", weight=0.8)
```

### Method 2: System-Suggested on Put

When `ghost_put` stores a memory, it computes the embedding and finds similar existing memories (top-5 by cosine similarity, threshold > 0.5). These are returned in the response:

```json
{
  "memory": { "key": "jwt-refresh-rotation", "..." : "..." },
  "related_memories": [
    { "key": "jwt-config", "similarity": 0.82, "content": "Using JWT with..." },
    { "key": "auth-architecture", "similarity": 0.71, "content": "Auth flow uses..." }
  ]
}
```

The agent decides whether to create edges. The system does not auto-create them.

### Method 3: Reflect-Created (future)

The reflect system could discover and suggest edges during lifecycle evaluation by running pairwise similarity on memories within a namespace. This is similar to the existing `sys-merge-similar` rule but creates edges instead of merging.

---

## Migration

### From `memory_links` to `memory_edges`

Existing `memory_links` rows migrate directly:

```sql
INSERT INTO memory_edges (from_id, to_id, rel, weight, access_count, last_accessed_at, created_at)
SELECT from_id, to_id, rel,
       CASE rel
           WHEN 'contradicts' THEN 0.9
           WHEN 'refines' THEN 0.8
           WHEN 'depends_on' THEN 0.7
           WHEN 'relates_to' THEN 0.5
           WHEN 'merged_into' THEN 0.0
           ELSE 0.5
       END,
       0, NULL, created_at
FROM memory_links;
```

The old `memory_links` table can be dropped after migration.

### ID Stability

Current links reference memory IDs, which change on version updates. Two options:

1. **Resolve on read** — when traversing edges, if `from_id` or `to_id` points to a superseded version, follow the `supersedes` chain to find the current version. More complex but preserves existing edges.

2. **Re-link on put** — when a memory is updated (new version), migrate all edges from the old ID to the new ID. Simpler at read time, small write overhead.

Option 2 is cleaner — edges always point to current versions.

---

## Implementation Plan

### Phase 1: Edge Storage + Creation
- New `memory_edges` table (schema above)
- Migrate from `memory_links`
- `ghost edge` CLI command
- `ghost_edge` MCP tool
- `put` returns `related_memories` when embeddings are available
- Re-link edges on memory version update

### Phase 2: Edge-Aware Context Assembly
- Phase 3 (edge expansion) in `context.go`
- Unified scoring with additive boost
- Practical bounds (damping, max edges per seed, expansion cap)
- Co-retrieval strengthening (update edge weight + access count)

### Phase 3: Edge Lifecycle
- Edge decay in reflect system
- Edge-specific reflect rules
- Edge pruning (weak edges auto-deleted)

### Phase 4: Hierarchical Summaries (future)
- `ghost consolidate` command creates summary memory + `contains` edges
- Context assembly learns to prefer summaries and suppress contained children
- This is an application of the edge system, not a structural change

---

## Comparison with LCM

| Aspect | LCM | Ghost (with edges) |
|--------|-----|-------------------|
| **Structure** | Strict tree (summaries → messages) | True DAG (any-to-any edges, multiple parents) |
| **Edge types** | Implicit (parent/child only) | Explicit typed edges with weights |
| **Retrieval** | DAG traversal (expand summaries) | Spreading activation (score propagation) |
| **Edge lifecycle** | Static (summaries don't change) | Dynamic (edges strengthen/decay) |
| **Lossless** | Yes (originals always preserved) | No (Ghost stores extracted knowledge, not transcripts) |
| **Cross-session** | No (session-scoped) | Yes (edges persist across sessions) |

Ghost's edge model is more flexible than LCM's rigid tree — it supports arbitrary associations, not just containment. The tradeoff is that LCM's tree guarantees you can always drill down to the original; Ghost's DAG is richer but doesn't guarantee lossless provenance.

---

## Open Questions (Resolved and Remaining)

1. **~~Should `contradicts` edges force-include the contradicting memory even if it exceeds budget?~~** RESOLVED: Contradicts edges get a minimum propagated score of 80% of the seed's score, bypassing the normal damping cap. They compete in normal packing but rank near the top. Eval confirmed: contradicts score 0.31 vs seed 0.39 (80%).

2. **~~Should edge weight have a floor?~~** RESOLVED: Co-retrieval strengthening uses diminishing returns: `weight += 0.05 × (1 - weight)`, asymptotically approaching 1.0. A weight at 0.9 gets +0.005 per co-retrieval, while a weight at 0.1 gets +0.045.

3. **Multi-hop traversal** — Still single-hop only. Cognitive science supports 2–3 hops with heavy decay. The eval showed single-hop already doubles useful context (2 → 4 memories for "how does authentication work"). Multi-hop adds complexity and performance cost — defer until single-hop proves insufficient.

4. **Edge visualization** — Not yet implemented. A `ghost graph` command could output DOT format for Graphviz rendering. Low priority — edges are inspectable via `ghost edge --list`.
