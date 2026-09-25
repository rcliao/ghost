# Research

Design explorations, paper analyses, and feature proposals for Ghost.

| Doc | Status | Summary |
|-----|--------|---------|
| [LCM Comparison](lcm-comparison.md) | Analysis | Comparison of Ghost vs LCM (Lossless Context Management) paper — complementary strengths, improvement ideas |
| [Memory Edges Design](memory-edges-design.md) | Implemented | DAG-based retrieval via weighted edges with spreading activation scoring |
| [Agent-Memory Eval Suites, 2026-08](agent-memory-eval-suites-2026-08.md) | Research (merged #129) | The field's eval suites vs ghost's harness: judge-free options, production-parity gap, what provenance + liveness newly make measurable |
| [Ghost As Built, 2026-09](ghost-as-built-2026-09.md) | Approved | Our why and what at `69b58ee`: intent, write/read/lifecycle data flow, data model, and nine gaps graded against the LLM-free intent |
| [What Makes a Good Agent Memory, 2026-09](ghost-expansion-research-2026-09.md) | In review | Judged against the owner's why — owned, encrypted, pluggable, fast, reliable: Codex shares Claude Code's hook shape, search takes 4.8 s with the default reranker on a 13k store, the standalone path runs untuned ([sources](ghost-expansion-research-2026-09.sources.md)) |
| [Supermemory Comparison](supermemory-comparison.md) | Analysis | Hosted, LLM-in-the-loop memory read for its integration surface: owned context block, store-side renderer with a fact budget, fail-open latency budget, dry-run destructive ops — and what to refuse |
| [Pair Rules Design](pair-rules-design.md) | Design (awaiting approval) | Deterministic pair-feature extractor + rules-as-data (propose/assert) + a reviewable firing trace; measured: rule pairs 8-12 caused_by/100 vs cosine 0 |
| [Memory-Infra Neighbours, 2026-09](memory-infra-neighbours-2026-09.md) | Research | LLM-free stores and embedded DBs measured against 'SQLite for memory': the vector gap is a driver bump, FSRS and Louvain exist in pure Go, governance ideas (write-back modes, ledger, superseded-stays-findable) need no dependency ([sources](memory-infra-neighbours-2026-09.sources.md)) |
| [Hindsight Comparison](hindsight-comparison.md) | Analysis | Postgres server with an LLM-mandatory write path; its retrieval engineering is the steal — rank-space boosting (measured 0.97→0.40 collapse otherwise), human-level knobs, bounded multiplicative boosts, interleave fusion for dedup, delete-time queues; it has no pair-rules analogue |
