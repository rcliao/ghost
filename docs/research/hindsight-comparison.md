# Hindsight: what to steal, what to refuse

Reviewed `github.com/vectorize-io/hindsight` at `559050e` (2026-09-25, MIT).
Citations are paths in that repo, mostly under `hindsight-api-slim/hindsight_api/`, unless prefixed `ghost:`.
Judged against ghost's intent: SQLite for memory, with a graph, deterministic algorithms with knobs; owned, pluggable, fast.

## What it is

A Postgres server with pgvector, not a library.
"Embedded" means an embedded Postgres daemon on `localhost:8888` with a five-minute idle shutdown (`hindsight-embed/README.md:9-15`); there is no single-file artifact.
Embeddings and the cross-encoder run locally and need no LLM.
Everything that creates structure does: facts exist only if a model emits `ExtractedFact` (`engine/retain/fact_extraction.py:255-294`), and `provider=none` "uses chunks mode (no fact extraction), and reflect/consolidation are disabled" (`engine/providers/none_llm.py:1-8`).

So the substrate and the write path are the opposite of ghost's.
The retrieval engineering is where it has learned things ghost has not yet had to.
Its evals are LLM-judged and need a provider; the LongMemEval numbers are not reproducible from the repo (`hindsight-system-evals/README.md:14-25`).

## Ghost's ground: retrieval algorithms with knobs

1. **Boost in rank space, never in score space.**
   Weighted RRF at `w=7` let one arm fill all 300 rerank slots; measured recall@20 fell 0.97 → 0.40.
   The fix, `1/(k + rank/w)`, cancels the `k` term so a boosted rank `r` beats another arm's `s` only when `r < w·s` (`engine/search/recall_boost.py:36-48`).
   Ghost fuses FTS, LIKE and vector by plain RRF today (`ghost:internal/store/search.go:506`); the day it weights an arm, this is the shape.

2. **Human levels over raw floats.**
   `BOOST_LEVELS = low / medium / high` map to tuned tuples, validated by a guard test (`engine/search/recall_boost.py:98-105`).
   That is "knobs an LLM can turn" without letting it invent a number.
   Ghost's `min_score`, `min_spread` and `budget` are bare floats and ints.

3. **Bounded multiplicative boosts around a neutral 0.5.**
   Final score is `CE × (1 + α(recency − 0.5)) × (1 + α(temporal − 0.5)) × (1 + α(proof − 0.5))` with α = 0.2, 0.2, 0.1, so no secondary signal moves a result more than ±10% and a missing signal is exactly neutral (`engine/search/reranking.py:32-46`).
   Ghost's context score is an additive weighted sum by kind (`ghost:internal/store/context.go:1136`); auditability favours the bounded form.

4. **A second fusion for dedup, not answering.**
   RRF averages down a near-identical twin that is first in one arm and absent in others, which caused duplicate creation during consolidation.
   `interleave_fusion` round-robins so every arm's head gets a slot (`engine/search/fusion.py:112`).
   Ghost's pair rules and consolidation share this failure shape; a restates queue should use its own candidate query.

5. **Per-stage score floors as an abstention knob** — `min_scores {semantic, keyword, reranker, final}` (`mcp_tools.py:1263-1271`), with the honest caveat that reranker scores are not calibrated across queries.
   Ghost has one floor.

6. **Tag groups with fuzzy resolution**: nested and/or/not over tags, trigram-resolved so a typo still matches (`mcp_tools.py:1255-1262`; in-process trigram Jaccard at `engine/entity_resolver.py:70-118`).
   Pure functions; a good fit for SQLite and Go.

7. **Delete-time maintenance queues, not sweeps.**
   Affected ids are enqueued inside the deleting transaction, so cost tracks the delete rather than the store (`engine/graph_maintenance.py:1-26`).
   Ghost's reflect is a sweep over everything.

8. **A regex-and-checksum secret/PII gate on the write path**, redact or block, "no LLM call, no external dependency" (`extensions/builtin/memory_defense_regex.py:1-8`).
   Ghost has no write-side scrubbing.

9. **Edge snapshots for revert.**
   Archiving a unit destroys its edges by cascade, so non-derivable edges are serialised onto the archive row for undo (`engine/causal_links.py:21-46`).
   Ghost's `disagree` deletes an edge outright; the same snapshot would make it reversible.

## The caller's ground

Hindsight's Claude Code integration plugs in through hooks, not MCP — `SessionStart`, `UserPromptSubmit` (45 s timeout), `Stop` (`hindsight-integrations/claude-code/hooks/hooks.json:1-35`) — and its skill tells the agent "your job is to decide **when** to store, not **what**".
Rendering lives in the API (`api/page_markdown.py`), consistent with ghost's rule that formatting is the caller's.

## Refuse

- **Postgres as the substrate.** A supervised daemon and a data directory is not a file you hand someone.
- **An LLM-mandatory write path.** Without a model there are no facts, no entities, no edges.
- **Consolidation by prompt rules.** Nine English rules, including "NO COMPUTATION" and "be very conservative with deletes" (`engine/consolidation/prompts.py:37-57`); each decision's `reason` is stored "diagnostic only" (`engine/consolidation/consolidator.py:816`).
  Ghost's rule id plus the features it saw is a reason that replays.
- **Collapsing causal types to one.** `causes`, `enables`, `prevents` are legacy; only `caused_by` is written, at constant weight 1.0 (`engine/causal_links.py:11-18`).
  Ghost's typed, weighted relations are strictly more expressive.
- **Archive-move as supersession.** Rows move to `invalidated_memory_units`, edges cascade away, history order is lost.
  Ghost's versions, compare-and-swap and bitemporal columns are the better primitive.
- **No lifecycle at all.** No tiers, no decay of rows, no forgetting; `access_count` was created and never written, then dropped (`tests/test_migration_drop_access_count.py:1-8`).

## What Hindsight does not have

It has no pair-rules analogue.
Its inferred edges are `semantic` (cosine ≥ 0.7) and `temporal` (time proximity), created blindly at insert with no candidate stage and no review; `caused_by` comes only from the extractor.
Contradiction, refinement and supersession exist only as sentences inside the consolidation prompt.
A deterministic relationship engine with an agree/disagree trace is a capability it lacks in any form.

## Not verified

Latency: none published in the repo.
The control-plane package declares ISC with no licence file while the tree is MIT; unexplained.
