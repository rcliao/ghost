# What makes a good agent memory, judged against ghost's why

## Research Question

Ghost's why, in the owner's terms: memory is your own file, not a cloud vendor's.
Any shell plugs in and pulls from it, one after another.
It is fast, reliable infrastructure that helps an agent serve its human.
How does ghost measure up?

- Q1. Owned: what makes memory the user's own, and what lock-in does it avoid?
- Q2. Encrypted: what transparent at-rest options exist without CGo?
- Q3. Pluggable: what can a store rely on across harnesses, standing alone?
- Q4. Fast: how fast is ghost on a real store?
- Q5. Reliable, with proper write and retrieval: does it serve its human?

## Summary

Ghost is owned in the sense that matters: one readable SQLite file, no telemetry, no vendor.
Vendor memory is cloud-held and exports only as lossy prose.

Pluggability is closer than the repo suggests.
Every major harness speaks MCP, and Codex shares Claude Code's hook names and injection field.
Ghost's hooks are Claude-only scripts.

Fast is where ghost fails its why today.
On a 13,365-memory store, search takes 120 ms without embeddings and 4.8 s once the default reranker self-enables.
In production about one turn in four waited over 2 s for memory.

Encryption has one CGo-free route, and it means changing the SQLite driver.
Retrieval on the standalone path returns mostly unrelated memories, and the score floor cannot separate them.

## Findings

### F1 — owned, and the lock-in is real [Q1]

ChatGPT, Claude and Gemini hold memory in their clouds.
Claude's export is asking it to write its memories out verbatim; its import is pasted text, labelled experimental.
Harness memory is local but harness-private: Claude Code and Codex each keep their own markdown directory.

Ghost is one SQLite file with a plain schema and no telemetry.
One weakness: no chunk records which embedding model produced it (`internal/store/sqlite.go:158`).

### F2 — encryption without CGo means a new driver [Q2]

`modernc.org/sqlite` has no encryption surface and only a read-only VFS.
SQLCipher and SEE both need CGo.
`ncruces/go-sqlite3` is the one CGo-free transparent option, through its Adiantum VFS with Argon2id keys.

Encryption is fully deterministic, so multiple snapshots reveal which blocks changed.
It claims no protection against tampering.
FTS5 becomes opt-in per connection.

FileVault already covers a removed or locked disk, and nothing else.
Ghost encrypts nothing and creates its directory world-readable (`internal/store/sqlite.go:59`).

### F3 — the common plug is MCP plus four shared hooks [Q3]

MCP over stdio works in Claude Code, Codex, Gemini CLI, Cursor and opencode.
Codex documents `SessionStart`, `UserPromptSubmit`, `Stop` and `PreCompact`, injecting through `additionalContext`.
Those are Claude Code's names and shape.

Ghost does not use this.
Its hooks are five Claude-specific shell scripts, and capture pipes transcripts to `claude -p` (`hooks/ghost-stop.sh:100`).
`Context` returns JSON, so each hook re-renders it with `jq` (`hooks/ghost-session-start.sh:43`).

### F4 — fast only with the models off [Q4]

Measured on a snapshot of the production store: 13,365 memories, 70,006 embedded chunks.

- Search p50 is 120 ms with FTS alone, and 403 ms with embeddings.
- It is 4.8 s with the pure-Go reranker, which a plain `go install` builds.
- Production uses an ONNX backend; 47 of 183 turns in eight days still waited over 2 s.

Vector search scans every chunk; no index exists (`internal/store/search.go:1664`).
Its three search channels run in sequence on every call (`internal/store/search.go:464`).

### F5 — the standalone path runs untuned and fails quietly [Q5]

The per-prompt hook is a bare `ghost context --budget 1500` (`hooks/ghost-user-prompt.sh:44`).
It sets no score floor or scope, unlike the production shell.
The reranker turns itself on when its model is on disk (`internal/embedding/reranker.go:192`).
One query took 6.66 s with it and 0.31 s without.

Failures stay silent.
Post-commit linking swallows its errors, and the eval set runs with embeddings off (`internal/store/sqlite_test.go:19`).

### F6 — retrieval does not yet serve the human [Q5]

A prompt about the comments tool returned five memories; one concerned that tool.
All five scored 0.30 to 0.40; on another prompt the relevant memory scored 0.79.
A floor of 0.3 removed none of them.

Memory must also be current to help.
Promotion rewards candidacy, not use (`internal/store/context.go:678`).
Dormant memories are filtered before ranking (`internal/store/search.go:426`).
A contradicted pin gets no `contradicts` edge (`internal/store/freshness.go:114`).

## Code References

- `hooks/ghost-user-prompt.sh:44` — the untuned standalone retrieval call
- `internal/embedding/reranker.go:192` — reranker enables itself in auto mode
- `internal/store/search.go:1664` — full vector scan, no index
- `hooks/ghost-stop.sh:100` — capture depends on the `claude` CLI
- `internal/store/sqlite.go:158` — embeddings carry no model identity

## Open Questions

- Do these findings justify a plan, or is fixing the standalone defaults enough for now?
- Is a SQLite driver change acceptable for encryption, or is FileVault plus file permissions sufficient?
- Should ghost ship one `ghost hook <event>` command for all harnesses, replacing the shell scripts?
- What latency budget should a per-prompt memory call have?
