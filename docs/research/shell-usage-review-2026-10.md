# Ghost in shell: is it helping? (review, 2026-10-18)

Checklist
- [x] Snapshot both agent stores + shell.db (VACUUM INTO, scratchpad; live DBs read-only)
- [x] Deploy state: which ghost the agents actually run
- [x] Recall: judge sampled ledger turns, three-way split (write / lifecycle / retrieval gap)
- [x] Write: unclaimed_trigger + verbal_save turns, did the fact land and is it retrievable
- [x] Constraints, corrections and dormancy (umbreon 96% dormant in its own namespace)
- [x] Ranked ghost-side proposals (deterministic, knob or metadata only); shell-side items listed separately
- [x] Fresh-context check of claims (6 blocking issues found and fixed: namespace-mixed counts, W1 location, count mismatches, approval labels, a privacy-revealing key)

Privacy: this file paraphrases. Verbatim turns, chat ids and memory content stay in the
session scratchpad, not in the repo.

## Method

Window: last 30 days (2026-09-18 → 2026-10-18). Sources per agent (pikamini, umbreonmini):
`shell.db` (`messages`, `recall_verifications`, `write_verifications`, `tool_uses`) and the
daemon's `memory.db`. Shell's recall ledger is a keyword heuristic
(`shell/internal/bridge/recall_verify.go:140`, `injectionCoversQuery` = token containment), so
its verdicts are an index into turns, not the finding. Each sampled turn is judged by reading the
reply, then searched for in the store with dormant rows included:
nothing stored = write gap; stored but dormant = lifecycle gap; stored, live, not used = retrieval gap.

## Baseline numbers (30 days)

| | pikamini | umbreonmini |
|---|---|---|
| user / assistant messages | 3,233 / 1,410 | 2,591 / 1,323 |
| recall-trigger turns logged | 183 | 188 |
| ledger "grounded via injection" | 149 | 139 |
| ledger "grounded via active read" | 15 | 26 |
| ledger "inject_irrelevant" | 19 | 23 |
| write turns verified | ~330 | ~110 |
| write trigger, no claim, no write | 82 | 12 |
| verbal save, no write | 5 | 11 |
| agent's own namespace: ltm / stm / dormant | 5,105 / 981 / 4,399 | 68 / 120 / 4,825 |
| new rows in own namespace, window | 1,346 | 404 |
| rows in the same file under `agent:claude-code` (window) | 3,723 (1,165) | 3,239 (1,053) |

Explicit ghost tool calls by pikamini (all time, `tool_uses`): put 1,276, search 566, get 80,
patch 73, curate 59, edge 19, consolidate 9, context 3.

## Deploy state: the agents run a September ghost

- `shell/shell` was built 2026-10-17 11:41 with `replace => ../ghost`, but the `../ghost` checkout
  sat at `8cfd922` (2026-09-26) until a pull on 2026-10-18 10:16 (reflog). The binary contains no
  `used_count` or `GHOST_EDGE_MIN_SCORE` strings, and neither live `memory.db` has the `used_count`
  column, which `migrate()` would add.
- So #136–#142 (via trace, used_count, edge A/B knobs, edge-passenger floor fix) are merged but
  not live in either agent. The PATH `ghost` binary (MCP tools) is from 2026-09-26 too.
- Consequence: production has no outcome signal. Nothing calls `curate op=used`, and the column
  does not exist yet.

## Speed and reliability

Per-turn injection (`memory inject` lines in `daemon.log`, since 2026-09-18):

| | n | p50 | p90 | p99 | max | avg injected |
|---|---|---|---|---|---|---|
| pikamini | 3,305 | 1.05 s | 2.14 s | 3.37 s | 6.45 s | 9.0 KB |
| umbreonmini | 2,663 | 0.67 s | 1.90 s | 3.21 s | 5.00 s | 7.0 KB |

Agent-called `ghost_search` has hit 12–15 s ("tool slow" log lines). 3 injections failed outright
(`load pinned tiers: context canceled`).

Writes that failed (30 days, from the agents' session transcripts):
- `ghost_put` failed 25 of 579 calls. 22 were `ns, key, and content are required`: the agent
  left out `ns` (or `key`). Shell's per-agent `mcp.json` sets `GHOST_NS=agent:<name>`, but ghost
  never reads that variable outside two test files, so it is a dead setting. Only 9
  of the 25 were retried successfully within the next two calls, so about 16 intended memories
  were lost.
- `ghost_patch` failed 8 of 24: two for the same missing `ns`, the rest hunk context mismatches
  against long memories and patches sent as JSON instead of a unified diff.
- Two `version conflict` errors on put (the agent re-put a key already at v2).
- Daemon-side: 6 `log exchange` writes lost to `SQLITE_BUSY`, 13 `media-note` writes lost to a
  `UNIQUE(memory_id, path)` violation on `memory_files`. Cause is in ghost:
  `internal/store/sqlite_crud.go:283` inserts each `PutParams.Files` entry with a plain INSERT,
  so a Put that lists the same path twice fails the whole write, memory included.

## Findings

### Summary

Ghost reliably delivers the pinned layer. Allergies and house rules sit in the system prompt and
the agents respect them. It does poorly at the part it exists for: bringing back the right
non-pinned fact when it is needed. In 30 sampled turns the ledger graded "grounded by
injection", a remembered fact was injected *and used* once. The agents compensate around
ghost: Notion and project docs answer recall questions, and pikamini queries `memory.db` with raw
`sqlite3 … LIKE` (13 times in 30 days). The causes, in order of impact: (L) lifecycle
rules hide good facts, (N) a third of each agent's memory file is in a namespace the agent never reads, (W) writes are lost or overwritten, (R) automated rows and the chat filter
crowd out distilled facts, and (D) the fixes already merged are not deployed.

### R. Recall

- **Shell's recall ledger cannot tell help from noise.** About 80 of 149 (pika) and 65 of 139 (umbreon)
  "grounded via injection" verdicts are scheduled prompts, not user questions. In an
  interactive-only sample of 30, 26 turns were not recall questions at all; 3 of the 4 real ones
  were false-green. On the miss side, 40 of the 41 judged "inject_irrelevant" flags (42 logged) are classifier false
  positives or correct answers. The ledger's ~85% "grounded" figure carries no information.
- **Superseded value given, correct one dormant.** Asked for a recipe ratio, umbreon gave the old
  value. The update (`joyoung-soymilk-baseline-2026-09-29`, 270 accesses) was demoted exactly 7
  days after it was written (reflect at 2026-10-06 09:08, `demoted=3`). Pikamini answered
  correctly from its own store. Revived on a copy, the row ranks #6.
- **Brand-name fact out-ranked by a word match.** "Add the snack I had before": the ledger
  called it grounded because a different product shared a generic word. The right fact (a
  brand-named snack) is live in ltm but has no chat tag. Shell's
  chat call gets 2/3 of the budget and uses `TagMode: "filter"`
  (`shell/internal/memory/memory.go:506`), so untagged facts compete only for the 1/3 cross-chat
  budget. The full-sentence query missed; the brand name alone ranked it #1. The agent recovered
  with raw SQL.
- **Automated rows dominate what is injected.** Umbreon: media-notes are 35–40% of logged direct
  hits, and keys that are literal user sentences another 12–23%. Three plant-photo notes were each
  hit in about 80 turns on unrelated topics. Pikamini's most-injected rows are all automated
  (`auto-summary-*`, `behavioral-*`, `hb-*`, `exchange-summary-*`, `briefing-*`).
- **Rows below the floor still arrive (unconfirmed).** With `GHOST_MIN_SCORE=0.3`, rows scoring
  0.02–0.22 were returned in replay. They are probably edge passengers, the bug #141 fixed, which
  is not deployed. The Sept build has no `via` to confirm it.
- **Chinese phrasing and dates.** "Can she eat X?" and "10/15 lunch" miss, where keyword
  rephrasings ("X allergy", "10月15日") hit at #1–2.

Measurement caveats: shell does not log the injected text. `memory_accesses` logs Phase-2
direct hits (`context.go:978`), so it is a lower bound that excludes pins and edge passengers.
Replays (about 40 total across reviewers) ran on today's store with the Sept-26 PATH binary,
and the PATH binary's pin budget drops most pins, so a replay understates production.

### L. Lifecycle: the rules the stores run are not the rules in the code (verified)

This is the largest single reason the agents cannot recall what they know.

- Reflect reads its rules from each DB's `reflect_rules` table
  (`internal/store/reflect_rules.go:82`). Built-ins are seeded with `INSERT OR IGNORE`
  (`internal/store/reflect.go:200`). So once a row exists, later changes to `builtinRules` never
  reach that store. Only three hand-written `UPDATE`s exist (`internal/store/sqlite.go:395-415`),
  none for promote or demote.
- `103f230` retuned the defaults: promote needs `access>50 AND age>72h`, and demote needs
  `unaccessed>168h`. The live rows are the March originals:

| rule | code (`8cfd922`) | pikamini stored | umbreonmini stored |
|---|---|---|---|
| `sys-promote-to-ltm` | stm, access>50, age>72h (+≥3 access days in code) | stm, access>3, age>24h | stm, access>3, age>24h |
| `sys-demote-stale-ltm` | ltm, unaccessed>168h | ltm, age>168h AND access<2 | **ltm, age>168h** (no access condition) |
| `sys-decay-sensory` | sensory, age>4h | sensory, age>4h, access<1 | sensory, age>4h |

- **Umbreon:** every unpinned ltm memory is demoted 7 days after it is *created*, however often
  it is used. Umbreon's own namespace: 68 ltm, of which no unprotected row is older than 7 days;
  4,825 dormant. The signature: for 37% of umbreon's distilled dormant rows the last access falls
  140–172h after creation (1.5% on pika). It is still running, at about 60 demotions a week.
  63% of its dormant rows (3,043 of 4,825) are distilled facts, not automated rows.
- **Hidden-but-true facts (examples, paraphrased):** where the family lives, a food one person
  dislikes, a favourite dessert, a "buy this supplement only on sale" rule, which agent owns
  reminders, family birthdays and star signs, a clothing size. None has a live copy in umbreon's
  store.
- **Pikamini** has the opposite problem. Its promote rule (`access>3 after 24h`) counts
  injections, so about half of everything reaches ltm. Its demote rule only fires on rows
  accessed fewer than 2 times, so ltm only grows (5,105 rows in its own namespace).
- Promotion and demotion are in-place UPDATEs with no timestamp or reason, so *when* a row went
  dormant can only be estimated from last access.

### C. Constraints and corrections

- Allergy-class constraints held up in all 5 meal or food turns checked: both agents flagged the
  allergen. They live in pinned canonical memories, and shell puts every pin in the system
  prompt (umbreon: 15.2 KB of a 16 KB budget, so close to full).
- One constraint failure: a pinned food canonical contained a line that pushed the agent into
  the very behaviour a later rule banned. The ban sat in an stm memory that had never been
  accessed. The agent's own later rewrite of the pin fixed it.
- Of about 10 real user corrections in 60 days, **no stale memory outranked its correction.**
  Each failure was that the corrected rule was no longer live. A "both agents should answer"
  rule went dormant at about 166h and was re-asked three times. A reminder-ownership rule went
  dormant at 154h, and a duplicate reminder followed two days later. Both match the umbreon
  demote rule above.
- "You forgot we're travelling": the trip memories were live, but nothing connects a plant or
  meal question to them. No `prevents` or `depends_on` edge exists anywhere.
- Several existing `contradicts` edges join unrelated memories (an allergy canonical to a
  stray-cat note). Agents use `contradicts` loosely, which matches the edge-adoption census.

### N. A third of each memory file is in a namespace the agent never reads (verified)

- Each agent's `memory.db` also holds `agent:claude-code` rows: 3,723 in pikamini's file and
  3,239 in umbreon's, about 1,100 new in each in the window. They are distilled session facts,
  for example a skincare product one person tolerates.
- Source: ghost's Claude Code hooks (`hooks/ghost-stop.sh:13`, `ghost-precompact.sh:13`,
  `ghost-user-prompt.sh:12`) are installed globally (`~/.claude/settings.json`), so they also run
  inside the shell agents' Claude sessions. They inherit the agent's `GHOST_DB`, but take the
  namespace from `GHOST_AGENT_NS`, default `agent:claude-code`. Shell sets `GHOST_NS`, which
  nothing reads (W3).
- Shell's injection reads only the agent's own namespace, so the agent never sees these facts,
  even though they sit in its own file. This is the split-brain again, now inside one file.

### W. Write side: agents do store; ghost loses or hides what they store

Shell's write ledger is mostly noise. Of 110 flagged turns, only one is a probable real miss
(umbreon, a plant-diary claim with only reads that turn; unconfirmed). About 64 of pikamini's 82
"unclaimed" flags are scheduler prompts whose own text contains a trigger word
(`shell/internal/bridge/write_verify.go:35-38`). So "the agent forgot to save" is not the problem.
The problems are what happens to the write:

- **W1. Key collisions turn unrelated facts into versions of each other (verified).** Auto-capture
  derives keys from the first words (`internal/capture/intents.go:158`), so generic keys recur:
  `notion`, `costco`, `user`, `user-assistant`. In umbreon's file (`agent:claude-code` namespace,
  the hook path in N) the key `umbreon` is at v26. Its versions alternate between an
  agent-verified ingredient check on a food (relevant to a known allergy) and a question someone
  asked in chat. The head (v26) is the question, now dormant. The ingredient fact survives only
  as a superseded version, which retrieval never returns. Worst cases: `user-assistant` at v62
  with 42 distinct contents in pikamini's file. In the agents' own namespaces, collisions are
  rarer (e.g. `merged-verify-before-assert` at v16 with 10 distinct contents). Put cannot tell
  "new value of the same fact" from "different fact that happens to share a key".
- **W2. Capture keys break UTF-8 (verified).** `intents.go:162` cuts the key at byte 60
  (`key[:60]`), which splits multi-byte Chinese characters. 69 keys in pikamini's file and 58
  in umbreon's are invalid UTF-8.
- **W3. The MCP put drops writes that leave out `ns` (verified).** See Speed and reliability: 22
  failures, about 16 writes lost, because `GHOST_NS` is never read.
- **W4. Duplicate file refs abort the whole put (verified).** 13 media notes lost
  (`sqlite_crud.go:283`).
- **W5. Agent-written metadata is thin.** In the window's explicit puts: `source_kind` is set on
  93–94%, `source_user` on only 61% (pika) / 33% (umbreon), and 41% / 62% of memories that name a
  person leave it empty. About 30–45% of corrections carry a `contradicts` edge. **Zero** `prevents`
  or `depends_on` edges exist in either store, including on 35 food or skincare plans. Pika
  re-states an existing fact under a new key in about 23% of a 30-memory sample. One agent appears
  under two `source_user` spellings (`umbreonmini`, `umbreon`).
- **W6. Ballast volume.** Of the 2,527 rows new in pikamini's file (all namespaces), about 1,000 are automated
  (exchange 290, hygiene 276, media-note 150, briefing 147, session 42, capture 119). For
  pikamini's exchange rows `utility_count` equals `access_count` exactly, so "utility" there
  measures injection, not use.

Retrieval spot-tests on facts that did land (11 natural follow-up questions, snapshot, reranker
off): 6 found, 5 not. 3 misses were facts gone dormant (default Search excludes dormant,
`internal/store/search.go:424`). One was a Chinese question phrasing ("can she eat X?") where
two hub memories, one of them pinned, took the top slots and the relevant facts ranked below 6.
Rephrased as keywords, the same facts ranked #1 and #2. One was a slash date ("10/15 lunch") that
missed; "10月15日" found it at #2.

## Proposals

Ghost-side items are all deterministic: a rule, a knob, metadata or a guard. No LLM in the store.
Ordered by expected effect on "recall it when needed".

| # | Change | Fixes | Size | Needs approval |
|---|---|---|---|---|
| G1 | **Reconcile system rule rows with code, with a conservative demote default.** (Shipped: every seeder-owned `sys-*` row is rewritten to the builtin on open; the demote builtin became `unaccessed>168h AND access<2`, because the bare code default would have demoted 4,039 of pika's 5,085 unprotected ltm rows in one pass on the dry run; with the new default, 0.) Version the builtins; on startup, rewrite a `sys-*` row only when it still equals a known older default (never a hand-edited row); show stored vs builtin (a flag on the existing rule listing, or a new `ghost rules diff`). | L (umbreon 7-day wipe, pika ltm bloat) | small | migration: no (old defaults live in code); a new subcommand: **yes** |
| G2 | **Restore the casualties once G1 is live.** Dry-run list of dormant distilled rows (not automated prefixes) that were used and are not superseded; promote them back. Reversible tier flip. | L (hidden household facts, corrected rules) | small | **yes**, prod data write |
| G3 | **Record why a tier changed.** Log every reflect transition (memory, rule id, from→to, time) to the existing `rule_events` table or a sibling. Today it is an UPDATE in place with no trace. | lets a review (or the agent) audit and reverse a demotion; the owner's lifecycle-audit wish | medium | yes if it needs a new table |
| G4 | **Write guards.** (a) MCP and the hooks: one namespace variable (read `GHOST_NS`, keep `GHOST_AGENT_NS` as an alias) and default `ns` from it when left out; (b) dedupe `Files` by path before insert; (c) cut capture keys on a rune boundary; (d) capture never supersedes a key whose head it did not write, or whose content is dissimilar; it takes a suffixed key instead. | N, W1–W4 (≈7k invisible rows, ≈16 lost puts, 13 lost media notes, invalid keys, facts overwritten by chatter) | small each | no |
| G5 | **Dormant fallback in Context.** When the live pool's top score is weak, search dormant rows too and admit hits above a strict floor, tagged `via=dormant`; a hit counts as an access. | R/L (stale value given while the current one was dormant) | medium | no (a knob, off by default until evaluated) |
| G6 | **Down-weight automated rows.** A bounded multiplicative knob on rows by origin (media-note, hb, exchange, briefing, auto-summary, all identifiable by key prefix or meta). Store it as metadata on write, not as prefix matching in the ranker; shell's daemon writers must set it. | R (ballast fills the budget) | medium | no (ghost); shell change too |
| G7 | **Deploy what is merged.** #136–#142 (via, used_count, edge-passenger floor) are not live. Also expose the embedded ghost commit (`ghost.Version`, and log it at daemon start) so drift shows up in the logs. Recording it in the DB would need a new table. | D | small | **yes**, deploy is the owner's call |
| G8 | **CJK query normalisation.** Turn slash dates into the stored forms; for Han-script queries, drop question particles before FTS. | R (Chinese phrasing misses) | small | no |

Shell-side (not ghost; listed so they are not lost):
- S1. Switch the chat-scoped fetch from `TagMode: "filter"` to boost, or add a third untagged arm.
  Untagged distilled facts should not be capped at 1/3 of the budget.
- S2. Ledgers: skip scheduler and warm-up prompts, recognise `plantlog` and doc-edit writes, and
  log `ghost_put` details. Better: log the injected keys per turn. The present ledgers measure
  nothing.
- S3. Report `curate op=used` once G7 lands, so promotion can key on use instead of injection.
- S4. Prompt: the agents write zero `prevents`/`depends_on` edges and leave `source_user` empty on
  41–62% of person facts. The ghost MCP instructions already ask for this, so the agent context is
  where it is being lost.
- S5. The pinned layer is at 15.2 of 16 KB on umbreon; the next pin will push one out silently.

Also for N: move or re-tag the existing `agent:claude-code` rows in each agent file into the agent's namespace (prod data write, owner-approved, after a dry-run list).

Suggested order: G1 + G4 (one PR each, both small, both stop ongoing loss), then G7 deploy and
G2 restore (owner-approved), then G3, then G5/G6 behind eval cases built from this review's misses
(soy ratio, snack brand, travel context), then G8.

## Verification of this review

Every "verified" claim above was re-checked in the main session against the snapshot or code:
the rule rows (`select * from reflect_rules`), the soy row's demotion time against `daemon.log`,
the `umbreon` key's version history, the 69 invalid-UTF-8 keys, the missing `GHOST_NS` read,
the `memory_files` insert, shell's 2/3 budget split, per-namespace counts, and the hooks' `GHOST_AGENT_NS` default. Claims marked "unconfirmed" were not.
