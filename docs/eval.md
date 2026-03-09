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
| `eval_test.go` | 8 test functions covering hot path, cold path, multi-hop, temporal, negation, correction, reflect, and scale |

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
| DECAY | STM, 96h old, 1 access | importance * 0.95 |
| PROMOTE | STM, 48h old, 8 accesses | tier -> ltm |
| DEMOTE | LTM, 200h old, 1 access | tier -> dormant |
| PRUNE | STM, 10 accesses, 1 utility | soft-deleted |
| IDENTITY | identity tier, 200h old, 0 access | unchanged |

### TestEvalScale (4 subtests)

500 noise memories + 43 seed memories:

- Signal-in-noise: keyword searches still find targets in top-5
- NS isolation: noise doesn't leak into scoped queries
- Latency: search stays under 100ms threshold
- Budget compliance: context assembly respects limits at scale

## Baseline Results (FTS-only, no embeddings)

```
Total:     36 scenarios
Passed:    28
Failed:     8 (all benchmark, no hard failures)
```

### Retrieval Metrics

| Category | Metric | Score | Notes |
|----------|--------|-------|-------|
| Keyword | MRR | 1.00 | Perfect — literal term overlap works |
| Semantic | MRR | 0.51 | Weak — "ship code" can't find "releasing code, ArgoCD" |
| Adversarial | MRR | 0.25 | Poor — can't disambiguate DB pool vs thread pool |
| Multi-hop | Recall | 0.29 | Very weak — only finds 1 of 3 needed pieces on average |
| Temporal | Rank-1 accuracy | 0.00 | Fails — recency weight (0.3) can't beat irrelevant FTS matches |
| Negation | Accuracy | 1.00 | Good — absence facts contain the keywords users search for |
| Correction | Accuracy | 1.00 | Perfect — versioning works correctly |
| Reflect | Accuracy | 1.00 | Perfect — all lifecycle rules fire correctly |

### Scale Performance

| Metric | Value |
|--------|-------|
| Corpus size | 543 memories |
| Avg search latency | 2ms |
| Signal survives noise | 4/4 queries |
| Budget compliance | Yes |

## Key Findings

### What FTS does well
- **Keyword recall is perfect** — if the user's query shares terms with the memory, it's found
- **Negation works** — because absence memories ("we don't use Redis") contain the keywords users search for
- **Versioning is correct** — Put/Search/Context all respect the supersedes chain
- **Scale is fine** — 2ms at 500+ memories, signal survives noise

### Where FTS falls short

1. **Semantic gap (MRR 0.51)**: "how do we ship code?" returns nothing because content says "releasing code, merge PR, ArgoCD" with zero overlap. This is the classic vocabulary mismatch problem.

2. **Temporal ranking (0% accuracy)**: "yesterday's deploy" returns `runbook` at rank 1 because FTS keyword relevance (0.5 weight) overwhelms recency (0.3 weight). The system finds deploy memories but doesn't rank them by time.

3. **Multi-hop recall (0.29)**: Searching "what database does the gateway use" only finds `atlas-routing` — it can't follow the chain to `user-service-arch` and `data-layer` because those memories don't share keywords with the query.

4. **Adversarial disambiguation (MRR 0.25)**: "database connection pool exhaustion" can't distinguish alpha's DB pool incident from beta's thread pool incident — both contain "pool exhaustion."

### Improvement targets

| Problem | Likely fix | Expected impact |
|---------|-----------|-----------------|
| Semantic gap | Embedding-based search (all-MiniLM-L6-v2) | Semantic MRR 0.51 -> ~0.8+ |
| Temporal ranking | Boost recency weight for episodic queries, or add time-aware reranking | Temporal accuracy 0% -> ~60%+ |
| Multi-hop | Return more diverse results (MMR), or semantic linking across memories | Multi-hop recall 0.29 -> ~0.6+ |
| Adversarial | Embeddings + namespace-aware scoring | Adversarial MRR 0.25 -> ~0.7+ |

## Adding New Scenarios

1. Add seed memories to `DefaultSeedCorpus()` in `eval_seed.go` if needed
2. Add test cases to the appropriate `TestEval*` function in `eval_test.go`
3. Add report scenarios to `TestEvalReport` for JSON tracking
4. Run `go test ./internal/store/ -run TestEvalReport -v` to verify

Difficulty guidelines:
- **Keyword** tests are hard failures — they must always pass
- **Semantic/adversarial/multi-hop/temporal** tests log `BENCHMARK:` and pass even on failure — they track improvement over time
- Use `notKeys` to test that false positives don't appear
