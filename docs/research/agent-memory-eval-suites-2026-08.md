# Agent-memory eval suites, August 2026: the field vs ghost's harness

## Research Question

Now that provenance and liveness monitoring are shipped, what should ghost's eval suite improve next?

- Q1. What properties do the latest external agent-memory eval suites measure, and which are judge-free?
- Q2. What does ghost's in-repo eval surface actually certify today, and under which config?
- Q3. Where are the gaps between the two, weighted by ghost's measured diseases and its LLM-free constraint?
- Q4. Which properties does the new provenance + liveness machinery make measurable for the first time?

## Summary

The field grew a provenance benchmark (Veracium, July 2026) — ghost shipped the mechanism before the field could score it.
Write-path suites matured: MPBench for poisoning, HaluMem's stage split, MemOps with judge-free operation traces.
The field's 2026 crisis is harness validity, which makes ghost's judge-free, red-first discipline the differentiator.

Ghost's own surface is broad — provenance, authority, scope, and liveness have no external equivalent — but config-hollow.
Only the liveness canary runs the production env; the reranker is exercised by zero tests; several suites log instead of fail.
The gap list: production-parity certification first, then adoption (MPBench, HaluMem stages, Veracium), then the unbuilt promotion-precision and scale instruments.

## Findings

### F1 — provenance became externally scoreable [Q1]

Ground Truth First / Veracium (arXiv 2607.21962, MIT) is the first suite scoring source attribution.
It builds ground truth before conversation text exists: per-fact validity intervals, sent-vs-received trust, third-party claims carrying the claimant as source.
Caveat: a versioned LLM judge scores it, with cross-family re-judging, though answers are script-valid by construction.
Elsewhere attribution appears only as a defect — the Penfield audit found 24 wrong-speaker answer keys inside LoCoMo's 6.4% corrupted labels.

### F2 — write-path suites matured, several judge-free [Q1]

MPBench (arXiv 2606.04329) benchmarks poisoning across four write channels: 3,240 cases, 50.46% average attack success.
HaluMem scores extraction, update, and QA separately; hallucinations originate at write time.
Judge-free options now exist for ghost's constraint: MemOps (gold operation traces), TEMPO (gold relevance), MemoryAgentBench's AR/CR subsets (substring match).
FAMA's binary criteria are gold-derived — convertible to judge-free grading.

### F3 — ghost certifies under the wrong config [Q2]

The inventory counts 24 in-repo suites plus five external harnesses.
But only `internal/store/eval_liveness_test.go:118` sets the production env; ten suites simulate the floor via params, bypassing the env branch.
`GHOST_RERANKER` is set by zero tests despite the largest measured deltas (multi-hop MRR 0.503→0.620, `docs/eval.md:719`).
Silent-pass debt: benchmark cases log instead of fail (`docs/eval.md:991`).
Staleness's sibling knob defaults to 0 versus the live 14-sibling failure (`internal/store/eval_staleness_test.go:560`).

### F4 — unbuilt roadmap items are now adoptable [Q3]

Scorecard against `docs/research/agent-memory-landscape-2026-08.md` §7: phase 1 one-of-six built; phase 2 zero-of-six, two partials.
Abstention remains skipped — `internal/store/halumem_test.go:84` hardcodes `SkipBoundary: true`.
The poisoning eval maps onto MPBench's write-channel taxonomy; the write-quality eval onto HaluMem's stages — adopt, not invent.
The existing knob matrix measures the wrong axes (freshness×utility, `internal/store/eval_personal_test.go:194`).
Promotion precision — against the measured 76% exposure-only ltm — has no instrument.

### F5 — harness validity is the field's wound, ghost's edge [Q3]

Vendor numbers on judge-scored suites stopped replicating.
The Zep–mem0 LoCoMo dispute spans 58.44 to 92.5 on one system; the gpt-4o-mini judge accepted 62.81% of intentionally wrong answers; MemDelta formalizes the confounds.
Ghost's discipline — red-first evals, loud preconditions, gold labels, deterministic clocks (`internal/store/eval_clock_test.go:15`) — is what this literature recommends.
The gap is coverage, not method.

### F6 — what provenance and liveness newly unlock [Q4]

Per-source quarantine and poisoning-forensics evals become possible only now: attack rows are attributable via `source_user`/`source_scope`.
Authority under adversarial writes is testable (a poisoned "stated" vs the person's real statement).
`GHOST_DIAG=1` stage counters make production liveness observable over time — a longitudinal instrument no external suite offers.
Absent everywhere, internal and external: promotion precision, answer stability, a 100k-scale envelope (`internal/store/eval_test.go:1629` stops at ~547), namespace-isolation invariants.

## Code References

- `internal/store/eval_liveness_test.go:118` — the only production-env suite
- `docs/research/agent-memory-landscape-2026-08.md` §7 — the roadmap this scorecard grades
- `internal/store/halumem_test.go:84` — abstention still skipped
- `docs/eval.md:991` — silent-pass benchmark cases
- `internal/store/eval_staleness_test.go:560` — undersized-corpus admission

## Open Questions

- Run Veracium despite its LLM-judge scoring (questions are script-valid by construction), or wait for a judge-free grader?
- Adopt MPBench cases verbatim (LLM-attack-generated) or re-derive an LLM-free subset shaped by its four write channels?
- Is a 100k-scale envelope worth the CI cost now, or after the production DBs cross 20k?
