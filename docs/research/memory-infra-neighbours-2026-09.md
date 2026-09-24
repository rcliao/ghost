# Memory-infra neighbours: what ghost can learn next, September 2026

## Research Question

The owner wants ghost to be memory infrastructure: SQLite for memory, with a graph, deterministic algorithms with knobs an LLM can turn.
Which existing tools already solve pieces of that, and which pieces are worth taking?

- Q1. Which LLM-free or LLM-optional agent-memory stores do something ghost does not?
- Q2. Which embedded databases offer a primitive ghost lacks, and at what cost in Go?
- Q3. Which deterministic memory algorithms exist as maintained Go libraries?
- Q4. How do neighbours expose knobs, audit and latency, and what could ghost adopt without a dependency?

## Summary

Two stated gaps close with pure-Go code ghost's own driver already ships or that exists as a small library.
Vector search: `modernc.org/sqlite/vec` bundles sqlite-vec since driver v1.47.0; ghost pins v1.45.0.
Principled decay: `go-fsrs` implements FSRS v6 in pure Go under MIT.
Clustering: gonum's Louvain takes a resolution parameter and needs no CGo.

Encryption at rest still means a driver swap; nothing on the shortlist changes that.
The embedded graph and bitemporal databases are references, not dependencies: Kùzu is archived, CozoDB idle, Dolt and XTDB are CGo or JVM.

The design ideas worth taking need no dependency at all.
They are write-back modes with a ledger, hash-chained audit, superseded rows that stay findable, per-kind half-lives, and an access gate before ranking.
Almost nobody publishes latency.

## Findings

### F1 — small stores carry the governance ideas [Q1]

GoodMemory writes back in four modes: `off`, `observe`, `review`, `selective`.
In `review` mode nothing durable is written until an operator approves.
Memory the assistant originated is blocked unless the host confirms it.
Its ledger records candidates, reasons, host and mode; export carries a SHA-256 manifest.

Other stores add one idea each.
Per-kind half-lives (decisions 90d, bugfixes 14d); an access gate "before ranking"; superseded entries that "stay findable" with a note on why.
Ghost already has the review trace these imply; it lacks write-back modes and a signed export.

### F2 — the vector gap is a version bump, not a new index [Q2]

`modernc.org/sqlite/vec` installs sqlite-vec 0.1.9 through `sqlite3_auto_extension`, transpiled, no CGo.
Ghost's `go.mod` pins v1.45.0 (`go.mod:11`); the package first shipped in v1.47.0.

Two caveats.
sqlite-vec is "pre-v1, so expect breaking changes".
Whether `vec0` is brute-force or indexed is not stated in its README or KNN docs; the gain may be a maintained, quantized scan rather than an index.
Ghost's own scan is `internal/store/search.go:1664`.

### F3 — graph and bitemporal databases are references only [Q2]

Kùzu's repository "was archived by the owner on Oct 10, 2025".
CozoDB has no commit since late 2024 and reaches Go through a C API.
Dolt needs CGo at about 100 MB; XTDB is JVM.

What to take is the query shape, not the engine.
Dolt's `dolt_history_<table>` yields "a row for every revision of a row"; ghost has versions and `valid_from`/`valid_to` (`internal/store/sqlite.go:342`) and no history query over them.
CozoDB's Datalog rules over a graph are what ghost's pair rules approximate in struct form.

### F4 — decay and clustering exist as pure-Go libraries [Q3]

`open-spaced-repetition/go-fsrs`: FSRS v6, `Weights [21]float64`, `Next`, `Retrievability`, `Forget`, MIT.
Its input is a graded recall; ghost's `utility_count` and the review verdicts are the closest graded signal (`internal/store/reflect.go:90`).

`gonum.org/v1/gonum/graph/community`: `Modularize(g, resolution, src)` is Louvain over weighted graphs, BSD-3.
Ghost's clusters are connected components (`internal/store/edge.go:560`).

### F5 — knobs and latency claims are rare [Q4]

Knobs seen: write-back mode, decay mode, pin, hard expiry, per-kind half-life.
Latency: two tools publish a number (22 ms, 82 ms); one refuses a p99 outright.
Ghost publishes none; its measured search p50 is 120 ms without embeddings and 4.8 s once the reranker self-enables.
Being the store that states a per-call budget and honours it would be a distinction, not a catch-up.

### F6 — encryption is unchanged by this sweep [Q2]

`modernc.org/sqlite` has no codec.
The pure-Go route remains `ncruces/go-sqlite3` with its Adiantum VFS, which "does not claim protect databases against tampering".
A newer layered driver exists but is four months old with a dozen stars.
Both are a driver migration.

## Code References

- `go.mod:11` — driver pinned below the version that bundles sqlite-vec
- `internal/store/search.go:1664` — brute-force vector scan
- `internal/store/reflect.go:90` — ease from utility, the hook for FSRS
- `internal/store/edge.go:560` — connected-components clustering
- `internal/store/sqlite.go:342` — bitemporal columns with no history query

## Open Questions

- Do these findings justify a plan, or is the driver bump alone the right next step?
- Is a write-back mode (`observe` / `review` / `selective`) in scope for the store, or the caller's job?
- Should ghost adopt FSRS for tier decay, given it needs a graded signal the review trace only partly provides?
- Is Louvain worth a gonum dependency, or should clustering wait for a graded fixture?
