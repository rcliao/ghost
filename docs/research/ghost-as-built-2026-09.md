# Ghost as built, September 2026: our why and what

## What This Describes

Ghost at `main`, commit `69b58ee` (2026-08-17, PR #125).
Every citation points at that commit.

This doc re-grounds the project before the next expansion.
It states the intent, then walks what the code does today against it.

Deliberately not covered:

- The shell harness, which lives in a sibling repo and consumes ghost as a library.
- The eval harness, which `agent-memory-eval-suites-2026-08.md` already audits (merged as #129).
- Any proposal. The companion doc `ghost-expansion-research-2026-09.md` carries the how.

## Intent

The owner's why, in their own words from review:
ghost keeps the memory of an agent regardless of its shell, meaning the model and everything around it.

Memory is the most important part of a personal agent.
It is private, it is secure, and it drives the agent's behaviour by flowing into the system prompt.
So the memory, the ghost, must be carried across many shells.

Five terms in that statement are load-bearing:

- **Portable.** The memory outlives any one model or harness.
- **Private.** It is the owner's, held locally.
- **Secure.** What enters it and what changes it are controlled.
- **Behaviour-driving.** Memory reaches the model through the system prompt, so stale memory is wrong behaviour.
- **Personal.** One household, several people, one agent identity.

LLM-free follows from portability rather than standing beside it.
A memory that needs a particular model to be read or maintained cannot be carried to another.
The README states the constraint as "LLM-free by design" (`./README.md:5`).
The architecture doc assigns the intelligence to the caller (`docs/ARCHITECTURE.md:365`).

Three secondary reasons support the constraint.

- **Durability.** One file and one binary survive a provider that does not.
- **Latency and determinism.** Shell's per-turn Haiku classifier averaged 8.5s and matched nothing in 1,098 calls; its deterministic replacement peaked at 19ms.
- **No extraction hallucination.** Ghost has no extraction stage to hallucinate in.

The cost is specific.
The store cannot judge meaning, so curation falls to the caller or to nobody.
Gaps G1 to G5 below are instances of that cost.
G6 to G8 are ordinary engineering debt.

## Data Flow

### Write

1. A caller hands `Put` a text and its metadata (`internal/store/sqlite_crud.go:27`).
   Callers are the CLI, the MCP server, the library API, and `ghost capture`.
2. The capture extractor turns a transcript into candidates by regex cues and entity salience (`internal/capture/capture.go:64`).
   Those candidates enter the same `Put`.
3. The guard refuses a TTL on pinned or locked memories.
   It refuses overwriting a locked key without `Unlock` (`internal/store/sqlite_crud.go:38`).
4. The triage step looks for an exact or near-duplicate before any row is written (`internal/store/sqlite_crud.go:105`).
   On a hit it reinforces the existing memory instead (`internal/store/write_triage.go:32`).
5. The versioner checks `BaseVersion` against the head and rejects a stale writer (`internal/store/sqlite_crud.go:177`).
6. The versioner inserts the new row with provenance and soft-deletes every prior live version (`internal/store/sqlite_crud.go:185`).
7. The versioner moves the old version's access history onto the new row (`internal/store/sqlite_crud.go:227`).
8. The chunker splits the text and the embedder encodes each chunk in one batch (`internal/store/sqlite_crud.go:238`).
   An embedding failure is skipped silently; FTS5 still indexes the text.
9. After commit, the auto-linker compares the new memory against the 50 most recent and writes `relates_to` edges (`internal/store/edge.go:336`).
10. After commit, the freshness detector looks for a change cue plus a shared entity (`internal/store/freshness.go:36`).
    On a match it writes `contradicts` new-to-old and halves the old memory's importance.

Steps 9 and 10 run outside the transaction and swallow their errors.

### Read

1. A caller hands `Context` a query, a namespace and a token budget (`internal/store/context.go:164`).
2. The pin loader packs pinned memories into half the budget, ordered by importance (`internal/store/context.go:180`).
   Overflow is dropped with a one-shot warning.
3. The searcher pulls 50 candidates (`internal/store/context.go:256`).
   It fuses FTS5, LIKE and vector hits by reciprocal rank (`internal/store/search.go:506`).
4. The vector searcher scans every embedded chunk in the namespace by brute-force cosine (`internal/store/search.go:1664`).
   There is no index.
5. The reranker, when its model is on disk, rescores the pool with a local cross-encoder (`internal/store/search.go:1143`).
6. The scorer weighs relevance, recency, importance, kind and tier (`internal/store/context.go:941`).
   It multiplies by 1.8 for a matching `ForUser` or `ForScope` (`internal/store/context.go:97`).
7. The expander follows edges one hop from every seed, direction and priority set per relation (`internal/store/edge_policy.go:186`).
8. The authority rule reserves a person's own statement whenever an inference about them is in the pool (`internal/store/source_alias.go:183`).
9. The packer applies the score floor, hoists reserved memories up to a third of the budget, then packs greedily (`internal/store/context.go:534`).
10. The toucher bumps `access_count` for all 50 candidates from step 3, not for the packed set (`internal/store/context.go:678`).
11. The strengthener raises edge weights between packed pairs (`internal/store/edge.go:509`).
    It bumps `utility_count` only for packed direct hits.

Nothing is cached between calls.
The query embedding, the full cosine scan, fusion, reranking and expansion all recompute every time.

### Lifecycle

1. A caller or a schedule invokes `Reflect`, which evaluates rules in priority order (`internal/store/reflect.go:217`).
2. The promoter lifts an stm memory to ltm at `access_count` over 50, age over 72 hours, and three distinct access days (`internal/store/reflect.go:142`).
3. The demoter sends an ltm memory untouched for seven days to dormant.
   Ease, derived from `utility_count`, stretches that window (`internal/store/reflect.go:90`).
4. The merger links similar memories rather than merging them (`internal/store/reflect_merge.go`).
5. The cluster finder returns connected components over `relates_to` edges (`internal/store/edge.go:560`).
6. The calling agent consolidates a cluster into a summary with `contains` edges (`internal/store/sqlite_expand.go:218`).

## Data Model

Everything lives in one SQLite file.
The schema is created and migrated in one function (`internal/store/sqlite.go:126`).

```dbml
Table memories {  // sqlite.go:128 — produced by Write 6, read by Read 2-3
  id text [pk]              // ULID
  ns text                   // agent identity
  key text                  // (ns,key) is the logical key: indexed, NOT unique
  content text
  kind text                 // semantic | episodic | procedural
  tags text                 // JSON array in TEXT, convention only
  version int
  supersedes text           // previous version id, no FK
  deleted_at text           // soft delete; head = live row, highest version
  tier text                 // sensory | stm | ltm | dormant
  pinned int
  importance real
  access_count int          // candidacy counter, see Known Gaps G1
  utility_count int         // packed-direct-hit counter
  ease real
  expires_at text
  valid_from text           // bitemporal
  valid_to text
  source_user text          // who said it
  source_kind text          // stated | observed | self | peer
  source_scope text         // where it was born
}

Table chunks {  // sqlite.go:151 — Write 8
  id text [pk]
  memory_id text [ref: > memories.id]   // enforced
  seq int
  text text
  embedding text            // declared TEXT, holds a binary blob
}

Table memory_edges {  // sqlite.go:362 — Write 9-10, Read 7 and 11
  from_id text [ref: > memories.id]     // enforced
  to_id text [ref: > memories.id]       // enforced
  rel text                  // 9 relations; 4 have never been created in production
  weight real               // raw cosine for auto-links
  access_count int
  // pk (from_id, to_id, rel)
}

Table memory_accesses {  // sqlite.go:348 — Read 10, Lifecycle 2
  memory_id text            // no FK; rows re-pointed across versions
  accessed_at text
}

Table source_aliases {  // sqlite.go:209 — Read 6
  ns text
  alias text                // pk (ns, alias), case-insensitive
  canonical text
}

Table reflect_rules {  // sqlite.go:221 — Lifecycle 1
  id text [pk]
  cond_tier text
  cond_access_gt int
  cond_utility_lt real
  action_op text
}

// Table memory_outcomes {   // DOES NOT EXIST
//   memory_id, context_call_id, was_packed, was_used
// }
// Nothing records whether a packed memory helped.
```

`chunks_fts` is an FTS5 table kept in step by triggers (`internal/store/sqlite.go:253`).
`memory_links` is legacy and migrated into `memory_edges`.
`memory_files` is declared twice (`internal/store/sqlite.go:171`, `:243`).

One entity carries two jobs.
`access_count` is both the rehearsal signal that prevents decay and the evidence that earns promotion.
Candidacy feeds both.

## What Persists Where

- **The database**: `--db`, then `$GHOST_DB`, then `~/.ghost/memory.db` (`internal/cli/root.go:73`). WAL mode. Nothing is invalidated; versions soft-delete.
- **Model files**: about 86MB under `~/.ghost/models/`, downloaded on first use (`internal/embedding/embedding.go:219`).
- **Hook scratch**: a prompt buffer and session-keys file under `/tmp` (`hooks/ghost-user-prompt.sh:32`). Lost on reboot.

Not persisted:

- No query, embedding or result cache exists in the running store.
- Stage counters and swallowed-error counts live in memory and print only under `GHOST_DIAG=1` (`internal/store/diagnostics.go:29`).
- No record survives of which memories a given `Context` call packed.

In production the single-file claim is weaker than it reads.
Each shell daemon keeps its own database, separate from the MCP and CLI default.
One agent's memories are split across two files.

## Known Gaps

All gaps are unowned unless marked.

### Intent gaps

- **G1. Promotion rewards candidacy.**
  Step 10 touches the whole 50-wide pool (`internal/store/context.go:678`).
  The promotion rule reads only `access_count` (`internal/store/reflect.go:142`).
  Measured: 76% of pika's writes never matched a query, yet 46% reached ltm.
- **G2. Dormant means invisible.**
  Search excludes dormant by default (`internal/store/search.go:426`).
  A correct allergy fact decayed out of view; with `IncludeAll` it ranked third, above the wrong one.
- **G3. Clusters are a hairball.**
  Connected components chain into one 1,311-member cluster at 1.2% density (`internal/store/edge.go:560`).
  Consolidation cannot start from it.
- **G4. Corrections need a cue phrase.**
  Freshness fires only on a change-cue regex (`internal/store/freshness.go:31`).
  A cue-less correction under a new key links as `relates_to`.
  For a pinned target the detector returns before writing the edge (`internal/store/freshness.go:114`).
  A corrected pin gets no demotion and no link to its correction.
- **G5. The typed graph is empty.**
  About 77,000 of 80,000 production edges are `relates_to`.
  `depends_on`, `prevents`, `caused_by` and `implies` have zero rows.
  Their only creator is an LLM-assisted CLI that is never run (`internal/cli/infer_edges.go:20`).
  Owner: the typed relations are new and were never backfilled; a one-time out-of-band classifier pass is the only route.
### Engineering debt

- **G6. Vector search is a full scan.**
  No index exists (`internal/store/search.go:1664`).
  The largest eval corpus is about 547 memories; live stores exceed 8,000.
  Owner-approved for a build: add an index.
- **G7. The clock leaks.**
  `touchMemories` stamps `time.Now()`, not the injectable clock (`internal/store/sqlite_util.go:116`).
  Reflect measures idle time from the injectable clock (`internal/store/reflect.go:290`).
  Under the frozen eval clock a touched memory's idle hours go negative, so it cannot idle-demote.
  The effect on lifecycle eval verdicts is not measured.
- **G8. Docs drift.**
  The architecture doc says 13,500 LOC and 10 MCP tools (`docs/ARCHITECTURE.md:37`, `:50`).
  Measured: 17,882 and 11.
  Five doc-sync PRs are open and unmerged (#118, #123, #126, #127, #128).

### Accepted, not a gap

- An LLM client that execs `claude` lives in `internal/store/e2e_bench.go:1045`.
  Owner: eval code is exempt where it tests LLM integration.

## Open Questions

- The why above appears in no repo doc. Should a version of it open the README?
- Is the two-database split a ghost gap or purely a shell deployment choice?
- The eval set passes 52 and skips 21, all embedded or external. Does that skip list block trusting any lifecycle change?
