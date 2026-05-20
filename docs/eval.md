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

### HaluMem (2025, retrieval-only harness implemented)

[HaluMem](https://arxiv.org/abs/2511.03506) ([repo](https://github.com/MemTensor/HaluMem),
[dataset](https://huggingface.co/datasets/IAAR-Shanghai/HaluMem)) is the first
operation-level hallucination benchmark for agent memory systems. The full
benchmark scores Extraction / Update / QA with LLM judges. Ghost ships a
**retrieval-only harness** (`internal/store/halumem.go`) for the QA task:
ingest gold `memory_points` per user → `Search` per question → score retrieved
keys against the question's evidence `memory_content`. No LLM in the Ghost loop.

**Setup:**

```bash
mkdir -p testdata/halumem
curl -L https://huggingface.co/datasets/IAAR-Shanghai/HaluMem/resolve/main/HaluMem-Medium.jsonl \
  -o testdata/halumem/HaluMem-Medium.jsonl

# Baseline (no reranker, ~few sec/user)
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_HALUMEM=testdata/halumem/HaluMem-Medium.jsonl \
GHOST_BENCH_USER_LIMIT=5 \
  go test ./internal/store/ -run TestHaluMemRetrieval -v -timeout 30m

# With cross-encoder rerank top-20
GHOST_EMBED_PROVIDER=local \
GHOST_RERANKER=local \
GHOST_RERANK_TOP_N=20 \
GHOST_BENCH_HALUMEM=testdata/halumem/HaluMem-Medium.jsonl \
GHOST_BENCH_USER_LIMIT=5 \
  go test ./internal/store/ -run TestHaluMemRetrieval -v -timeout 60m
```

**Initial results (HaluMem-Medium, 5 users, 688 questions, retrieval only):**

| Question type | n | Baseline MRR | + Rerank top-20 MRR | Δ |
|---|---|---|---|---|
| **Overall** | **688** | **0.349** | **0.571** | **+64%** |
| Memory Conflict | 182 | 0.520 | 0.732 | +41% |
| Basic Fact Recall | 190 | 0.301 | 0.566 | +88% |
| Generalization & Application | 201 | 0.319 | 0.485 | +52% |
| Multi-hop Inference | 65 | 0.283 | 0.509 | +80% |
| Dynamic Update | 50 | 0.116 | 0.441 | +280% |

R@5 overall: 0.381 → **0.572**. Cross-encoder rerank pays off heavily on Dynamic
Update — Ghost stores all memory_point versions; the reranker picks the latest
fact that actually answers the question. Memory Boundary questions
(abstention test, evidence=0) are skipped by `SkipBoundary` since recall isn't
meaningful for them.

**Out-of-scope for this harness:**
- Memory Extraction task — requires Ghost to extract memories from raw dialogue
  (currently we ingest pre-extracted gold `memory_points` directly).

**LLM-judge E2E (Accuracy / Hallucination / Omission)** — opt-in via env:

```bash
GHOST_EMBED_PROVIDER=local \
GHOST_RERANKER=local \
GHOST_RERANK_TOP_N=20 \
GHOST_BENCH_HALUMEM=testdata/halumem/HaluMem-Medium.jsonl \
GHOST_BENCH_USER_LIMIT=5 \
GHOST_BENCH_LLM_MODEL=haiku \
GHOST_BENCH_JUDGE_TOPK=5 \
  go test ./internal/store/ -run TestHaluMemRetrieval -v -timeout 60m
```

When `GHOST_BENCH_LLM_MODEL` is set, each non-Boundary question takes a second
pass: Ghost retrieves → `compressContext` distills → LLM answers → LLM-as-judge
scores three independent booleans (correct, hallucination, omission). Aggregated
across the run, these produce HaluMem's published rates: C ↑, H ↓, O ↓.
Results are reported alongside the retrieval metrics under `judge_correct`,
`judge_hallucination`, `judge_omission`.

The judge uses a single 3-line response format — one judge call per question
rather than three — to keep latency and cost down. Falls back to all-zeros on
parse failure or LLM error.

See `internal/store/halumem.go` for the loader + harness.

### LoCoMo-Plus (2026)

[LoCoMo-Plus](https://arxiv.org/abs/2602.10715v1) is a 2026 extension of LoCoMo that adds a "Cognitive" category: 401 cue-trigger pairs across four relation types (causal, state, goal, value). The benchmark tests whether a memory system can retrieve a semantically-disconnected cue given a later trigger — e.g., cue "I'm learning to say no" retrieved from trigger "I volunteered for that project and now I'm overwhelmed".

**Setup:**

```bash
curl -L https://raw.githubusercontent.com/xjtuleeyf/Locomo-Plus/main/data/locomo_plus.json \
  -o testdata/locomo/locomo_plus.json

GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LOCOMO_PLUS=testdata/locomo/locomo_plus.json \
GHOST_BENCH_EMBED_CACHE=testdata/locomo/embed_cache_plus.json \
  go test ./internal/store/ -run TestLoCoMoPlus -v -timeout 30m
```

**Baseline results (20-question sample, 5 per relation type):**

| Mode | Overall MRR | R@1 | causal | state | goal | value |
|------|-------------|-----|--------|-------|------|-------|
| Baseline (no LLM) | **0.029** | 0.005 | 0.080 | 0.012 | 0.016 | 0.010 |
| + LLM HyDE (Haiku) | **0.289** | 0.100 | 0.463 | 0.281 | 0.274 | 0.136 |
| + LLM Rewrite (Haiku) | **0.314** | 0.200 | 0.294 | 0.332 | **0.507** | 0.121 |

**Key finding**: LLM-assisted query transformation **10× retrieval quality** on cognitive memory. Ghost itself stays LLM-free — the LLM runs only in benchmark orchestration to transform the query string before calling Ghost's search API. This validates the architecture: **Ghost as LLM-free infrastructure + LLM-at-edge for cognitive reasoning**.

**E2E LoCoMo-Plus with cognitive judge** (12-question sample, Haiku):

| Mode | Overall | causal | state | goal | value |
|------|---------|--------|-------|------|-------|
| no-memory | 0.500 | 0.500 | 0.500 | 0.500 | 0.500 |
| **ghost** | **0.750** | 1.000 | 0.500 | 0.667 | 0.833 |
| ghost-rewrite | 0.708 | 0.667 | 0.667 | 0.667 | 0.833 |
| oracle | 0.958 | 1.000 | 1.000 | 0.833 | 1.000 |

**Counterintuitive insight**: E2E judges the response, not the retrieval rank. Even though `ghost-rewrite` won retrieval 10× over `ghost`, plain `ghost` wins E2E (0.75 vs 0.71). The LLM compensates for merely-related cues; rewrite's different retrievals sometimes hurt response quality. **Retrieval precision matters most when the LLM is rigid**; when the LLM adapts, good-enough retrieval suffices.

**Expanded 5-mode comparison** (12-question sample, Haiku):

| Mode | Overall | causal | state | goal | value | LLM calls |
|------|---------|--------|-------|------|-------|-----------|
| no-memory | 0.542 | 0.500 | 0.500 | 0.500 | 0.667 | 1 |
| **ghost** | **0.792** | 1.000 | 0.667 | 0.667 | 0.833 | 1 |
| ghost-compress | 0.708 | 1.000 | 0.500 | 0.500 | 0.833 | 2 |
| ghost-rewrite | 0.625 | 0.500 | 0.500 | 0.667 | 0.833 | 2 |
| oracle | 0.875 | 1.000 | 1.000 | 0.667 | 0.833 | 1 |

**Cost-quality-latency analysis** (full 100-question run, 25 per type — causal/goal/state/value, Haiku 4.5 via `claude -p`, $1/M in + $5/M out):

| Mode | Score | In-Tok | Out-Tok | Latency | $/question |
|------|-------|--------|---------|---------|-----------|
| no-memory | 0.515 | 146 | 83 | 7.9s | $563μ |
| ghost | 0.630 | 389 | 95 | 9.1s | $865μ |
| ghost-compress | 0.660 | 303 | 103 | 21.4s | $818μ |
| **ghost-compress-wide** | **0.670** | 359 | 107 | 24.3s | $894μ |
| oracle | 0.885 | 203 | 92 | 8.1s | $663μ |

**Per-relation-type breakdown (100q):**

| Type | no-memory | ghost | compress | compress-wide | oracle |
|------|-----------|-------|----------|---------------|--------|
| causal | 0.46 | 0.60 | **0.68** | **0.68** | 0.92 |
| goal | 0.56 | 0.62 | 0.60 | **0.66** | 0.88 |
| state | 0.56 | 0.58 | **0.68** | 0.60 | 0.84 |
| value | 0.48 | 0.72 | 0.68 | **0.74** | 0.90 |

**Key findings:**
- `ghost-compress-wide` is the overall winner (0.670), 4% better than plain `ghost` and closing **~42% of the oracle gap** over the no-memory baseline.
- **Per-type contrast**: wider retrieval helps `goal`/`value` (structured latent facts) but *hurts* `state` queries — plain `compress` at top-5 wins `state` 0.68 → 0.60 for compress-wide. State cues are narrower; adding more candidates dilutes signal.
- Compression modes cost **~2.7× latency** (extra LLM call), but input-token cost stays comparable to plain `ghost` because bullets are shorter than raw sessions.
- Oracle still leads by **21.5 pts** — even perfect retrieval-plus-compression leaves a model-reasoning gap to close.

**Revised takeaway**: For cognitive-memory / latent-cue queries, **recall matters more than precision** *only when the latent fact is broad*. Narrower cues (state) are better served by tight top-5. A future `ghost-compress-auto` mode could classify the query intent and choose the retrieval width adaptively.

**ghost-compress-wide** is the current Pareto leader at $894μ/q — closes ~42% of oracle's quality gap at ~$330μ premium over the no-memory baseline. For latency-sensitive paths, plain `ghost` (no compression) is the 90%-of-best choice at no extra LLM cost.

**Design principle validated**: Ghost's formatted retrieval (full sessions + query-relevant line highlighting with >>> prefix) is already well-tuned for LLM consumption. Pre-processing modes (rewrite, compress) that add extra LLM calls often hurt response quality by diverging from the user's original question intent.

Plain `ghost` achieves **90% of oracle** (0.792 / 0.875) at the same LLM cost as `no-memory`.

**Usage:**
```bash
# Baseline (pure retrieval)
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LOCOMO_PLUS=testdata/locomo/locomo_plus.json \
  go test ./internal/store/ -run TestLoCoMoPlus -v

# With LLM query rewriting (best for LoCoMo-Plus)
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LOCOMO_PLUS=testdata/locomo/locomo_plus.json \
GHOST_BENCH_LLM_REWRITE=1 \
GHOST_BENCH_LLM_MODEL=haiku \
  go test ./internal/store/ -run TestLoCoMoPlus -v
```

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
- + cross-encoder rerank top-20 (`GHOST_RERANKER=local GHOST_RERANK_TOP_N=20`): multi-hop MRR **0.503 → 0.620** (+23%), top-1 hits 28/89 → 45/89 — see breakdown below

**Multi-hop rerank-window expansion (2026-05-11):**

Baseline failure analysis showed 22/89 multi-hop questions had ground-truth at rank 6-15
(findable but past the top-5 cutoff). Default reranker window was `min(10, candidates)`,
so widening to top-20 lets the cross-encoder rescue these into top-5.

| Rank bucket (multi-hop, n=89) | Baseline | Rerank top-20 |
|---|---|---|
| top-1 | 28 | **45** |
| rank 2-5 | 35 | 22 |
| rank 6-15 | 22 | **16** |
| rank 16+ | 4 | 6 |

| Metric (multi-hop, n=89) | Baseline | Rerank top-20 | Δ |
|---|---|---|---|
| MRR | 0.503 | **0.620** | +0.117 (+23%) |
| R@5 | 0.610 | **0.632** | +0.022 |
| NDCG@10 | 0.540 | **0.613** | +0.073 |

Cross-category sanity check (PER_CAT=25, n=100, rerank top-20): Overall MRR 0.779,
R@5 0.793. Other categories healthy (open-domain MRR 0.941, temporal 0.849,
single-hop 0.773 — no regression vs full-baseline numbers reported above).

LongMemEval re-verified at R@5=0.9083, MRR=0.8277 (reranker not enabled in its
best config; `GHOST_RERANK_TOP_N` is no-op when reranker is off). The change is
purely additive — defaults preserve existing behavior.

**Remaining multi-hop failure mode** (22/89 at rank 6+ even after rerank top-20):
mostly inferential queries about specific people — "Would X be...?",
"What's X's political leaning?", "Does X live near beaches?". The cross-encoder
finds the right session within top-20 but doesn't lift it into top-5. Sessions
fit fully in 8×1024-char chunk coverage (max 5871 chars in LoCoMo), so chunk
truncation isn't the bottleneck. The reranker model (`ms-marco-MiniLM-L-6-v2`)
is trained on MS-MARCO passage ranking — likely under-confident on conversational
inference. Future levers to try: (a) score-blend cross-encoder with original
dense+FTS score instead of full replacement; (b) entity-co-occurrence boost for
queries that name a specific person; (c) stronger cross-encoder model.

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
- `GHOST_RERANK_TOP_N=N` — override default reranker window (10). Widening to 20 helps LoCoMo multi-hop (rescues evidence at rank 6-15 into top-5). Higher N → linearly more cross-encoder calls.
- `GHOST_RERANK_CHUNK_LEN=N` (default 1024) — chars per MaxP chunk fed to cross-encoder. Larger chunks cover more content per pair but cost grows O(L²) in attention.
- `GHOST_RERANK_CHUNKS_PER_DOC=N` (default 8) — cap chunks per doc. Lowering trades coverage for throughput; in LoCoMo testing 4 chunks regressed multi-hop MRR vs 8.
- `GHOST_RERANK_ADAPTIVE=1` — opt-in skip when pre-rerank top-1 is overwhelmingly confident. `GHOST_RERANK_SKIP_TOP1` (default 0.9) and `GHOST_RERANK_SKIP_SPREAD` (default 0.2) tune the trigger. Skip ONLY — does not auto-narrow the window (narrowing regressed LoCoMo multi-hop MRR 0.620 → 0.525 in testing). On the current pre-rerank score scale (RRF+dense fusion, observed top-1 max ~0.85 on LongMemEval, ~0.7 on LoCoMo multi-hop) the default skip threshold rarely fires — useful as a safety hook, not a major throughput lever.
- `GHOST_RERANK_DEBUG=1` — log per-call timing + top-1 score to stderr for profiling.

### Reranker backend choice (GO vs ORT)

Cross-encoder reranking has two backends, switched via Go build tag:

| Backend | Build | Per-call cost (multi-hop, rerank-20) | Multi-hop MRR | LME PER_TYPE=20 MRR |
|---|---|---|---|---|
| **GO** (default, pure-Go) | `go build` | ~18.7s | **0.620** | 0.789 (no rerank: 0.829) |
| **ORT** (CGo + onnxruntime) | `go build -tags=ORT` | ~0.29s (~64× faster) | 0.517 | 0.789 |

**ORT speedup is real but currently regresses cross-encoder rerank quality.**
ORT applies sigmoid to cross-encoder scores — many candidate chunks clip to
exactly 0.0, scrambling MaxP's per-doc max and pushing useful evidence out of
top-5. Investigation TBD; likely needs a logit-mode toggle in hugot or a
custom score-normalization layer.

**Practical guidance:**

- **Default (no tag) — GO backend.** Use this for slice benchmarks where
  rerank quality matters. Scope to a single category (e.g.
  `GHOST_BENCH_LOCOMO_CAT=multi-hop`) or PER_CAT/PER_TYPE samples — full-corpus
  rerank at top-20 takes ~8 hours.
- **`-tags=ORT` — ORT backend.** Use for fast iteration on retrieval changes
  (no LLM in loop, end-to-end LME completes in ~20s for a 120-q sample), or
  when reranking is disabled and the dense+FTS path is what's being tested.
  See `make ort-install` for setup.

The throughput-lever options that don't change backends (adaptive skip,
chunk-size tuning, narrowing window) all regressed quality more than they
saved time on the hard slices. Their env knobs ship (`GHOST_RERANK_ADAPTIVE`,
`GHOST_RERANK_CHUNK_LEN`, `GHOST_RERANK_CHUNKS_PER_DOC`) but defaults preserve
known-good behavior.

#### ORT setup

```bash
# macOS (arm64)
brew install onnxruntime
make ort-install      # downloads libtokenizers.a v1.23.0 to ~/.ghost/libs
make ort-build        # CGO_ENABLED=1 go build -tags=ORT
make ort-test         # LongMemEval PER_TYPE=20 sanity check (~20s)
```

`GHOST_ONNXRUNTIME_PATH` must point at the **directory** containing the
shared library (not the file). Override via env if not at the platform default
(`/opt/homebrew/lib` on macOS arm64; `/usr/lib/x86_64-linux-gnu` on Debian).
- `GHOST_EMBED_MODEL_LOCAL=gte-small` — better embedding model (same 384 dims, +7% recall)
- `GHOST_BENCH_EMBED_CACHE=path` — pre-computed embeddings for fast iteration
- `GHOST_BENCH_EXPAND_EDGES=1` — build entity-based edges and use edge expansion during search (multi-hop)
- `GHOST_BENCH_MULTI_QUERY=1` — decompose complex queries into sub-queries (multi-hop)
- All achieved without LLM in the loop — pure retrieval with local embeddings

### E2E Benchmark (Ghost + LLM)

End-to-end evaluation: Ghost retrieves memories → LLM answers questions. Modes compared (select via `GHOST_BENCH_MODES=mode1,mode2,...`):

| Mode | Description | LLM usage |
|------|-------------|-----------|
| `no-memory` | LLM alone, no Ghost | 1 call (answer) |
| `ghost` | Ghost retrieves → LLM answers | 1 call (answer) |
| `ghost-hyde` | LLM writes hypothetical answer → Ghost searches with it → LLM answers | 2 calls (hyde + answer) |
| `ghost-rewrite` | LLM rewrites query with synonyms/concepts → Ghost searches → LLM answers | 2 calls (rewrite + answer) |
| `ghost-compress` | Ghost retrieves top-5 → LLM compresses to query-focused facts → LLM answers | 2 calls (compress + answer) |
| `ghost-compress-wide` | Ghost retrieves top-15 → LLM compresses wider set → LLM answers | 2 calls |
| `ghost-compress-auto` | Heuristic intent classifier picks top-5 (narrow) or top-15 (wide) per query → compress → answer | 2 calls |
| `ghost-rewrite-compress` | LLM rewrites query → Ghost searches → LLM compresses → LLM answers | 3 calls |
| `ghost-hyde-compress` | HyDE generates speculative cue → search with it → compress → answer | 3 calls |
| `ghost-compress-edges` | Ghost searches with 1-hop edge expansion → compress → answer | 2 calls |
| `ghost-agent` | LLM iteratively refines search query (up to 3 rounds) → LLM answers | 4-7 calls |
| `oracle` | Perfect evidence → LLM answers | 1 call (answer) |

**Ghost stays LLM-free** — hyde/rewrite modes invoke the LLM from benchmark orchestration. The `Store.Search` API accepts pre-transformed query strings with no coupling to an LLM. This lets us test integration patterns cleanly.

**Scoring options:**
- Default: token-F1 + flexible contains + token recall (max-signal)
- `GHOST_BENCH_LLM_JUDGE=1`: LLM-as-judge (correct=1.0, partial=0.5, wrong=0.0) — matches LoCoMo-Plus evaluation methodology

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
# Download _M dataset (~2.7 GB)
cd testdata/longmemeval/
curl -L -o longmemeval_m_cleaned.json \
  https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_m_cleaned.json

# Build cache. With ORT (~80 min for 230k unique texts on M-series). With GO
# (default) this takes ~5+ hours due to single-threaded simplego backend.
CGO_LDFLAGS="-L$HOME/.ghost/libs -L/opt/homebrew/lib" CGO_ENABLED=1 \
GHOST_ONNXRUNTIME_PATH=/opt/homebrew/lib \
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_m_cleaned.json \
GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_m.json \
  go test -tags=ORT ./internal/store/ -run TestLongMemEvalBuildCache -v -timeout 240m

# Run benchmark (no reranker, ORT build, ~7.5 min wall time for 470q)
CGO_LDFLAGS="-L$HOME/.ghost/libs -L/opt/homebrew/lib" CGO_ENABLED=1 \
GHOST_ONNXRUNTIME_PATH=/opt/homebrew/lib \
GHOST_EMBED_PROVIDER=local \
GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_m_cleaned.json \
GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_m.json \
  go test -tags=ORT ./internal/store/ -run TestLongMemEval -v -timeout 60m
```

**Results (LongMemEval _M, n=470, ORT build, no reranker, 2026-05-19):**

| Slice | n | R@5 | MRR | R@10 |
|---|---|---|---|---|
| **Overall** | **470** | **0.694** | **0.537** | 0.806 |
| single-session-assistant | 56 | 0.964 | 0.600 | 1.000 |
| knowledge-update | 72 | 0.840 | 0.712 | 0.931 |
| single-session-user | 64 | 0.797 | 0.551 | 0.875 |
| temporal-reasoning | 127 | 0.601 | 0.495 | 0.724 |
| multi-session | 121 | 0.598 | 0.526 | 0.761 |
| single-session-preference | 30 | 0.400 | 0.196 | 0.533 |

**Vs. published _M baselines (R@5):**

| System | R@5 |
|---|---|
| BM25 (paper) | 0.634 |
| **Ghost (ORT, no rerank)** | **0.694** |
| Contriever (paper) | 0.723 |

Ghost beats BM25 by 9.5%; trails Contriever by 4 points. The collapse vs _S
(R@5 0.908 → 0.694) is concentrated in slices where the 10× larger haystack
hurts — temporal-reasoning (0.984 → 0.601), multi-session (0.875 → 0.598),
single-session-preference (0.733 → 0.400). The "find the right session" slices
(assistant, knowledge-update, user) hold up well.

**With rerank top-20 (ORT)** the picture is unchanged — Overall R@5=0.690,
MRR=0.534 (vs no-rerank 0.694 / 0.537). Same pattern as _S: ORT's cross-encoder
sigmoid clips many candidate chunks to 0.0 → rerank tail is essentially
unordered → no quality lift. This is the open ORT-backend issue, not a property
of the _M dataset. Wall time stays under 8 minutes either way thanks to the
threaded backend.

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
