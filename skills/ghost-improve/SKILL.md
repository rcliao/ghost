---
name: ghost-improve
description: Run ghost memory eval, reflect, and improve cycle
user_invocable: true
---

# Ghost Memory Improvement Cycle

You are running a self-improvement cycle for the ghost memory system. Your goal is to measure retrieval quality, identify problems, and take corrective action using ghost MCP tools.

## Step 1: Measure

Run the ghost eval CLI to get baseline precision/recall:

```bash
ghost eval --simulate --reflect
```

Parse the JSON output. Note:
- `avg_precision` — of returned memories, how many were relevant? (target: >0.75)
- `avg_recall` — of expected memories, how many were returned? (target: >0.90)
- Look at individual `results` — which queries have low precision or recall?

## Step 2: Diagnose

For each query with problems:

**Low recall (misses):** The expected memory wasn't returned.
- Use `ghost_get` to check if the memory exists and inspect its importance/tier
- If importance is low, boost it: `ghost_curate(ns, key, op="boost")`
- If it's in dormant tier, promote it: `ghost_curate(ns, key, op="promote")`

**Low precision (noise):** Irrelevant memories were returned.
- Check `ghost_expand(ns="eval:ghost")` — are there clusters that should be consolidated?
- If a noisy group exists, consolidate them: read each with `ghost_get`, write a summary, call `ghost_consolidate`
- If individual memories are truly irrelevant noise, diminish them: `ghost_curate(ns, key, op="diminish")`

## Step 3: Improve

Take action based on diagnosis:

1. **Boost missed memories** — increase importance so they rank higher
2. **Consolidate noisy clusters** — create parent summaries that suppress children
3. **Diminish noise** — reduce importance of irrelevant memories that keep appearing
4. **Archive stale** — archive memories that are outdated or no longer useful

## Step 4: Re-measure

Run eval again to see if scores improved:

```bash
ghost eval --simulate
```

Compare to Step 1 results. Report the delta.

## Step 5: Report

Summarize what you did and the improvement:

```
Improvement cycle complete:
- Precision: X% → Y% (Δ+Z%)
- Recall: X% → Y% (Δ+Z%)
- Actions taken: N boosts, M consolidations, K diminishes
- Specific fixes: [list what you changed and why]
```

Store the result as a ghost memory for trend tracking:
```
ghost_put(ns="eval:ghost", key="improvement-<timestamp>",
  content="<summary of changes and deltas>",
  kind="episodic", tags=["eval-result", "improvement"],
  tier="stm", importance=0.4)
```

## Usage

- `/ghost-improve` — run one improvement cycle
- `/loop 15m /ghost-improve` — continuous improvement every 15 minutes

## Important

- Only use the `eval:ghost` namespace — never modify production memories
- The eval namespace is self-contained with synthetic test data
- Focus on precision improvements — recall is usually already good
- If both precision and recall are above target (0.75/0.90), report "healthy" and skip improvements
