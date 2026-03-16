---
name: ghost-improve
description: Run ghost memory eval, reflect, and improve cycle
user_invocable: true
---

# Ghost Memory Improvement Cycle

You are running a self-improvement cycle for the ghost memory system. Your goal is to measure retrieval quality, identify problems, and take corrective action using ghost MCP tools and parameter tuning.

## Step 1: Measure

Run the ghost eval CLI to get baseline precision/recall:

```bash
ghost eval --simulate --reflect
```

Parse the JSON output. Note:
- `avg_precision` — of returned memories, how many were relevant? (target: >0.75)
- `avg_recall` — of expected memories, how many were returned? (target: >0.90)
- Look at individual `results` — which queries have low precision or recall?

If both precision and recall are above target, report "healthy" and skip to Step 5.

## Step 2: Diagnose

For each query with problems:

**Low recall (misses):** The expected memory wasn't returned.
- Use `ghost_get` to check if the memory exists and inspect its importance/tier
- If importance is low → curate fix (boost)
- If it's dormant → curate fix (promote)

**Low precision (noise):** Irrelevant memories were returned.
- First, test if edge expansion is the cause:
  ```bash
  GHOST_EDGE_DAMPING=0 ghost eval --simulate
  ```
  If precision improves → edge expansion is the problem (tune parameters).
  If precision stays the same → FTS search scoring is the problem (curate data).
- Check `ghost_expand(ns="eval:ghost")` for clusters needing consolidation.

## Step 3: Improve

Two levers available — use the right one based on diagnosis:

### Lever A: Curate data (when individual memories are the problem)
1. **Boost missed memories** — increase importance so they rank higher
2. **Consolidate noisy clusters** — create parent summaries that suppress children
3. **Diminish noise** — reduce importance of irrelevant memories
4. **Archive stale** — move outdated memories to dormant

### Lever B: Tune parameters (when the algorithm is the problem)
Edge expansion parameters are configurable via env vars. Test combinations:
```bash
# Reduce expansion aggressiveness
GHOST_EDGE_DAMPING=0.15 GHOST_EDGE_MIN_WEIGHT=0.3 ghost eval --simulate

# Limit neighbors per seed
GHOST_EDGE_MAX_PER_SEED=3 ghost eval --simulate

# Raise minimum edge weight threshold
GHOST_EDGE_MIN_WEIGHT=0.5 ghost eval --simulate
```

Available env vars:
- `GHOST_EDGE_DAMPING` — score propagation factor (default 0.3, lower = less noise)
- `GHOST_EDGE_MIN_WEIGHT` — minimum edge weight to follow (default 0.1, higher = stricter)
- `GHOST_EDGE_MAX_PER_SEED` — max neighbors per seed (default 5, lower = less spread)
- `GHOST_EDGE_MAX_EXPANSION` — max total expanded memories (default 50)
- `GHOST_EDGE_MAX_BOOST` — max additive boost factor (default 0.5)

When you find a parameter combination that improves scores, note it in the report
for eventual baking into source code defaults.

## Step 4: Re-measure

Run eval again to see if scores improved:

```bash
ghost eval --simulate
```

Compare to Step 1 results. Report the delta.

## Step 5: Report

Summarize concisely:

```
Ghost improve: precision X%→Y% (ΔZ%), recall X%→Y% (ΔZ%)
Actions: N boosts, M consolidations, K diminishes, P param tests
[one line per specific fix]
```

Store the result for trend tracking via ghost CLI:
```bash
ghost put -n "eval:ghost" -k "improvement-YYYY-MM-DD-cycleN" \
  --kind episodic -t "eval-result,improvement" --tier stm --importance 0.4 \
  "summary text here"
```

## Important

- Only use the `eval:ghost` namespace — never modify production memories
- The eval namespace is self-contained with synthetic test data
- Focus on precision improvements — recall is usually already good
- When tuning parameters, always compare against the unchanged baseline
- If a parameter combination helps, report it — the user will bake it into defaults
