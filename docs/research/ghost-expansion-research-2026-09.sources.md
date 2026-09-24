# Sources for ghost-expansion-research-2026-09

Trimmed from three research-agent web sweeps on 2026-09-18.
F = fetched through WebFetch's summariser model, S = search snippet only.
Neither is a verbatim read.
The review threads on the parent doc list what was independently verified.

Only Sweep 3 backs the current doc.
Sweeps 1 and 2 answered earlier readings of the owner's why and are kept as reference, not as findings.

# Sweep 3 — owned, encrypted, pluggable (the current doc)

# Owned + pluggable: encryption at rest, harness plug points, lock-in (sweep 3, 2026-09-18)

Labels: **F** = fetched primary source (quotes verbatim via summariser or raw curl), **S** = search snippet only, **UNVERIFIED** = could not confirm.

## Q1. Encryption at rest for a pure-Go SQLite file

### modernc.org/sqlite — no encryption path
- pkg.go.dev page (v1.59.0, published 2026-09-15, BSD-3) makes no mention of encryption / codec / SQLCipher / SEE. **F** https://pkg.go.dev/modernc.org/sqlite
- Its only VFS surface is `modernc.org/sqlite/vfs`: "Package vfs exposes a Go fs.FS to SQLite as a read-only VFS." / "The VFS is read only: it has no write path and refuses to create a journal, so a database opened through it can only be read from." API is `New(fs fs.FS) (name string, _ *FS, _ error)`. No writable custom-VFS API is documented, so there is no hook to put an encrypting layer under it. **F** https://pkg.go.dev/modernc.org/sqlite/vfs
- Third-party doc (hanzoai/sqlcipher on pkg.go.dev) states modernc "as of v1.56.0 ... contains no SQLCipher support, no SQLITE_HAS_CODEC build, and no encryption API surface" and that SQLCipher's codec "is a hook inside SQLite's pager, above the VFS". **S** https://pkg.go.dev/github.com/hanzoai/sqlcipher
- GitHub mirror issue search `?q=encrypt` returns "No results" (open issues filter) **F** https://github.com/modernc-org/sqlite/issues?q=encrypt ; GitLab tracker not fetched. Trackers: https://gitlab.com/cznic/sqlite/-/issues , https://github.com/modernc-org/sqlite

### ncruces/go-sqlite3 — CGo-free, has encryption VFSes
- CGo-free, but NO LONGER wazero: README title is "Go bindings to SQLite using wasm2go"; "It wraps a Wasm build of SQLite, and uses wasm2go to translate it to Go." **F** (raw README) https://github.com/ncruces/go-sqlite3
- Switch history: maintainer says wasm2go "resolves long standing issues, such as the slow performance on interpreter targets, and slow startup on compiler targets"; breaking changes "mostly related to import paths and wazero configuration. I don't expect _any_ changes to the SQLite API". A community member reported: "Compiling wasm2go sources seems quite heavy, on my machine it took ~40 seconds and 4GiB of RAM." **F** https://github.com/ncruces/go-sqlite3/discussions/361 . "v0.32.0 likely last wazero version; 0.35+ no wazero" **S**.
- Caveats (README): "Because each database connection executes within a Wasm sandboxed environment, memory usage will be higher than alternatives." Performance: "Performance of the database/sql driver is competitive with alternatives" (links cvilsmeier/go-sqlite-bench). `database/sql` driver at `github.com/ncruces/go-sqlite3/driver`. **F**
- FTS5: available but OPT-IN per connection, not default-on. ext/README: "`github.com/ncruces/go-sqlite3/ext/fts5` provides full-text search"; `ext/fts5/fts5.go` imports `github.com/ncruces/go-sqlite3-wasm/v6/fts5` and exposes `func Register(db *sqlite3.Conn) error` / `RegisterCustom` (loaded via `sqlite3.ExtensionInit(..., fts5.DylinkInfo)`). So a switch means registering FTS5 on every connection (e.g. a driver connect hook) plus an extra wasm2go compile unit. **F** (raw source) https://github.com/ncruces/go-sqlite3/blob/main/ext/fts5/fts5.go
- WAL: "On Unix, this package uses mmap to implement shared-memory for the WAL-index, like SQLite. On Windows, this package uses MapViewOfFile". Support matrix: Linux/macOS/Windows amd64+arm64 = full file locks + full shared-memory WAL; BSDs reduced locking. **F** https://github.com/ncruces/go-sqlite3/blob/main/vfs/README.md , https://github.com/ncruces/go-sqlite3/wiki/Support-matrix
- `vfs/adiantum` (**F**, raw README + api.go): "This package wraps an SQLite VFS to offer encryption at rest." Adiantum = XChaCha12 + AES + NH/Poly1305; "we use Argon2id to derive 256-bit keys from plain text where needed. File contents are encrypted in 4 KiB blocks". "The VFS encrypts all files _except_ super journals" (so WAL and rollback journal are encrypted — inference from that sentence); "Temporary files _are_ encrypted with **random** keys" -> recommend `PRAGMA temp_store = memory;`.
  - NOT protected: "The only security property that disk encryption provides is that all information such an adversary can obtain is whether the data in a sector has or has not changed over time." / "The encryption offered by this package is fully deterministic. This means that an adversary who can get ahold of multiple snapshots (e.g. backups) of a database file can learn precisely: which blocks changed, which ones didn't, which got reverted." / "This is weaker than other forms of SQLite encryption that include *some* nondeterminism." / "This package does not claim protect databases against tampering or forgery." -> "you should sign your backups".
  - Key handling: URI params `key` (32 bytes), `hexkey` (64 hex digits), `textkey` (any length, Argon2id KDF); warning "this makes your key easily accessible to other parts of your application" -> use `PRAGMA key=/hexkey=/textkey=` instead. Pluggable `KDF(secret string) (key []byte)` / `HBSH(key)` interface.
  - Numeric performance cost: not stated in README ("not found").
  https://github.com/ncruces/go-sqlite3/blob/main/vfs/adiantum/README.md
- `vfs/xts` (**F**): AES-XTS; "We use PBKDF2-HMAC-SHA512 to derive AES-128 keys from plain text where needed"; same determinism + no-tamper caveats; "AES-XTS uses _only_ NIST and FIPS 140 approved cryptographic primitives"; "In general Adiantum performs significantly better". https://github.com/ncruces/go-sqlite3/blob/main/vfs/xts/README.md
- Switching cost for ghost (inference, not sourced): driver name `sqlite3` vs modernc `sqlite`, DSN/pragma syntax differs, higher per-connection memory, heavy compile, FTS5 tokenizer behaviour to re-verify, one-time migrate of existing plaintext DB (VACUUM INTO / backup API into adiantum VFS), and a new dependency (needs human approval under CONVENTIONS).

### SQLCipher / SEE — both C => CGo
- SQLCipher Community: BSD-3-Clause, C; crypto providers OpenSSL / LibTomCrypt / CommonCrypto / NSS; 256-bit AES, per-page HMAC, PBKDF2; "as little as 5-15% overhead for encryption on many operations"; commercial editions from Zetetic. **F** https://github.com/sqlcipher/sqlcipher . Go bindings found are all CGo (e.g. xeodou/go-sqlcipher) **S**.
- SEE: "$2000 one time fee"; "A drop-in replacement for public-domain SQLite C source code that has the added ability to read/write AES-encrypted databases"; perpetual source licence. **F** https://sqlite.org/prosupport.html , https://sqlite.org/see/doc/release/www/index.wiki . C source => CGo (or re-transpile, which modernc does not offer).

### OS-level
- FileVault: "Without valid login credentials or a cryptographic recovery key, the internal APFS volumes remain encrypted and are protected from unauthorized access even if the physical storage device is removed and connected to another computer." "credentials are required during the boot process." **F** https://support.apple.com/guide/security/volume-encryption-with-filevault-sec4c6dc1b6e/web . Apple's pages do not claim any protection once unlocked; i.e. a logged-in user's processes, Time Machine/cloud backups of the file, and copies to other disks see plaintext (inference from volume-level design; Apple statement on this: not found).
- Encrypted APFS sparsebundle/disk image: not fetched — UNVERIFIED (hdiutil AES-256 images exist; mount step is not transparent to a CLI/MCP server).
- Linux fscrypt (kernel doc, **F**): "fscrypt protects the confidentiality of file contents and filenames in the event of a single point-in-time permanent offline compromise of the block device content"; once key loaded "fscrypt does not hide the plaintext file contents or filenames from other users on the same system"; root compromise defeats it. https://www.kernel.org/doc/html/latest/filesystems/fscrypt.html . LUKS: not fetched (same volume-level class).
- gocryptfs (**F**): FUSE overlay, per-file AES-GCM/XChaCha20-Poly1305 authenticated; Linux native, macOS "beta-quality"; needs a mount. https://github.com/rfjakob/gocryptfs . age whole-file encrypt at open/close: not fetched; by construction it is not transparent (plaintext temp copy while open, no crash safety for WAL).

### Key management patterns
- `zalando/go-keyring`: no CGo — "It aims to simplify using statically linked binaries, which is cumbersome when relying on C bindings"; macOS via "/usr/bin/security binary"; Linux "Secret Service dbus interface, which is provided by GNOME Keyring" (needs a `login` collection; headless boxes lack it); Windows Credential Manager. **F** https://github.com/zalando/go-keyring
- 1Password CLI: "Authenticate 1Password CLI the same way you unlock your device, like with your fingerprint, face, Apple Watch, Windows Hello PIN, or device user password." **F** https://www.1password.dev/cli/app-integration/
- `pass`: "Each password lives inside of a gpg encrypted file"; `~/.password-store`; "standard gpg-agent (which can be configured to stay authenticated for several minutes)". **F** https://www.passwordstore.org/
- Obsidian: "Obsidian stores your notes as Markdown-formatted plain text files in a vault." No local at-rest encryption mentioned. **F** https://obsidian.md/help/data-storage
- Signal Desktop (cautionary tale): SQLCipher DB key sat in plaintext `config.json`; researcher: makes "the encrypted database worthless"; Signal support: "The database key was never intended to be a secret. At-rest encryption is not something that Signal Desktop is currently trying to provide or has ever claimed to provide."; fixed 2024 via Electron safeStorage (Keychain / DPAPI / kwallet, gnome-libsecret). **F** https://www.bleepingcomputer.com/news/security/signal-downplays-encryption-key-flaw-fixes-it-after-x-drama/
- Lesson: DB encryption is only as good as where the key lives. Key in env var / config file = Signal's 2018 bug. Keychain-held key still yields to same-user malware (any process run as the user can ask `security` for it, subject to ACL prompts).

### Comparison
| Option | CGo? | Transparent to app? | Stolen disk | Other local user | Backup/sync leak | Same-user malware | Cost |
|---|---|---|---|---|---|---|---|
| modernc as-is + FileVault/LUKS | no | fully | yes (when powered off/locked out) | no (file perms only) | **no** | no | zero; user must have FDE on |
| ncruces + adiantum/xts VFS | no | yes after driver swap + key supply | yes | yes (if key not readable) | yes, but deterministic: snapshot diffing leaks which 4K blocks changed; no tamper protection | no (key in process/keychain) | driver swap, more RAM, heavy compile, new dep, migration |
| SQLCipher | **yes** | yes | yes | yes | yes (+ page HMAC) | no | breaks no-CGo; 5-15% |
| SEE | **yes** | yes | yes | yes | yes | no | $2000 + CGo |
| gocryptfs / encrypted disk image / fscrypt dir | no | only after mount/unlock step | yes | fscrypt: no once unlocked | yes while unmounted | no | per-OS setup, macOS FUSE beta |
| age/whole-file at open/close | no | no (plaintext while open) | partial | partial | yes | no | breaks WAL/concurrent MCP+CLI access |


## Q2. Plug points in coding-agent harnesses (Sept 2026)

| Harness | (a) MCP | (b) Context-injecting hooks | Static file | (c) Built-in memory |
|---|---|---|---|---|
| **Claude Code** | yes. `claude mcp add [options] <name> -- <command> [args...]`; local+user scope in `~/.claude.json`, project scope in `.mcp.json`. **F** https://code.claude.com/docs/en/mcp | yes. Events include `SessionStart`, `UserPromptSubmit`, `Stop`, `PreCompact`, `PostCompact`, `SessionEnd`, `SubagentStart/Stop`, `PreToolUse`, `PostToolUse` (32 listed). Injection: `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"..."}}`; and "The exceptions are `UserPromptSubmit`, `UserPromptExpansion`, `SessionStart`, and `PostModelSwitch`, where Claude Code adds plain-text stdout as context that Claude can see and act on." Stop exposes `last_assistant_message` (capture point). **F** https://code.claude.com/docs/en/hooks | `CLAUDE.md` (+`@import`, `.claude/rules/`). "Claude Code reads `CLAUDE.md`, not `AGENTS.md`" (import it with `@AGENTS.md`). **F** | Auto memory, on by default. "Each project gets its own memory directory at `~/.claude/projects/<project>/memory/`"; `MEMORY.md` index, "first 200 lines ... or the first 25KB" loaded; "Auto memory is machine-local."; "plain markdown you can edit or delete at any time"; relocatable via `autoMemoryDirectory`; disable `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`. **F** https://code.claude.com/docs/en/memory |
| **OpenAI Codex CLI** | yes. `mcp_servers.<id>` with `command` ("Launcher command for an MCP stdio server") / `url`, in `~/.codex/config.toml`. **F** https://learn.chatgpt.com/docs/config-file/config-reference | yes (Claude-shaped). Events: `SessionStart, SessionEnd, SubagentStart, SubagentStop, PreToolUse, PermissionRequest, PostToolUse, PreCompact, PostCompact, UserPromptSubmit, Stop, Interrupt`. Config: `~/.codex/hooks.json`, `~/.codex/config.toml`, `<repo>/.codex/hooks.json`; toggle `[features] hooks = false`. SessionStart/UserPromptSubmit: "Plain text on stdout is added as extra developer context" + `hookSpecificOutput.additionalContext`. Stop: "Plain text output is invalid for this event." **F** https://learn.chatgpt.com/docs/hooks . Caveat: open issue says SessionStart `additionalContext` rejected on codex-cli 0.154.0 **S** https://github.com/openai/codex/issues/45999 | `AGENTS.md` (`project_doc_max_bytes`, `project_doc_fallback_filenames`). **F** | Memories, "off by default" (`features.memories`); stored locally under `~/.codex/memories/` ("summaries, durable entries, recent inputs, and supporting evidence"), "treated as generated state"; `/memories` command. **F** https://learn.chatgpt.com/docs/customization/memories . Generated by an LLM summarising past threads **S**. |
| **Gemini CLI** | yes. `mcpServers` in `~/.gemini/settings.json` or `.gemini/settings.json` (`command`,`args`,`env`,`cwd`,`timeout`,`trust`). **F** https://geminicli.com/docs/tools/mcp-server/ | yes, different names: `BeforeTool, AfterTool, BeforeAgent, AfterAgent, BeforeModel, BeforeToolSelection, AfterModel, SessionStart, SessionEnd, Notification, PreCompress`. `SessionStart` `hookSpecificOutput.additionalContext` = "Injected as the first turn in history"; `BeforeAgent` additionalContext "appended to the prompt for the current turn only". Configured in `settings.json` `hooks`. **F** https://geminicli.com/docs/hooks/reference/ | `GEMINI.md` (`~/.gemini/GEMINI.md` + hierarchy); filename configurable: `"context": { "fileName": ["AGENTS.md", "CONTEXT.md", "GEMINI.md"] }`. **F** https://geminicli.com/docs/cli/gemini-md/ | `/memory show|reload`; "Auto Memory is an experimental feature that mines your past Gemini CLI sessions in the background and proposes durable memory updates and reusable Agent Skills"; local: transcripts in `~/.gemini/tmp/<project>/chats/`, candidates as `.patch` / `SKILL.md`, reviewed via `/memory inbox`. **F** https://geminicli.com/docs/cli/auto-memory/ . `save_memory` -> `~/.gemini/GEMINI.md`: not re-confirmed, UNVERIFIED. |
| **Cursor** | yes. `.cursor/mcp.json` / `~/.cursor/mcp.json`; stdio, SSE, Streamable HTTP. **F** https://cursor.com/docs/context/mcp | yes, camelCase + snake_case output: `sessionStart, sessionEnd, preToolUse, postToolUse, beforeSubmitPrompt, preCompact, stop, afterAgentResponse, ...`; `~/.cursor/hooks.json`, `<project>/.cursor/hooks.json`. sessionStart: "additional context to add to the conversation's initial system context"; postToolUse `additional_context`; stop `followup_message`. **F** https://cursor.com/docs/hooks . `beforeSubmitPrompt` injecting `additional_context`: **S** only (forum feature requests + a claim that client 3.20.17 does) — UNVERIFIED. Forum bug: sessionStart additional_context "never injected" **S** https://forum.cursor.com/t/158452 | `.cursor/rules/`, User Rules, `AGENTS.md`. **F** | "Memories": the docs URL now resolves to the Rules page with no mention of Memories (**F**: absent). Third-party: per-project, background-model-proposed, reviewed in Settings **S**. Storage location/export: UNVERIFIED. |
| **opencode** | yes. `mcp` key in opencode config; `"type": "local"`, `"command": [..]`, `environment`. **F** https://opencode.ai/docs/mcp-servers/ | plugin events (JS/TS plugin, not a shell hook): `session.created, session.idle, session.compacted, message.updated, tool.execute.before/after, tui.prompt.append, experimental.session.compacting` (can "inject additional context into the compaction prompt"). No documented per-prompt injection hook on the page. **F** https://opencode.ai/docs/plugins/ | `AGENTS.md`. **F** | none found. |
| **Cline** | (MCP: not re-fetched; UNVERIFIED this sweep) | docs.cline.bot/features/hooks now only points to "SDK Plugins"; names not retrieved — UNVERIFIED. | `.clinerules` (per Claude Code docs listing). | not found |

**Common denominator.** (1) MCP stdio server — universal across all five confirmed; but tools are model-elective (the model must choose to call `ghost_context`). (2) A static instruction file — universal, but three names: `CLAUDE.md`, `AGENTS.md` (Codex, Cursor, opencode, Gemini via `context.fileName`), `GEMINI.md`; the store can only rely on it to *tell the model to call the MCP tools* or to hold a small rendered pinned block. (3) Automatic injection hooks — now present in Claude Code, Codex CLI, Gemini CLI, Cursor; Claude Code and Codex share event names and the `hookSpecificOutput.additionalContext` JSON shape + plain stdout; Gemini uses `BeforeAgent` for per-prompt; Cursor uses `additional_context` and per-prompt injection is unconfirmed. So: one `ghost hook <event>` CLI that prints text/JSON covers Claude Code + Codex nearly verbatim, thin adapters for Gemini/Cursor; MCP + instruction-file stanza is the fallback everywhere. Notable: every harness's built-in memory that was confirmed is a LOCAL plain-markdown file (Claude Code, Codex) — harness-specific, LLM-written, unencrypted, not shared across harnesses.

## Q3. The lock-in being avoided

- **ChatGPT memory**: vendor cloud. OpenAI Memory FAQ returned 403 (not fetched). Third-party consensus: official export "contains your conversations, not your saved memory entries"; saved memories must be copied by hand from Settings > Personalization > Manage memories, or by asking the model to write them out. **S** https://www.notis.ai/blog/can-you-export-your-chatgpt-memory/ ; export doc https://help.openai.com/en/articles/7260999-exporting-your-chatgpt-history-and-data (not fetched).
- **Claude (claude.ai) memory**: vendor cloud, viewable ("Settings > Capabilities ... 'View and edit your memory'"). Export = text: ask Claude to "Write out your memories of me verbatim" and "save...as a backup or bring it to another AI service by copying and pasting". Import = paste other provider's text; "Claude will extract key information and store it as individual memory entries"; "Memory imports are experimental and still in active development". **F** https://support.claude.com/en/articles/12123587-import-and-export-your-memory-from-claude
- **Claude Code auto-memory**: local plain markdown, machine-local (see Q2). Readable by any other agent with file access, but no other harness loads it.
- **Gemini**: vendor cloud; since 2026-03-26 Gemini imports memories + chat-history ZIPs from ChatGPT/Claude (not in UK/CH/EEA); Takeout export exists but "designed primarily for Google's own ecosystem". **S** https://www.ghacks.net/2026/03/31/google-adds-chatgpt-and-claude-import-tools-to-gemini-for-memory-and-chat-history/ ; Google help https://support.google.com/gemini/answer/16868299 (not fetched).
- Pattern: portability in 2026 = copy-paste prose / one-shot ZIP import, LLM re-extracted on arrival (lossy, one-way, no live shared store). No vendor lets another vendor's agent read its memory live.

**Neighbours (local-first / user-owned):**
1. **Basic Memory** — Markdown files + SQLite index; "Plain text on your disk. Forever."; "Cancel anytime, your data stays yours."; MCP-native; AGPL-3.0; no extraction LLM (the assistant writes notes); paid cloud sync $15/mo. **F** https://github.com/basicmachines-co/basic-memory
2. **localmem-mcp** — "One SQLite file you own" (`~/.localmem/memories.db`), fastembed `BAAI/bge-small-en-v1.5` + FTS5 bonus, no LLM, Python, MIT, positioned against Mem0/Zep/OpenMemory; no encryption at rest. **F** https://github.com/OpenAgentHQ/localmem-mcp — closest twin to ghost.
3. **sqlite-memory-mcp (RMANOV)** — SQLite, hybrid lexical+vector RRF, "rule-based zero-LLM entity extraction", consolidation cycle. **S** https://glama.ai/mcp/servers/RMANOV/sqlite-memory-mcp
4. **sqliteai/sqlite-memory** — SQLite extension, markdown-oriented, local llama.cpp embeddings, offline-first sync between agents. **S** https://github.com/sqliteai/sqlite-memory
5. **claude-mem / Engram / Memori** — per comparison table: claude-mem SQLite+Chroma (LLM optional), Engram SQLite FTS5 (no LLM), Memori SQL-native (no LLM). Same article: mem0's local path is gone — "OpenMemory ... was deleted from the monorepo on July 29, 2026 (commit ea2ee075, 208 files removed)"; `mem0-mcp` "archived in March 2026"; MCP entry now hosted. "The document does not mention encryption at rest for any of the thirteen servers compared." **F-secondary** (fetched, but a third-party comparison; per-project claims not confirmed at each README) https://mnemoverse.com/docs/library/memory-mcp-servers-compared

Takeaway: "local SQLite + MCP + no LLM" is no longer unique (localmem-mcp, Engram, sqlite-memory-mcp). Unoccupied ground found in this sweep: encryption at rest (none of the neighbours advertise it) and first-class hook adapters across harnesses.

# Sweep 2 — portable / private / secure in the literature's sense (superseded frame)

## Properties the literature treats as essential that a local LLM-free store most plausibly LACKS
(ghost specifics are hypotheses from CLAUDE.md/memory notes, NOT verified in code during this web task)
1. Principal-scoped retrieval as a hard FILTER (audience-aware disclosure) — ghost has ForUser/ForScope as boosts only. [MuPPET, PiSAs, GateMem, 2604.16548]
2. Verified, propagating forgetting — hard delete across versions, chunks, FTS, vectors, consolidation summaries, WAL, backups; pinned history "never purges" conflicts with erasure. [2606.30306, GateMem]
3. Instruction/data separation at render time tied to origin — only origin-trusted memory may enter the system prompt as instruction; the rest quoted as data. [Leong 2605.08442; OWASP/Claude Code v2.1.50; TMA-NM]
4. Writer-shell provenance — which model/harness authored each memory. [MemCollab, AFTER]
5. Per-shell behavioural conformance check on model swap — does the new model obey pinned rules, act on retrieved values. [MERIT 55%; INT-1]
6. Decontextualised, self-contained writes (coreference, absolute dates, entity normalisation, EN/ZH) — can't be done deterministically; must be demanded of the shell at write time and can only be linted by the store. [2511.17208, HaluMem]
7. Update-on-write semantics for changing facts, including on the pinned tier. [MERIT; owner's "corrected pins never demoted"]
8. Encryption at rest — not available in modernc.org/sqlite; integrity/tamper-evidence also absent. [Adiantum README]
9. Embedding model stamping + mismatch refusal. [vendor migration docs; owner's two-binaries note]
10. Ephemeral/incognito mode (no-write, no-access-log) as a first-class store mode. [Claude/ChatGPT/Gemini]
11. Whole-store point-in-time rollback after a poisoning incident (owner did this by hand on 2026-09-11 with .bak files + soft-deletes). [2604.16548 Rollbackability]
12. A stable, documented export carrying content + provenance + timestamps + content hash (the common denominator of PAM / Portable Agent Memory / Engram / .af) — ghost has export/import; whether it carries all four is UNVERIFIED.

## Addenda (after review)

**1.7 MCP as a portability layer.** Environment observation (this session): ghost is exposed as `mcp__ghost__*` tools to a Claude Code harness — any MCP-capable shell can call put/context/search unchanged. But MCP is a tool-call transport: it defines no memory schema, provenance fields, or semantics, so it makes the store REACHABLE from many shells, not the memory INTERPRETABLE the same way by them. PLUR blog (S, https://plur.ai/blog/open-standard-ai-agent-memory/): "MCP adoption is still concentrated in Anthropic-adjacent tools" (vendor claim, UNVERIFIED). Note the owner's production path is NOT MCP — the Telegram shell embeds ghost as a library and composes the system prompt itself — so the real portability contract is the rendered prompt text + the Go API, and the tool descriptions/instructions shipped over MCP are themselves model-facing text that different models will follow differently (INT-1).

**5.5 Cognitive-science grounding.** "AI Meets Brain: Memory Systems from Cognitive Neuroscience to Autonomous Agents", Liang, Li, Li, Zhou + 11, arXiv 2512.23343, 2025-12-29 — F (abstract only). Verbatim: "constrained by interdisciplinary barriers, existing works struggle to assimilate the essence of human memory mechanisms"; covers taxonomy, storage, "complete management lifecycle from both biological and artificial perspectives", and "memory security from dual perspectives of attack and defense"; futures: "multimodal memory systems and skill acquisition". Abstract does not enumerate desiderata; whether the body discusses CLS/reconsolidation specifically is UNVERIFIED (57 pages, not read). Tulving episodic/semantic/procedural is already ghost's `kind` taxonomy and CoALA's. Complementary-learning-systems (fast episodic capture + slow consolidation into semantic) and reconsolidation (a retrieved memory becomes labile and is re-stored updated) — no primary source fetched in this sweep: NOT VERIFIED. Honest reading: the cog-sci literature supplies ARCHITECTURE metaphors (two-speed store, update-on-retrieval) rather than testable desiderata; the testable requirement lists come from the security/governance side (5.2).
**MemOS requirements:** arXiv 2507.03724 is in the prior sweep's source list — covered there, not re-researched. (Note HaluMem and the 2604.16548 security survey share authors with the MemOS group — Zhiyu Li, Feiyu Xiong — so "MemOS ranks well on HaluMem" (S) is a same-lab result.)

## Sources (URLs for items cited S above without a link)
- OWASP Top 10 for Agentic Applications (2025-12-09): https://genai.owasp.org/2025/12/09/owasp-top-10-for-agentic-applications-the-benchmark-for-agentic-security-in-the-age-of-autonomous-ai/ ; ASI06 explainer https://vectorize.io/articles/owasp-asi06 ; https://www.promptfoo.dev/docs/red-team/owasp-agentic-ai/
- OWASP "Memory is a feature…" (F): https://genai.owasp.org/2026/05/13/memory-is-a-feature-it-is-also-an-attack-surface/
- SpAIware: https://www.sciencedirect.com/science/article/abs/pii/S0167739X25002894 ; https://thehackernews.com/2024/09/chatgpt-macos-flaw-couldve-enabled-long.html
- Chroma Context Rot (F): https://www.trychroma.com/research/context-rot
- Anthropic context engineering (F): https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
- Manus context engineering: https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus ; Don't Break the Cache: https://arxiv.org/pdf/2601.06007
- Claude incognito: https://support.claude.com/en/articles/12260368-use-incognito-chats ; ChatGPT memory FAQ: https://help.openai.com/en/articles/8590148-memory-faq ; Gemini activity: https://support.google.com/gemini/answer/13594961 ; roundup: https://memx.app/glossary/temporary-chat/
- Drift-Adapter: https://arxiv.org/pdf/2509.23471 ; https://tianpan.co/blog/2026/07/05/retiring-an-embedding-model-reindex-without-downtime
- Memp: https://arxiv.org/abs/2508.06433 ; https://github.com/zjunlp/MemP
- Generative Agents: https://arxiv.org/pdf/2304.03442 ; mem0: https://mem0.ai/research , https://arxiv.org/html/2504.19413v1
- Letta: https://docs.letta.com/guides/core-concepts/agent-file ; https://github.com/letta-ai/agent-file ; https://docs.letta.com/guides/core-concepts/memory/context-hierarchy/
- PAM: https://portable-ai-memory.org/ ; memorywire: https://github.com/mthamil107/memorywire ; Engram: https://plur.ai/spec.html ; SSRN Engram protocol: https://papers.ssrn.com/sol3/papers.cfm?abstract_id=6878038
- W3C CG: https://www.w3.org/community/ai-agent-memory-interop/ ; proposal https://www.w3.org/community/blog/2026/05/18/proposed-group-ai-agent-memory-interoperability-community-group-community-group/
- Cross-model reader adaptation: https://arxiv.org/abs/2608.17050 ; Always-On survey: https://arxiv.org/pdf/2606.30306 ; MemLineage etc. via https://arxiv.org/pdf/2606.30566 (S)
- Adiantum VFS (F): https://github.com/ncruces/go-sqlite3/blob/main/vfs/adiantum/README.md ; gosqlite: https://gosqlite.org
- Anthropic memory tool (F): https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool


# Sweep 1 — LLM-free defect remedies (first draft, superseded as the frame; still valid as evidence under F6)

## Not found / gaps
- No paper found that evaluates encoder-only "was this memory used" attribution for promotion (Q1.3) — treat as an original experiment.
- No 2025-26 primary source found on incoming-edge expansion or k-truss pruning for agent memory graphs.
- go-fsrs, gonum Louvain, GraphRAG resolution_scale: recalled/snippet, not verified.
- All WebFetch content was summarised by a small model; numbers should be re-checked against PDFs before being quoted in a design doc.

## Sources (F = fetched via WebFetch, S = search snippet only)
- F https://arxiv.org/abs/2604.12007 — Memory Worth, 2026-04-13
- F https://arxiv.org/abs/2608.00017 — LUCID / memory reward inflation, 2026
- F https://arxiv.org/abs/2608.02508 — RoMeRL, 2026-08-03
- F https://arxiv.org/abs/2601.03192 — MemRL, 2026-01-06
- F https://arxiv.org/abs/2503.08026 — RMM, ACL 2025
- F https://arxiv.org/abs/2605.01567 — Feedback-Normalized Developer Memory, 2026-05-02
- F https://arxiv.org/abs/2606.06054 — MemGate, 2026-06-04
- S https://arxiv.org/pdf/2409.00729 ContextCite; https://arxiv.org/pdf/2411.15102 AttriBoT; https://arxiv.org/html/2505.16415v1 ARC-JSD
- S https://dl.acm.org/doi/10.1145/3765766.3765803 ACT-R agent memory (HAI 2025); https://arxiv.org/pdf/2512.20651 Memory Bear
- F https://arxiv.org/abs/2303.02813 — Well-Connected Communities, 2023
- S https://link.springer.com/chapter/10.1007/978-3-319-09912-5_4 mutual kNN clustering; https://arxiv.org/pdf/0912.3408; https://arxiv.org/pdf/1711.04712
- S https://www.mintlify.com/microsoft/graphrag/concepts/community-detection ; https://arxiv.org/html/2401.18059v1 RAPTOR; https://en.wikipedia.org/wiki/Leiden_algorithm
- F https://arxiv.org/abs/2501.13956 and https://arxiv.org/html/2501.13956 — Zep/Graphiti, 2025-01-20
- F https://arxiv.org/abs/2606.26511 — MemStrata, 2026-06-25
- F https://arxiv.org/abs/2606.06240 — TOKI, 2026-06-04
- F https://huggingface.co/cross-encoder/nli-deberta-v3-xsmall
- F https://github.com/sindrehaugen/neuro-cognitive-engine
- F https://arxiv.org/abs/2609.10263 — RD-Forget, 2026-09-09
- F https://arxiv.org/abs/2606.15903 — Control-Plane Placement / ForgetEval, 2026-06-14
- F https://github.com/open-spaced-repetition/awesome-fsrs/wiki/The-Algorithm — FSRS-6
- S https://borretti.me/article/implementing-fsrs-in-100-lines
- S https://arxiv.org/pdf/2601.18642 FadeMem; https://arxiv.org/pdf/2604.20300 FSFM; https://arxiv.org/pdf/2605.03675 MEMTIER; https://arxiv.org/pdf/2606.10616
- F https://arxiv.org/abs/2605.30711 — SAGE, 2026-05-29
- F https://arxiv.org/abs/2603.04549 — A-MAC, 2026-03-04
- S https://arxiv.org/abs/2607.22962 ConsistencyGate
- S https://github.com/urchade/GLiNER ; https://arxiv.org/html/2507.18546v1 GLiNER2; https://export.arxiv.org/pdf/2501.03172 GLiREL; https://arxiv.org/html/2605.10108v1 GLiNER-Relex
- F https://pkg.go.dev/modernc.org/sqlite/vec
- S https://github.com/asg017/sqlite-vec-go-bindings ; https://github.com/viant/sqlite-vec
- F https://dev.to/ruslan_manov/reviewable-memory-consolidation-for-local-ai-agents-2nd0 ; S https://glama.ai/mcp/servers/RMANOV/sqlite-memory-mcp
- F https://github.com/sqliteai/sqlite-memory
- S https://litestream.io/how-it-works/vfs/ ; https://github.com/benbjohnson/litestream/releases ; https://github.com/vlcn-io/cr-sqlite
- F https://arxiv.org/abs/2605.11032 — PAM, 2026-05-10
- F https://arxiv.org/abs/2606.01138 — memorywire, 2026-05-31
- F https://www.w3.org/community/ai-agent-memory-interop/
- F https://plur.ai/blog/open-standard-ai-agent-memory/
- F https://arxiv.org/abs/2606.24535 — Governed Shared Memory, 2026-06-23
- S https://arxiv.org/abs/2505.18279 Collaborative Memory; https://arxiv.org/html/2603.10062; https://arxiv.org/pdf/2606.30306
- S https://arxiv.org/abs/2606.23127 ; https://arxiv.org/html/2602.01869 ; https://arxiv.org/pdf/2606.09316 ; https://arxiv.org/pdf/2605.08386
