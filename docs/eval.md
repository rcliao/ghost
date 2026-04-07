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

# Run benchmark
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
  go test ./internal/store/ -run TestLongMemEval -v -timeout 30m

# Quick iteration (limit questions)
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
GHOST_BENCH_LIMIT=50 \
  go test ./internal/store/ -run TestLongMemEval -v -timeout 10m

# Debug single question
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
GHOST_BENCH_QUESTION=42 \
  go test ./internal/store/ -run TestLongMemEvalSingleQuestion -v
```

**Protocol:** For each question, ingests ~50 timestamped chat sessions as memories via `Put()`, queries via `Search()`, and measures retrieval metrics against human-annotated evidence session IDs.

**Files:**

| File | Purpose |
|------|---------|
| `longmemeval.go` | Dataset types, loader, benchmark runner |
| `longmemeval_test.go` | Test entry points (skip if dataset not present) |
| `testdata/longmemeval/` | Dataset files (git-ignored) + README |

**Baseline Results (FTS-only, no embeddings, `_s` dataset, 470 questions):**

| Question Type | n | Recall@5 | Recall@10 | Recall@50 | MRR | NDCG@10 |
|---|---|---|---|---|---|---|
| **Overall** | **470** | **0.141** | **0.289** | **0.987** | **0.167** | **0.152** |
| knowledge-update | 72 | 0.194 | 0.361 | 1.000 | 0.248 | 0.214 |
| multi-session | 121 | 0.165 | 0.304 | 0.982 | 0.212 | 0.179 |
| single-session-user | 64 | 0.188 | 0.391 | 0.984 | 0.136 | 0.172 |
| temporal-reasoning | 127 | 0.114 | 0.246 | 0.983 | 0.160 | 0.131 |
| single-session-assistant | 56 | 0.089 | 0.161 | 0.982 | 0.077 | 0.071 |
| single-session-preference | 30 | 0.033 | 0.267 | 1.000 | 0.061 | 0.086 |

**Interpretation:**
- Recall@50 ~99% — evidence is almost always found, just poorly ranked
- Recall@5 ~14% — FTS struggles to rank evidence above keyword-similar distractors
- Hardest: `single-session-assistant` (recalling assistant responses) and `single-session-preference` (implicit preferences) — minimal keyword overlap
- Easiest: `knowledge-update` and `multi-session` — more keyword-rich questions
- Embeddings expected to significantly improve Recall@5 and NDCG by capturing semantic similarity

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
