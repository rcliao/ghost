---
name: ghost-hygiene
description: Review and curate ghost memories for quality. Run this to boost relevant memories, diminish noise, archive stale ones, create missing edges, and flag consolidation candidates. Intended to run periodically across any project.
allowed-tools: mcp__ghost__ghost_context, mcp__ghost__ghost_search, mcp__ghost__ghost_curate, mcp__ghost__ghost_edge, mcp__ghost__ghost_expand, mcp__ghost__ghost_consolidate, mcp__ghost__ghost_get, mcp__ghost__ghost_reflect
argument-hint: [focus-query]
---

# Ghost Memory Hygiene

You are performing a memory quality review for the ghost memory system.
This skill is designed to run periodically across any project — not just the current one.

**Namespace**: `agent:claude-code` (all memories live here).
**Focus**: If `$ARGUMENTS` is provided, focus the review on that topic. Otherwise do a general review.

## Key Principle: Use Tags, Not Importance, for Project Filtering

Memories have project tags (e.g. `project:ghost`, `project:zam`). The hooks use `-t project:<name>` to filter by project context at retrieval time. **Do NOT diminish a memory just because it's from a different project** — it may be valuable in its own project context. Instead:

- **diminish/archive**: Only for genuinely low-quality memories (stale, "session cut off", duplicates, placeholder content)
- **boost/promote**: For high-quality actionable knowledge regardless of project
- **Fix missing tags**: If a memory lacks a project tag but clearly belongs to one, note it for tagging

## Step 1: Pull Broad State

Pull memories using multiple approaches to get broad coverage (not just top-scored):

1. `ghost_search` with the focus query (or "recent sessions") — limit 30 — to see what search returns
2. `ghost_context` with budget=6000 — to see what context assembly selects
3. `ghost_expand` — to find consolidation candidates and cluster sizes

This gives visibility into 50+ memories instead of just the top 12.

## Step 2: Evaluate Each Memory

For every memory, assess:

1. **Quality**: Does it contain actionable knowledge, or is it a "session was cut off" / "no code written" placeholder?
2. **Freshness**: Is the information still accurate? Has the feature been completed, making planning memories obsolete?
3. **Uniqueness**: Is this a near-duplicate of another memory? (Check the clusters from expand)
4. **Tag correctness**: Does it have the right project tag? Is it missing one?

## Step 3: Curate

Apply using `ghost_curate`:

- **archive**: Session-cut-off placeholders, completed-work planning notes, stale obsolete info. Be aggressive here.
- **diminish**: Low-quality content that isn't worth archiving but shouldn't score highly.
- **boost**: High-quality actionable knowledge — debugging procedures, architecture decisions, gotchas that save time.
- **promote**: Memories proven useful across multiple sessions. Move tier up toward ltm.

**Priority: archive stale > boost quality > diminish noise.** Removing garbage has more impact than tweaking scores.

## Step 4: Create Typed Edges

The graph currently has almost no typed edges (only auto-generated `relates_to`). Look for:

- **contradicts**: Two memories say conflicting things. Critical — forces both into context so the agent sees the conflict.
- **depends_on**: Memory A requires understanding Memory B first. Helps with retrieval ordering.
- **refines**: Memory A is a more accurate/complete version of Memory B. Helps dedup scoring.

Use `ghost_edge` to create these. Skip `relates_to` — those are auto-generated on put.

## Step 5: Consolidate Large Clusters

Check `ghost_expand` clusters. For clusters with 5+ members:
1. Read a sample of the cluster members (use `ghost_get` on a few keys)
2. Write a concise summary capturing the key facts
3. Use `ghost_consolidate` to create the summary — children are auto-suppressed in future context

Focus on the largest clusters first — they waste the most token budget.

## Step 6: Report

Summarize:
- Total memories reviewed
- Actions taken: archived / diminished / boosted / promoted (with counts)
- Edges created (list type, from, to)
- Clusters consolidated (with member counts)
- Tag issues found
- Overall memory health observations and trends
