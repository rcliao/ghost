# Pair rules: deterministic relationship candidates with an audit trace

## Problem

Ghost's graph is where its deterministic algorithms are supposed to run, and the typed part of it is empty.
On the production store, 2,131 edges are `relates_to` and zero are `caused_by`, `prevents`, `implies` or `depends_on`.
Retrieval handling for those relations shipped in August (`internal/store/edge_policy.go`) and has had nothing to handle.

The only creator of typed edges, `infer-edges`, draws candidate pairs from `relates_to`.
Those are cosine near-neighbours, which are restatements.

Measured 2026-09-23 with Jev as the classifier: 0 causal relations in 100 cosine pairs.
0 of 6 proposed edges were genuine on hand review.

A throwaway rule (`cmd/pairproto`) generated pairs by shared named entity, time gap and low token overlap.
Same classifier, same store: 8 to 12 `caused_by` per 100, 5 of 7 graded right, 0 wrong, and no overlap with the cosine pool.

The relationship logic ghost does have is hard-coded and invisible: `freshness.go` writes `contradicts` on a regex, `autoLinkEdges` on a threshold.
Nobody can see why an edge exists, tune the rule, or review a firing.

## Goals / Non-Goals

Goals:

- A deterministic pair-feature extractor in the store, unit-tested, no model calls.
- Pair rules as data, in the pattern of `reflect_rules`, with two actions: propose a candidate, or assert an edge.
- Every firing recorded, so an agent or the owner can review and reverse it.
- The existing hard-coded relationship logic expressed as rules over the same features, in a later phase.
- Verified against the kept prototype and a graded fixture before each phase lands.

Non-goals:

- Deciding `caused_by`, `prevents` or `implies` inside the store. Rules narrow; the caller or an out-of-band classifier decides.
- A rule language. Struct conditions now; an expression engine only if rules must be user-written strings.
- Lifecycle audit for promotions and demotions. Same table shape, separate task.
- Any change to the retrieval hot path.

## Proposed Design

Three layers, each testable alone.

**Features.** `PairFeatures(a, b)` is a pure function over two memories.
It returns:

- shared entities, each with its document frequency
- days apart, and which memory is older
- token Jaccard
- cue kind on the newer side
- same person, same scope, key-prefix match
- the cosine band, when both have embeddings

Entity extraction gains a stopword pass: months, weekdays, and bare numbers are not entities.
Document frequency comes from a per-namespace count computed once per run, not stored.

**Rules.** A `pair_rules` row is a `PairCond` and a `PairAction`, persisted like `reflect_rules` and seeded with system defaults.
Conditions are AND-joined numeric and boolean thresholds over the features.
Actions: `propose <rel>` surfaces the pair through the existing candidates API; `assert <rel>` writes the edge.
Only rules whose precision has been measured on the fixture may `assert`.

**Trace.** Every firing writes a `rule_events` row: rule, pair, features seen, action, outcome.
`reviewed` and `verdict` columns let a caller agree or disagree; disagree on an assert removes the edge.
Reflect's promotions and demotions can write the same table later.

```
memories ──PairFeatures──▶ features ──pair_rules──▶ propose ─▶ candidates API ─▶ caller/Jev labels ─▶ CreateEdge
                                          └──────▶ assert  ─▶ CreateEdge
                                          └──────────────────▶ rule_events (always)
```

Candidate enumeration stays bounded: pairs are generated from the entity index, not all-pairs, and capped per run.

## Data Flow

1. A caller runs `ghost rules pairs --ns X` or `InferEdges`, which now takes `Source: rules` (NEW).
2. The store builds the entity index for the namespace and its document-frequency table (NEW).
3. For each entity within the frequency band, the store enumerates pairs and computes `PairFeatures` (NEW).
4. The rule evaluator matches each feature set against the enabled `pair_rules` (NEW).
5. For `propose`, the store returns the pair through `ListReasoningCandidates` (CHANGED: a source field).
6. For `assert`, the store calls `CreateEdge` (unchanged) with the rule id in the edge reason.
7. The store writes one `rule_events` row per firing (NEW).
8. A caller labels proposed pairs and calls `CreateEdge`, or reviews events and records a verdict (NEW interface).

Steps 2 to 7 are sequential inside one run; runs are idempotent because existing typed edges are skipped, as today.

## Components

- Feature extractor — `internal/store/pair_features.go` (NEW)
- Entity stopwords — `internal/entity/extract.go` (CHANGED)
- Rule model and evaluator — `internal/store/pair_rules.go` (NEW)
- Trace writer and review — `internal/store/rule_events.go` (NEW)
- Candidate source switch — `internal/store/edge_infer.go` (CHANGED)
- CLI `rules` subcommand — `internal/cli/rules.go` (NEW, needs approval)
- Prototype and fixture — `cmd/pairproto`, `testdata/pair_rules/` (kept)

## Data Model

```dbml
Table pair_rules {  // NEW — identity: id; lives in the store file
  id text [pk]
  ns text                     // empty = every namespace
  name text
  enabled int
  priority int
  created_by text             // system | caller id
  cond_min_shared_entities int
  cond_max_entity_df int
  cond_min_days_apart real
  cond_max_jaccard real
  cond_cue text               // any | correction | causal | none
  cond_same_user int          // -1 ignore, 0 different, 1 same
  cond_key_prefix_match int
  action_op text              // propose | assert
  action_rel text             // caused_by | prevents | implies | contradicts | refines | contains
  min_precision real          // measured on the fixture; assert requires >= 0.8
}

Table rule_events {  // NEW — identity: id; lives in the store file
  id text [pk]
  rule_id text [ref: > pair_rules.id]        // enforced
  from_id text [ref: > memories.id]          // enforced
  to_id text [ref: > memories.id]            // enforced
  features text               // JSON of PairFeatures as evaluated
  action_op text
  action_rel text
  edge_written int
  created_at text
  reviewed int                // 0 until a caller records a verdict
  verdict text                // agree | disagree | empty
  reviewed_by text
}

Table memory_edges {  // unchanged, adjacent
  from_id text
  to_id text
  rel text
  weight real
}

Table reflect_rules {  // unchanged; pair_rules mirrors its shape on purpose
  id text [pk]
}
```

Compatibility: both tables are additive `CREATE TABLE IF NOT EXISTS`; an older binary ignores them and the contract version is unchanged.
Edges written by `assert` are ordinary `memory_edges` rows; nothing reads `rule_events` on the hot path.

## Interfaces

- `PairFeatures(a, b *model.Memory, df map[string]int) Features` (NEW, library) — pure; used by step 3.
- `ListReasoningCandidates(ctx, p)` (CHANGED) — `p.Source` gains `rules`; result rows carry `rule_id` and `features`. Used by step 5.
- `InferEdges(ctx, p)` (CHANGED) — `p.Source: rules` selects the rule-generated pool instead of `relates_to`.
- `ReviewRuleEvent(ctx, id, verdict, by string) error` (NEW) — records agree or disagree; disagree on an asserted edge deletes it. Used by step 8.
- CLI `ghost rules pairs --ns X [--dry-run]`, `ghost rules events --ns X [--unreviewed]`, `ghost rules review <id> --verdict agree|disagree` (NEW subcommand; needs approval).
- MCP: `ghost_edge_candidates` gains `source: rules` (CHANGED); no new tool.

Errors: a rule referencing an unknown relation is rejected at insert; a run with no enabled rules returns zero events, not an error.

## Options Considered

### A. Struct-based rules, persisted like `reflect_rules` (chosen)

- Pros: no dependency, matches the codebase, unit-testable without a database, knobs an LLM can set through existing surfaces.
- Cons: adding a feature means adding a column; rules are not free-form.

### B. Expression rules with `cel-go` or `expr`

- Pros: user-written conditions, new features need no schema change.
- Cons: new dependency needing approval, a second language to document, sandboxing and error surfaces to own.
- Rejected for now; revisit if rule authors need conditions the struct cannot express.

### C. Keep rules in Go, as `freshness.go` does today

- Pros: simplest; nothing to persist.
- Cons: invisible, untunable, unauditable — the problem statement.
- Rejected.

### D. No rules; better classifier prompts on the cosine pool

- Pros: no store change.
- Cons: measured dead end — the pool contains no causal pairs to find.
- Rejected.

## Risks

- **Entity extraction is the weak feature.** Mitigated by the stopword pass and the frequency band; verified by re-running the prototype comparison after the change.
- **Assert rules writing wrong edges.** Mitigated by `min_precision` measured on the fixture, and by disagree deleting the edge; phase 1 ships only `propose`.
- **Pair enumeration cost on large stores.** Bounded by the frequency band and a per-run cap; measured on the 13k snapshot before landing.
- **Classifier probability drift.** Jev's probabilities on true causal pairs ran 0.40 to 0.71; floors are calibrated per relation from graded pairs, not defaulted. Accepted.
- **Fixture privacy.** The repo is public; the in-repo fixture is synthetic, the graded real pairs stay gitignored under `testdata/pair_rules/local/`.

## Definition of Done

Phase 1, this handoff:

- automated: `PairFeatures` unit tests pass on the synthetic fixture, including date-word exclusion.
- automated: rule evaluator tests pass with `propose` rules; `assert` is rejected when `min_precision` is unset.
- automated: `rule_events` written for every firing; review test flips `reviewed` and deletes an asserted edge on disagree.
- automated: `go test ./...` and `go vet ./...` green.
- manual: `cmd/pairproto` re-run on a fresh snapshot with the store's extractor reproduces the 2026-09-23 result within noise (≥ 5 `caused_by` per 100 through Jev).
- manual: owner reviews the two new tables.

Out of this handoff: asserting rules for `contradicts` and `contains`, migrating `freshness.go`, lifecycle events, the MCP source flag, an expression engine.

## Unresolved Questions

- **Blocking:** approve the two new tables and the `rules` CLI subcommand?
- **Blocking:** should `rule_events` also become the lifecycle audit table now, or stay pair-only until the lifecycle design is written?
- Non-blocking: does `assert` for `contradicts` replace `freshness.go` in phase 2, or run beside it?
- Non-blocking: is the synthetic fixture enough, or should the local graded file be a required manual gate before any `assert` rule is seeded?
