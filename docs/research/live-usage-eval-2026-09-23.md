# Live usage eval — does ghost help a coding agent? (2026-09-23)

A read-only audit of one live store (`agent:claude-code`, the Claude Code hooks) and 60 days of the sessions it served. Question: do the memories ghost injects actually get used, and is the lifecycle keeping the store healthy?

## Method

- **Store.** Snapshot of the live SQLite DB (copied, so no access counters moved). Counts use the latest version per `(ns, key)`.
- **Sessions.** 45 interactive Claude Code sessions whose transcripts carry a hook injection. SDK/eval runs, where the hooks don't fire, are excluded.
- **"Used".** A memory counts as used when its key, or a rare fragment of its content, appears later in assistant text or tool input. It must not arrive from a user prompt or tool result first, and injection blocks and memory writes don't count. Hits were then hand-graded; a null control (the same keys checked against sessions that never received them) gives the chance rate.
- **Relevance.** 30 per-prompt injections sampled with a fixed seed and judged relevant, partial or irrelevant.

Limits: exact-text matching misses paraphrased influence and silently avoided mistakes. Grading was done by one judge, and the 30-prompt sample is ±~17 points.

## Findings

### Usage

| Channel | Share of injected tokens | Strict reference rate |
|---|---|---|
| SessionStart hook | 20% | 10.6% |
| UserPromptSubmit hook (per prompt) | 80% | 2.0% (null control 2.6%) |
| Explicit `ghost_context` tool call | small | 13.1% |

- About 1.05M tokens were injected into 45 sessions (median about 7k per session, max 189k).
- After hand-grading, about 1–2% of injected memories were used, and 14 of 45 sessions had at least one real use.
- The real uses were concrete: a manual deploy step, a host name, a prod read-only SQL recipe, and a worktree gotcha.
- Per-prompt relevance was 5 relevant, 7 partial and 18 irrelevant. The worst injections came from harness turns (task notifications, subagent hand-backs) and short follow-ups ("how did it go now?").
- `ghost_curate` was called 0 times, even though a hygiene hint was shown 145 times.

### Store health

- 73% of active memories (1,109 of 1,519) had more than 20 accesses and 0 utility.
- Raw user-prompt captures were promoted to LTM and hit the access-log cap within days. Examples: `stop` (a table-row fragment), `think-fine-lets` ("I think that is fine, and lets…").
- Some memories the grading showed to be useful had already been demoted to dormant.
- Captures phrased "instead of …" were auto-linked with `contradicts` edges to real memories. Those edges are force-included past MinScore, so dormant captures still surfaced.

## Root causes (fixed in this PR)

1. **Access was counted on raw search hits, not on returned memories.** `Context()` called `touchMemories` for every `Search()` result, before the MinScore/MinSpread floor and the budget. A memory that lexically matched common words accrued access days without ever being shown. `sys-promote-to-ltm` reads access days as rehearsal, so it promoted these memories. `sys-prune-low-utility` skips memories with 2+ access days, so it spared them.
2. **A lone returned direct hit never earned utility.** Utility was credited only inside `strengthenCoRetrievedEdges`, which runs when 2+ memories are returned.
3. **The low-utility prune exempted zero utility.** `ruleMatches` returned false for `UtilityCount == 0`, so the rule could never fire on exactly the memories it targets.

The fix touches only **returned direct hits**. A first version touched everything returned; review caught that edge passengers (typed-edge neighbours, force-included `contradicts`) would then accrue access without utility and get promoted through the same door. `TestContextEdgePassengerNotPromoted` pins this.

With 1 and 2 fixed, "zero utility over more than 20 returned accesses" means "only ever an edge passenger", which is what the prune was written for. The spaced-access guard still protects anything accessed on 2+ distinct days.

## Not fixed here (follow-ups)

- **Explicit gets earn no utility.** `Get` records an access but never utility, so a memory fetched explicitly more than 20 times on one day, and older than 72h, is now prunable.
- **Typed-edge neighbours bypass the score floor** (#103), including dormant ones (#104). An auto-classified `contradicts` edge from a junk memory is enough to drag it into context.
- **Some memories are no longer touched by `Context`:** pinned memories (they're lifecycle-exempt, but `last_accessed_at` can be stale after an unpin), edge passengers, and children replaced by `substituteParents`. Children in LTM that are always represented by their parent will go stale and be demoted to dormant after 7 days.
- **The access-log cap (100 rows per memory) can erase the spacing guard.** A memory returned more than 100 times in one day collapses to 1 distinct day. This rarely matters now that direct-hit utility tracks access.
- **The GC prune is now inconsistent with reflect.** `sqlite_gc.go` still requires `utility_count > 0`.
- **Utility measures "returned as a direct hit", not "used".** A reference-detection signal (did the agent act on it?) would make the lifecycle and `GHOST_UTILITY_WEIGHT` reward usefulness instead of topicality.

## Operator-side changes made alongside (hooks, not in this repo)

- **Per-prompt hook:** skips harness turns and prompts of 5 words or fewer, and records the keys it injects so later prompts don't repeat them. One key had been re-injected 106 times in a single session.
- **SessionStart hook:** keeps the seen-keys list on `resume` and resets it on `startup`, `clear` and `compact`.
- **Heuristic prompt capture:** now opt-in (`GHOST_PROMPT_CAPTURE=1`).
- **LLM extraction prompt:** rejects status snapshots such as "PR awaiting merge".
- **One-time live cleanup** (backup taken first):
  - 128 prompt captures moved to dormant, with their 15 typed edges removed.
  - 1,004 zero-utility memories moved from LTM back to STM, with access counts reset to utility.
  - 7 hand-verified useful dormant memories restored.
  - LTM went from 1,473 to 375.
