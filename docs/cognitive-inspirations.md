# Cognitive Science Inspirations

The science behind ghost's memory design — what we borrowed, what we adapted, and where we intentionally diverge.

---

## Tulving's Memory Taxonomy → Memory Kinds

Endel Tulving (1972) distinguished three types of long-term memory:

- **Semantic** — general world knowledge, facts, concepts ("I know that Paris is a capital")
- **Episodic** — autobiographical events with temporal context ("I remember visiting Paris last summer")
- **Procedural** — skills, routines, how-to knowledge ("I know how to ride a bike")

Ghost adopts these directly as `kind`: `semantic`, `episodic`, `procedural`.

Kinds now influence retrieval scoring through **kind-specific weights**. Episodic memories weight recency higher (0.30 vs 0.05), matching Tulving's observation that episodic retrieval is temporally-indexed. Procedural memories weight access frequency higher (0.35 vs 0.15), reflecting that well-practiced procedures should surface more readily. Semantic memories weight relevance highest (0.40), prioritizing factual accuracy over timing.

Kind defaults are also tier-aware: `sensory` and `stm` tier memories default to `episodic` (new observations are events), while `ltm` and `identity` tier memories default to `semantic` (proven knowledge is factual). Explicit kind always overrides the default.

**What we left out:** Tulving later expanded to five systems (adding priming and perceptual memory). These don't map well to text-based agent memory, so we stopped at three.

---

## Atkinson-Shiffrin Model → Memory Tiers

The multi-store model (Atkinson & Shiffrin, 1968) proposes three sequential stores:

```
Sensory → Short-Term Memory → Long-Term Memory
              (limited,            (unlimited,
               decays fast)         durable)
```

Information flows from sensory to STM via _attention_, and from STM to LTM via _rehearsal_. Without rehearsal, STM contents decay.

Ghost's tier system is a direct adaptation:

| Cognitive model | Ghost tier | Behavior |
|----------------|-----------|----------|
| Sensory register | `sensory` | Ultra-short-lived buffer. Promoted to STM if attended (accessed >1 time), deleted after 4h otherwise |
| Short-term memory | `stm` | Default tier. Subject to importance decay |
| Long-term memory | `ltm` | Promoted from STM after repeated access (the "rehearsal" analog) |
| — | `pinned` | No direct analog. Functions like _self-schema_ — chronically accessible core knowledge. Replaces the old `identity` tier. Exempt from all lifecycle rules |
| — | `dormant` | Closest to "forgotten but recoverable" — Ebbinghaus showed even forgotten material has "savings" on relearning |

**What we adapted:** The original model has no "identity" tier. We added it because agents need permanently pinned knowledge (role, core instructions) that should never decay. Cognitively, this resembles _autobiographical self-knowledge_ — the facts about yourself you never forget.

**Sensory tier:** We initially left out sensory memory, reasoning that the LLM's context window fills this role. However, we found that raw context window observations (conversation exchanges, transient inputs) benefit from a brief buffer stage before committing to STM. The `sensory` tier implements this: memories enter as sensory, and the reflect system either promotes them to STM (if accessed/attended) or deletes them (if ignored after 4 hours). This prevents STM from accumulating low-value observations that were never revisited.

---

## Ebbinghaus Forgetting Curve → Importance Decay

Hermann Ebbinghaus (1885) discovered that memory retention decays exponentially:

```
R = e^(-t/S)
```

Where R is retention, t is time, S is memory strength. Key findings:
- ~50% lost within 1 hour, ~70% within 24 hours
- Each rehearsal strengthens the memory and slows future decay
- Meaningful material decays slower than meaningless material

Ghost uses this in two places:

1. **Context scoring** applies recency decay: `recency = e^(-0.1 × age_days)` — a 7-day half-life. Recent memories score higher.

2. **Reflect rules** decay importance for unaccessed STM memories: `importance *= 0.95` per cycle. The minimum floor of 0.1 prevents complete forgetting — inspired by Ebbinghaus's "savings" effect (even forgotten material is faster to relearn).

**Where we diverge:** Ebbinghaus showed that _each repetition lengthens the decay interval_. Ghost's decay rate is fixed — a memory accessed 10 times decays at the same rate as one accessed 4 times. We use access count for _promotion_ decisions instead (>3 accesses → promote to LTM), which is a coarser but simpler approximation.

---

## Spaced Repetition → Access-Based Promotion

The spaced repetition effect (Ebbinghaus, 1885; Leitner, 1972) shows that reviewing material at expanding intervals produces dramatically better retention. The testing effect (Roediger & Karpicke, 2006) adds that _active retrieval_ strengthens memory more than passive re-exposure.

Ghost's promotion rule (`sys-promote-to-ltm`) is inspired by this: if a memory has been accessed 3+ times over 24+ hours, it has proven its value through repeated retrieval and earns promotion to LTM.

The `utility_count` field goes further than classical spaced repetition — it tracks not just _whether_ a memory was retrieved, but whether it was _useful_. This maps to the testing effect's insight that successful, purposeful recall strengthens memory more than rote retrieval. The utility ratio (`utility_count / access_count`) lets the reflect system prune memories that are frequently surfaced but rarely helpful.

**Where we simplify:** True SRS (like Anki) schedules proactive reviews at expanding intervals. Ghost is purely reactive — memories are only accessed when a search or context query surfaces them. We don't schedule reviews because ghost is a tool, not a tutor.

---

## Memory Consolidation → The Reflect System

In neuroscience, consolidation is how fragile short-term memories become stable long-term ones. Two stages:

1. **Synaptic consolidation** (hours) — strengthens neural connections
2. **Systems consolidation** (weeks–years) — transfers memories from hippocampus to neocortex

Sleep plays a critical role: slow oscillations coordinate the transfer. The brain consolidates _during quiet periods_, not during active processing.

Ghost's `reflect` command is the consolidation analog. It evaluates rule-based conditions and applies tier transitions:

```
sensory (attended)          → STM         [attentional selection]
sensory (unattended, >4h)   → deleted     [sensory trace decay]
STM (unaccessed, decaying)  → dormant     [synaptic trace weakening]
STM (repeatedly accessed)   → LTM         [hippocampal-to-cortical transfer]
STM (similar memories)      → merged      [memory consolidation / deduplication]
LTM (stale, unused)         → dormant     [cortical trace weakening]
Low utility                 → deleted     [synaptic pruning]
```

**Narrowing the gap:** Brain consolidation _transforms_ memories — abstracting, generalizing, and integrating them with existing knowledge. Ghost's reflect primarily changes metadata (tier, importance), but the **similarity merge** feature begins to bridge this gap. The `MERGE` action uses embedding cosine similarity to find semantically overlapping memories and consolidate them — the survivor inherits the combined access history and tags from absorbed memories. This is a structural form of consolidation (deduplication), not yet semantic synthesis. Park et al.'s Generative Agents (2023) implement the full version with a "reflection" mechanism that generates higher-level insights from raw memories — ghost's `MERGE` action supports a `strategy` field that could adopt LLM-based synthesis (`llm_synthesize`) in the future.

**Another divergence:** Consolidation happens during sleep — a quiet period. Ghost's reflect runs on-demand, regardless of agent activity. There's no concept of "idle-time processing."

---

## Levels of Processing → Importance at Write Time

Craik & Lockhart (1972) proposed that memory durability depends on _how deeply_ information is processed during encoding:

- **Shallow** — surface features (what does it look like?)
- **Deep** — meaning, associations, implications (what does it mean?)

Deeper processing → more durable memory traces.

Ghost approximates this with the `importance` field (0.0–1.0), set at write time. The agent decides how important a memory is when storing it — a rough proxy for encoding depth. High-importance memories resist decay and rank higher in context assembly.

**Where we simplify:** Ghost relies on the _caller_ to judge importance. The original theory is about the encoding _process_ itself, not a label. We chose explicit over implicit — the agent declares importance rather than ghost trying to infer it.

---

## Spreading Activation → Edge Expansion

Collins & Loftus (1975) proposed that semantic memory is organized as a network where concepts are linked by associations. Recalling one concept spreads _activation_ along its links to related concepts, making them easier to retrieve. Key properties:

- **Strength-weighted** — stronger associations spread more activation
- **Decay with distance** — activation diminishes as it spreads further from the source
- **Accumulation** — a concept receiving activation from multiple sources is even more accessible

Ghost implements this through **edge expansion** in context assembly (Phase 3). When search finds seed memories, activation spreads along `memory_edges` to their neighbors:

```
propagated_score = seed_score × edge_weight × damping (0.3)
```

Properties mapped to ghost:

| Cognitive property | Ghost implementation |
|-------------------|---------------------|
| Association strength | Edge `weight` (0.0–1.0), strengthened by co-retrieval |
| Decay with distance | `damping` factor (0.3), single-hop only |
| Accumulation | Additive boost when memory appears as both direct hit and neighbor |
| Association types | Typed edges: `relates_to`, `contradicts`, `depends_on`, `refines`, `contains` |

**Hebbian learning** ("neurons that fire together wire together") is also implemented: when two connected memories appear together in a `ghost_context` response, their edge weight increases via `weight += 0.05 × (1 - weight)` — diminishing returns asymptotically approaching 1.0.

**Contradicts edges** implement a cognitive safety mechanism: conflicting information is force-surfaced (80% of seed score) because agents, like humans, need to see contradictions to make informed decisions.

**Containment suppression** mirrors hierarchical memory organization: when a summary (parent) is present, its constituent details (children) are suppressed — similar to how accessing a schema or gist inhibits recall of specific instances (schema-consistent memory suppression, Anderson & Neely, 1996).

**Where we diverge:** Human spreading activation is multi-hop — activation can propagate through several links with decreasing strength. Ghost currently limits expansion to single-hop for performance and simplicity. Cognitive science supports multi-hop (2–3 hops with heavy decay), which could be a future enhancement.

**Where we simplify:** Real semantic networks have graded, continuous activation over time. Ghost computes activation in a single pass during context assembly — there's no persistent activation state between queries.

---

## Influences from Recent Agent Memory Research

### Park et al. — Generative Agents (2023)

Their retrieval scoring formula influenced ghost's context assembly:

| Park et al. | Ghost |
|------------|-------|
| Recency (1/3) | Recency (0.2) |
| Importance (1/3) | Importance (0.2) |
| Relevance (1/3) | Relevance (0.4) |
| — | Access frequency (0.2) |

We weight relevance higher (0.4 vs 1/3) because ghost serves task-oriented agents, not social simulation. We added access frequency as a fourth signal — absent in Park et al. — because repeated retrieval is a strong signal for agent memory.

### MemGPT / Letta (2023)

MemGPT's insight: treat the LLM context window as constrained RAM, with tiered overflow to external storage. Ghost's two-phase context assembly (pinned tiers first, then search) mirrors this: `identity` and `ltm` are "core memory" (always loaded), while `stm` and search results are "archival memory" (loaded on demand within budget).

### ReMe (2024)

ReMe's utility-based evaluation inspired ghost's `utility_count` / `access_count` ratio. The idea: track not just _how often_ a memory is retrieved, but _whether it actually helped_. This enables principled pruning of high-access, low-value memories.

---

## Summary: What We Borrowed vs. Where We Diverge

| Inspiration | What ghost does | Where ghost diverges |
|------------|----------------|---------------------|
| Tulving's taxonomy | 3 kinds: semantic, episodic, procedural | Kind-specific retrieval weights (episodic→recency, procedural→access, semantic→relevance) |
| Atkinson-Shiffrin | 5 tiers: sensory → stm → ltm → identity → dormant | Added identity tier (no cognitive analog); sensory tier added for raw observations |
| Ebbinghaus decay | Exponential recency scoring, importance decay with minimum floor | Fixed decay rate — doesn't lengthen intervals after rehearsal |
| Spaced repetition | Access-count-based promotion to LTM | Reactive only — no proactive review scheduling |
| Consolidation | Reflect system with rule-based tier transitions; `consolidate` command for hierarchical summaries | No automatic content transformation — summary text provided by caller |
| Levels of processing | Explicit importance at write time | Caller-declared, not encoding-depth-inferred |
| Spreading activation | Edge expansion in Phase 3, Hebbian co-retrieval strengthening | Single-hop only, no persistent activation state |
| Park et al. | 4-factor retrieval scoring | Higher relevance weight, added access frequency |
| MemGPT | Pinned tiers + search-based overflow | No self-editing — agent must explicitly store/update |
| ReMe | Utility ratio for pruning | Explicit utility-inc, not automatically inferred |

The unifying design choice: **ghost stays mechanical**. No LLM calls inside the library. Decay, promotion, and pruning follow deterministic rules. The intelligence lives in the calling agent — ghost is the storage and retrieval layer that makes that intelligence persistent.
