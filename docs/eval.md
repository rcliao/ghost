# Ghost Eval Framework

Quantitative benchmarks for ghost's retrieval, context assembly, and lifecycle systems. Runs as standard Go tests — no external dependencies.

## Quick Start

```bash
# Baseline (FTS+LIKE only)
go test ./internal/store/ -run TestEval -v -count=1

# With local embeddings
GHOST_EMBED_PROVIDER=local go test ./internal/store/ -run TestEval -v -count=1

# Extract JSON report
go test ./internal/store/ -run TestEvalReport -v -count=1 2>&1 | grep EVAL_REPORT | sed 's/.*EVAL_REPORT://'

# Individual scenario
go test ./internal/store/ -run TestEvalMultiHop -v
```

## Architecture

Three files in `internal/store/`:

| File | Purpose |
|------|---------|
| `eval_seed.go` | ~43 seed memories across 5 namespaces + `SeedStore()` helper |
| `eval_metrics.go` | `PrecisionAtK`, `RecallAtK`, `MRR` + JSON report types |
| `eval_test.go` | 14 test functions, 43 report scenarios |

### Seed Corpus Design

The seed corpus models a realistic agent memory store with deliberate challenges:

- **Semantic clusters**: Multiple memories about "database", "deployment", "architecture" to test ranking
- **Distractors**: `project:beta` has similar keywords (e.g., both alpha and beta have "pool exhaustion" incidents)
- **Temporal variation**: Deploys at 18h, 168h, 720h ago; incidents at different dates
- **Negation facts**: Explicit "we do NOT use X" memories
- **Multi-hop chains**: Facts that link through shared entities (gateway -> service -> database)
- **Reflect triggers**: Memories with specific age/access/utility patterns to trigger each lifecycle rule

## Test Scenarios

### TestEvalHotPath (7 subtests)

Models how shell actually uses ghost for system prompt construction:

**Layer 1 — SystemPrompt via `List()`**: Static system instruction assembled from identity/tools namespaces with char budget.

**Layer 2 — InjectContext via `Context()`**: Semantic augmentation prepended to user messages with token budget and tier pinning.

### TestEvalColdPath (12 subtests)

Mid-conversation recall at three difficulty levels:

| Difficulty | Description | Hard fail? |
|------------|-------------|------------|
| Keyword | Literal term overlap (e.g., "PostgreSQL pgvector") | Yes |
| Semantic | Meaning overlap, few shared keywords (e.g., "how to ship code" -> "releasing code, merge PR, ArgoCD") | No |
| Adversarial | Distractors share keywords but wrong meaning (e.g., DB pool vs thread pool) | No |

### TestEvalNoResultPrecision (5 subtests)

Queries about things NOT in memory — the system should return nothing useful:

- "Kubernetes pod deployment" — absent technology, should not return `ship-process` as false positive
- "MongoDB replica set" — absent DB, should not return `data-layer` (PostgreSQL)
- "Python virtual environment" — absent language
- "chocolate soufflé" — completely unrelated domain

### TestEvalVagueQueries (6 subtests)

Underspecified searches that agents actually make:

- "the config" — vague, multiple acceptable answers
- "that issue we had" — any incident memory is acceptable
- "deploy" — single word, should find deploy-related memories
- "the database stuff" — tests whether vague DB query surfaces alpha's data-layer

### TestEvalMultiHop (4 subtests)

Queries that require combining info from 2-3 memories:

- "what database does the API gateway route user requests to?" — needs `atlas-routing` + `user-service-arch` + `data-layer`
- "how does search work end-to-end from API to database?" — needs gateway + search service + data layer
- "what was in the latest deploy and how does the pipeline work?" — needs recent deploy + ship-process
- "what caused the search incident and what safeguards exist?" — needs incident + architecture

### TestEvalTemporalRecall (5 subtests)

Recency-sensitive queries where newer memories should rank higher:

- "what did we deploy yesterday" — should prefer 18h-old deploy over 720h-old
- "most recent production deployment" — recency must dominate
- "what happened last week" — mid-range temporal targeting
- Broad "deployment history" — all three deploys should appear

### TestEvalNegation (5 subtests)

Queries about what the system does NOT use:

- "do we use Redis?" — should find the explicit "no Redis" memory
- "does beta use a relational database?" — should find beta-storage (says no RDB), NOT alpha's PostgreSQL
- "what API style?" — should surface the "REST only, no GraphQL" memory

### TestEvalUtilityFeedback (2 subtests)

Tests the utility feedback loop:

- **Utility boost**: Call `UtilityInc` 10 times on a memory, verify it ranks higher in subsequent `Context()` calls
- **Low utility pruning**: Memory with 10 accesses but 1 utility (ratio 0.1) gets pruned by reflect

### TestEvalAccessPromotion (1 test)

Full access-driven promotion loop:

1. Create STM memory
2. Access it 5 times via `Get()` (access_count > 3)
3. Backdate to >24h old
4. Run `Reflect()` — verify promotion to LTM
5. Run `Reflect()` again — verify LTM persists (no demotion)

Tests the promote rule: `Tier: "stm", AccessGT: 3, AgeGTHours: 24 → PROMOTE to LTM`

### TestEvalIdempotent (4 subtests)

Deduplication and re-storage behavior:

- **Same key, same content**: Creates v2 (current behavior — no dedup check)
- **Search no duplicates**: Same key doesn't appear twice in search results
- **Similar content, different keys**: Both appear in search (legitimate distinct memories)
- **Context no duplicates**: Same key doesn't appear twice in context assembly

### TestEvalContextEfficiency (3 subtests)

Information density within budget:

- **Relevant not crowded out**: Under tight budget (200 tokens), the most relevant memory should still appear even with pinned identity memories
- **Budget utilization**: With generous budget, utilization should be reasonable (>50%)
- **Pin vs no-pin**: Removing pins should free budget for more search-relevant results

### TestEvalMemoryCorrection (6 subtests)

User corrects a stale fact via `Put()` versioning:

1. V1 is searchable before update
2. Put creates V2
3. Get/Search/Context all return V2
4. History preserves both versions

### TestEvalReflectLifecycle (6 subtests)

Validates all five lifecycle rules on seeded trigger candidates:

| Rule | Seed Setup | Expected |
|------|-----------|----------|
| DECAY | STM, 96h old, 1 access (<10 threshold) | importance * 0.95 |
| PROMOTE | STM, 48h old, 12 accesses (>10 threshold) | tier -> ltm |
| DEMOTE | LTM, 200h old, never accessed (>168h unaccessed) | tier -> dormant |
| PRUNE | STM, 10 accesses, 1 utility | soft-deleted |
| PINNED | pinned LTM, 200h old, 0 access | unchanged |

### TestEvalScale (4 subtests)

500 noise memories + 43 seed memories:

- Signal-in-noise: keyword searches still find targets in top-5
- NS isolation: noise doesn't leak into scoped queries
- Latency: search stays under 100ms threshold
- Budget compliance: context assembly respects limits at scale

## Baseline Results (FTS-only, no embeddings)

```
Total:     43 scenarios
Passed:    37
Failed:     6 (all benchmark, no hard failures)
```

### Retrieval Metrics

| Category | Metric | Score | Notes |
|----------|--------|-------|-------|
| Keyword | MRR | 1.00 | Perfect — literal term overlap works |
| Semantic | MRR | 0.51 | Weak — "ship code" can't find "releasing code, ArgoCD" |
| Adversarial | MRR | 0.50 | Improved — dormant/sensory tier exclusion removes noise |
| Multi-hop | Recall | 0.29 | Very weak — only finds 1 of 3 needed pieces on average |
| Temporal | Rank-1 accuracy | 0.67 | Improved — temporal intent detection boosts recency for time-sensitive queries |
| Negation | Accuracy | 1.00 | Good — absence facts contain the keywords users search for |
| Vague | Accuracy | 1.00 | Acceptable result found 5/6 times (misses "database stuff") |
| Correction | Accuracy | 1.00 | Perfect — versioning works correctly |
| Reflect | Accuracy | 1.00 | Perfect — all lifecycle rules fire correctly |

### Agent Trust Metrics

| Category | Finding |
|----------|---------|
| No-result precision | FTS returns results for 3/5 absent queries — risk of false confidence |
| Utility feedback | `UtilityInc` did not improve context rank — accessFreq weight (0.15) too weak |
| Context efficiency | Budget utilization only 38% at 1000 tokens — system under-fills |
| Deduplication | Same-key versioning works; no duplicate keys in search/context |
| Access promotion | Full loop works: 5 gets + 24h age → STM promoted to LTM |

### Scale Performance

| Metric | Value |
|--------|-------|
| Corpus size | 543 memories |
| Avg search latency | 2ms |
| Signal survives noise | 4/4 queries |
| Budget compliance | Yes |

## Key Findings

### What works well
- **Keyword recall is perfect** — if the user's query shares terms with the memory, it's found
- **Negation works** — because absence memories ("we don't use Redis") contain the keywords users search for
- **Versioning is correct** — Put/Search/Context all respect the supersedes chain
- **Scale is fine** — 2ms at 500+ memories, signal survives noise
- **Deduplication** — search and context never return duplicate keys
- **Access-driven promotion** — full STM → LTM lifecycle works end-to-end
- **Reflect lifecycle** — all 5 rules (decay, promote, demote, prune, identity protect) fire correctly

### Where the system falls short

1. **Semantic gap (MRR 0.51)**: "how do we ship code?" returns nothing because content says "releasing code, merge PR, ArgoCD" with zero overlap. Classic vocabulary mismatch.

2. **Temporal ranking (67% accuracy)**: Temporal intent detection boosts recency for time-sensitive queries. Remaining failure (`recent_incident`) is a semantic gap — "latency spike" doesn't FTS-match "performance degradation" without embeddings.

3. **Multi-hop recall (0.29)**: Searching "what database does the gateway use" only finds `atlas-routing` — can't follow the chain to `user-service-arch` and `data-layer` because they don't share keywords with the query.

4. **Adversarial disambiguation (MRR 0.50)**: Improved by excluding dormant/sensory tier noise from default search results. Further gains expected from embeddings.

5. **No-result precision**: FTS returns results for absent queries like "MongoDB replica set" (returns `search-service-arch`, `data-layer`). An agent would incorrectly believe it has relevant knowledge.

6. **Utility feedback doesn't affect ranking**: Calling `UtilityInc` 10 times didn't improve a memory's context rank. The `accessFreq` component (0.15 weight in context scoring) is too weak relative to other factors.

7. **Context under-utilization**: At 1000 token budget, only 383 tokens used (38%). The system leaves budget on the table instead of packing more useful memories.

### Improvement targets

| Problem | Likely fix | Expected impact | Status |
|---------|-----------|-----------------|--------|
| Semantic gap | Embedding-based search (all-MiniLM-L6-v2) | Semantic MRR 0.51 → ~0.8+ | Open |
| Temporal ranking | Boost recency weight for episodic queries, or time-aware reranking | Temporal accuracy 0% → ~60%+ | **Done** (0.67) |
| Multi-hop | Return more diverse results (MMR), or semantic linking | Multi-hop recall 0.29 → ~0.6+ | Open |
| Adversarial | Embeddings + namespace-aware scoring | Adversarial MRR 0.25 → ~0.7+ | **Partial** (0.50) |
| No-result precision | Relevance threshold — suppress results below minimum score | Reduce false positives | Open |
| Utility feedback | Increase utility weight in context scoring, or add dedicated utility_ratio factor | Make utility signal meaningful | Open |
| Context fill | Lower minimum score threshold for budget fill, or excerpt more memories | Budget util 38% → 70%+ | Open |

## External Benchmarks

### LongMemEval (ICLR 2025)

Published benchmark for long-term memory abilities: 500 questions across 6 categories testing information extraction, multi-session reasoning, temporal reasoning, knowledge updates, and abstention.

Paper: https://arxiv.org/abs/2410.10813 | Dataset: https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned

**Setup:**

```bash
# Download dataset (see testdata/longmemeval/README.md)
cd testdata/longmemeval/
wget https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_s_cleaned.json

# Build embedding cache (one-time, ~30-60 min depending on model)
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_s.json \
  go test ./internal/store/ -run TestLongMemEvalBuildCache -v -timeout 120m

# Run benchmark with cache (~21 seconds for all 470 questions)
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_s.json \
  go test ./internal/store/ -run TestLongMemEval -v -timeout 30m

# FTS-only baseline (no embeddings, ~78 seconds)
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
  go test ./internal/store/ -run TestLongMemEval -v -timeout 10m

# Debug single question
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_s.json \
GHOST_BENCH_QUESTION=42 \
  go test ./internal/store/ -run TestLongMemEvalSingleQuestion -v
```

**Protocol:** For each question, ingests ~50 timestamped chat sessions via `BenchInsert()`, queries via `Search()`, and measures retrieval metrics against human-annotated evidence session IDs.

### LoCoMo (Snap Research, 2024)

Benchmark for long-term conversational memory: 10 conversations (~27 sessions each, ~300 turns), 1,986 QA pairs across 5 categories.

Paper: https://arxiv.org/abs/2402.17753 | Repo: https://github.com/snap-research/LoCoMo

**Setup:**

```bash
cd testdata/locomo/
wget https://raw.githubusercontent.com/snap-research/LoCoMo/main/data/locomo10.json

# Build cache + run (~4 seconds with cache)
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LOCOMO=testdata/locomo/locomo10.json \
GHOST_BENCH_EMBED_CACHE=testdata/locomo/embed_cache.json \
  go test ./internal/store/ -run TestLoCoMoBuildCache -v -timeout 30m

GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LOCOMO=testdata/locomo/locomo10.json \
GHOST_BENCH_EMBED_CACHE=testdata/locomo/embed_cache.json \
  go test ./internal/store/ -run TestLoCoMo -v -timeout 30m
```

### Benchmark Results Summary

**Improvement journey (LongMemEval_S, 470 questions):**

| Stage | Recall@5 | MRR | NDCG@10 |
|-------|----------|-----|---------|
| FTS-only | 0.141 | 0.169 | 0.149 |
| + MiniLM embeddings | 0.642 | 0.703 | 0.639 |
| + Threshold 0.3→0.2, RRF k 60→20 | 0.766 | 0.713 | 0.700 |
| + Term overlap reranking + relative temporal | 0.795 | 0.785 | 0.757 |
| + Cross-encoder reranking (ms-marco-MiniLM) | 0.849 | 0.857 | 0.835 |
| + User-turn indexing + knowledge-update detection | 0.876 | 0.835 | — |
| + Windowed speaker chunks (500-char, 1-turn overlap) | **0.908** | 0.827 | — |
| Paper BM25 (_M, harder dataset) | 0.634 | — | 0.540 |
| Paper Contriever (_M, harder dataset) | 0.723 | — | 0.663 |

**LongMemEval per-type (latest: windowed speaker chunks, no reranker):**

| Question Type | n | Recall@5 | MRR |
|---|---|---|---|
| **Overall** | **470** | **0.908** | **0.827** |
| knowledge-update | 72 | — | **0.946** |
| single-session-user | 64 | — | **0.886** |
| multi-session | 121 | — | 0.879 |
| temporal-reasoning | 127 | — | 0.818 |
| single-session-assistant | 56 | — | 0.725 |
| single-session-preference | 30 | — | 0.429 |

**Key wins from user-turn indexing:**
- single-session-user: MRR 0.604 → **0.858** (+25.4%), R@5 0.703 → **0.922** (+21.9%)
- knowledge-update: MRR 0.877 → **0.938** (+6.1%), R@5 0.792 → **0.938** (+14.6%)
- single-session-assistant R@5: 0.982 → **1.000** (perfect recall)
- Overall R@5: 0.849 → **0.876** (+2.7%) — beats prior best even without cross-encoder

**LoCoMo per-category (best: windowed speaker chunks + edges + multi-query):**

| Category | n | Recall@5 | MRR | NDCG@10 |
|---|---|---|---|---|
| **Overall** | **1,532** | **0.750** | **0.595** | **0.642** |
| single-hop | 281 | — | **0.630** | 0.583 |
| multi-hop | 89 | — | **0.497** | 0.544 |
| open-domain | 841 | — | **0.600** | 0.672 |
| temporal | 321 | — | **0.576** | 0.644 |

**LoCoMo improvement journey (session-over-session):**
- Baseline (session-only chunks): MRR 0.398, R@5 0.501
- + speaker-agnostic turn indexing: MRR 0.433, R@5 0.547
- + windowed speaker chunks (500-char windows, 2-turn overlap): MRR **0.595**, R@5 **0.750** (+49.5%)
- + edge expansion (entity co-occurrence) and multi-query decomposition

**Run with best config:**
```bash
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LOCOMO=testdata/locomo/locomo10.json \
GHOST_BENCH_EMBED_CACHE=testdata/locomo/embed_cache.json \
GHOST_BENCH_EXPAND_EDGES=1 \
GHOST_BENCH_MULTI_QUERY=1 \
  go test ./internal/store/ -run TestLoCoMo -v -timeout 30m
```

**Key findings:**
- Ghost significantly exceeds published BM25 and Contriever baselines (on easier _S dataset)
- User-turn indexing was the biggest single improvement: single-session-user MRR 0.604→0.858 (+25%)
- Cross-encoder reranking was the biggest ranking quality improvement (MRR +9%, NDCG +10%)
- knowledge-update near-perfect: MRR 0.938, R@5 0.938 (was 0.877/0.792)
- LoCoMo is much harder — dense conversations with similar topics across sessions
- Remaining weak spots: `single-session-preference` (MRR 0.49), LoCoMo `multi-hop` (R@5 0.38)
- All achieved without LLM in the retrieval loop — pure local models (gte-small + ms-marco cross-encoder)

**Optional features (env vars):**
- `GHOST_RERANKER=local` — enables cross-encoder reranking (~42 min for 470 questions vs 21s without)
- `GHOST_EMBED_MODEL_LOCAL=gte-small` — better embedding model (same 384 dims, +7% recall)
- `GHOST_BENCH_EMBED_CACHE=path` — pre-computed embeddings for fast iteration
- `GHOST_BENCH_EXPAND_EDGES=1` — build entity-based edges and use edge expansion during search (multi-hop)
- `GHOST_BENCH_MULTI_QUERY=1` — decompose complex queries into sub-queries (multi-hop)
- All achieved without LLM in the loop — pure retrieval with local embeddings

### E2E Benchmark (Ghost + LLM)

End-to-end evaluation: Ghost retrieves memories → LLM answers questions. Three modes compared:
- **no-memory**: LLM answers with no context (baseline)
- **ghost**: LLM answers with Ghost-retrieved top-5 sessions + highlighting
- **oracle**: LLM answers with ground-truth evidence sessions (upper bound)

**LongMemEval E2E:**
```bash
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_s.json \
GHOST_BENCH_PER_TYPE=10 \
GHOST_BENCH_LLM_MODEL=haiku \
  go test ./internal/store/ -run TestE2ELongMemEval -v -timeout 120m
```

**LoCoMo E2E:**
```bash
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LOCOMO=testdata/locomo/locomo10.json \
GHOST_BENCH_EMBED_CACHE=testdata/locomo/embed_cache.json \
GHOST_BENCH_PER_CAT=5 \
GHOST_BENCH_LLM_MODEL=haiku \
  go test ./internal/store/ -run TestE2ELoCoMo -v -timeout 60m
```

**LongMemEval _M dataset** (harder, ~500 sessions/question):
```bash
# Download _M dataset
cd testdata/longmemeval/
wget https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_m_cleaned.json

# Build cache (larger dataset, takes longer)
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_m_cleaned.json \
GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_m.json \
  go test ./internal/store/ -run TestLongMemEvalBuildCache -v -timeout 240m

# Run benchmark (uses BatchBenchInsert for fast ingestion)
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_m_cleaned.json \
GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_m.json \
  go test ./internal/store/ -run TestLongMemEval -v -timeout 60m
```

**Retrieval improvements in this round:**
- **Knowledge-update detection**: Queries with update intent ("current", "now", "latest") get strong recency bias among topically relevant results
- **User-turn-aware scoring**: Sessions where the user (not assistant) mentioned relevant facts get a scoring boost
- **Improved multi-hop decomposition**: Comma-separated clauses, "both X and Y", multiple question words
- **BatchBenchInsert**: Single-transaction batch insert with batched embeddings — critical for _M dataset performance
- **LoCoMo edge expansion**: `BenchBuildEdges` + `ExpandEdges` flag for entity-based graph traversal

## Adding New Scenarios

1. Add seed memories to `DefaultSeedCorpus()` in `eval_seed.go` if needed
2. Add test cases to the appropriate `TestEval*` function in `eval_test.go`
3. Add report scenarios to `TestEvalReport` for JSON tracking
4. Run `go test ./internal/store/ -run TestEvalReport -v` to verify

Difficulty guidelines:
- **Keyword** tests are hard failures — they must always pass
- **Semantic/adversarial/multi-hop/temporal** tests log `BENCHMARK:` and pass even on failure — they track improvement over time
- **No-result/vague/efficiency** tests benchmark agent trust factors
- Use `notKeys` to test that false positives don't appear
