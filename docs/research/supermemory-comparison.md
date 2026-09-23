# Supermemory: what to steal, what to refuse

Reviewed `github.com/supermemoryai/supermemory` at `57b430b` (2026-09-18, MIT).
Citations are paths in that repo unless prefixed `ghost:`.
Judged against ghost's why: owned, encrypted, pluggable, fast, reliable, serves its human.

## What it is

The repo holds clients, not the engine.
Every code path calls `api.supermemory.ai` (`apps/mcp/src/server/client/index.ts:135`).
A self-hosted engine exists as a closed binary; it needs an LLM key and ships no MCP (`apps/docs/self-hosting/overview.mdx`).
The API reference has delete endpoints and no export endpoint.

So it is the thing ghost exists not to be: memory held by a vendor, with an LLM in every write.
Its value to us is the integration surface, which is where ghost is thinnest.
No reproducible benchmark is in the repo; our July verdict on that stands.

## The rule for sorting

Owner's ruling, 2026-09-23: ghost is a memory database.
Formatting memory for a prompt is the caller's job; `Context` exposes parameters so the caller can get what it needs and shape it.
Ghost's own ground is metadata that lets the right memory be retrieved, and the algorithm behind retrieval.
Everything below is sorted by that rule.

## Ghost's ground: metadata and algorithm

1. **A retrieval deadline the caller can set, failing open.**
   Supermemory times retrieval out at 5000 ms and proceeds with nothing (`packages/tools/src/vercel/index.ts:16,149,160`).
   Ghost's reranker already honours a context deadline and keeps partial results (`ghost:internal/store/search.go:1169`), but neither the CLI nor MCP lets a caller pass one.

2. **Expose the knobs on every surface.**
   `Context` has sixteen parameters; MCP exposes six of them; the CLI exposes `--budget` alone (`ghost:internal/cli/context.go`).
   The standalone hook uses the CLI, so it cannot set a floor, a spread or a scope.
   Supermemory's plugin defaults, `similarityThreshold 0.6` and `maxMemories 5`, are the kind of thing a caller should be able to set.

3. **Typed relations, and a way to fill them.**
   Supermemory has three relations: `updates`, `extends`, `derives` (`packages/memory-graph/src/api-types.ts:1`).
   Ghost has nine, but four have no rows in production (`ghost:internal/store/edge.go`).
   The pattern to keep is ghost's own `ghost_edge_candidates`: the store proposes pairs, the caller labels them, the store keeps the label.
   A one-time backfill through the existing out-of-band `infer-edges` path is a run, not a design change.

4. **Fact lifecycle as metadata, not deletion.**
   `isLatest`, `isForgotten`, `forgetAfter`, `forgetReason`, `parentMemoryId`; retrieval filters on the first two (`packages/memory-graph/src/types.ts:11-22`).
   Ghost has `supersedes`, `deleted_at`, `expires_at` and `valid_to`.
   Missing: a reason, and a reviewable "forgotten" state distinct from deleted.

5. **A review state that weights retrieval.**
   Low-confidence rows are down-weighted until approved or declined (`apps/docs/recall/memory-review.mdx`).
   Ghost marks them as `source_kind=observed` and the authority rule reserves the statement; a reviewed bit could feed scoring directly.

6. **Stable-versus-recent as a retrieval class.**
   Their profile splits `static` from `dynamic` facts and serves them by a separate call, 50 to 100 ms against 200 to 500 ms for search (`apps/docs/concepts/user-profiles.mdx`).
   For ghost this is a retrieval mode over pinned and recent memories, not a rendering.

7. **Destructive operations as dry-run, ids, apply**, capped and batch-tagged for undo (`apps/docs/recall/memory-operations.mdx:176-286`).
   Store API design, no formatting involved.
   Ghost's `rm --hard --all-versions` is unguarded (`ghost:internal/store/sqlite_crud.go:920`).

8. **A per-call retrieval cache** keyed by the normalised query (`packages/tools/src/shared/cache.ts`), so a tool loop does not re-run the full scan.

## The caller's ground

These are good ideas that belong in whatever calls ghost: the shell, or the reference hook scripts for standalone use.
They must not enter the store.

- **An owned, escape-proof context block** with strip-and-replace per turn (`packages/tools/src/shared/memory-context.ts:1-27`).
- **One renderer with a fact cap and a `+N more` line** (`apps/mcp/src/server/space-presentation.ts:89-101`).
- **Scope derived from git per call**: hashed common dir for the project, hashed user email for the person (`apps/docs/integrations/codex.mdx`).
  Ghost's `for_scope` parameter is the receiving end.
- **Signal-gated capture** with keyword triggers, tool-payload exclusion and `<private>` redaction.
- **Lean tool descriptions that say when not to fire.** This one sits on ghost's MCP surface, so it is ghost's to write, but it is guidance, not storage.

## Refuse

- **Deleting by similarity.** `add_memory action:"forget"` searches at 0.85 and deletes the top hit (`apps/mcp/src/server/client/index.ts:197-225`). Never in a user-owned file.
- **Hidden sticky scope.** The active space is server-side state per actor (`apps/mcp/src/server/server.ts:63`). Derive scope per call instead.
- **Asynchronous "dreaming".** Memories can appear after the write reports done, so a save is not immediately readable. Ghost is synchronous; keep it.
- **An LLM in every write**: extraction, relation typing, ingest filters, profile buckets.
- **Inferred `derives` memories.** They manufacture the uncertain rows the review queue then has to clean up. Keep the queue, skip the inference.
- **Rewriting the system prompt every turn** in query mode, which defeats provider prompt caching. Keep a stable profile block and put per-turn results elsewhere.

## Where ghost already leads

Reads see writes immediately.
Writes are compare-and-swap.
Provenance is typed (`stated`, `observed`, `self`, `peer`) where Supermemory has one `isInference` flag.
Corrections are reserved in context rather than merely ranked.
The store is one file the user holds, so "export" is a copy.
Its evals run in the repo.

## Not verified

The Claude Code, Codex and OpenCode plugins live in separate repos.
Their behaviour here is from Supermemory's docs, not their source.
Latency figures quoted in its docs (profile 50 to 100 ms, search 200 to 500 ms) are vendor claims.
