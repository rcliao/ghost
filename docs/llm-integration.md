# Integrating Ghost with LLMs

Ghost provides persistent memory for AI agents. This guide covers how to integrate ghost with Claude Code, other LLM-based tools, and custom agents.

## Integration Methods

| Method | Best For | Setup |
|--------|----------|-------|
| **MCP Server** | Claude Code, any MCP-compatible client | `ghost mcp-serve` as stdio transport |
| **Go Library** | Custom Go agents, Telegram bots, orchestrators | `import memory "github.com/rcliao/ghost"` |
| **CLI** | Shell scripts, cron jobs, any language via subprocess | `ghost put`, `ghost search`, `ghost context` |

---

## MCP Server (Claude Code)

### Setup

Add ghost to your Claude Code MCP config (`~/.claude.json`):

```json
{
  "mcpServers": {
    "ghost": {
      "type": "stdio",
      "command": "ghost",
      "args": ["mcp-serve"]
    }
  }
}
```

This exposes 5 tools to the agent:

| Tool | Purpose |
|------|---------|
| `ghost_put` | Store or update a memory |
| `ghost_search` | Full-text search with ranking |
| `ghost_context` | Budget-aware context assembly |
| `ghost_curate` | Instance-level lifecycle actions on a single memory |
| `ghost_reflect` | Run lifecycle rules across all memories (promote, decay, prune) |

### Teaching the Agent to Use Ghost

Ghost's MCP server includes built-in instructions that tell the agent what ghost is and how to use it. However, agents tend to under-use ghost unless explicitly instructed. The most effective way to drive adoption is through a **CLAUDE.md** file in your project root (or `~/.claude/CLAUDE.md` globally) with **strong, directive language**. Soft instructions like "consider using ghost" get ignored. Use "MUST", "Do NOT skip", and bold emphasis on required behaviors.

Example CLAUDE.md section:

```markdown
## Ghost Memory (MCP)

You have access to a persistent memory system via Ghost MCP tools. This is how you learn across sessions. **Use it.**

### MUST: Retrieve before working (ghost_context)
**Before starting any non-trivial task**, call ghost_context to check for relevant past learnings:
  ghost_context(query="<describe the task>", ns="agent:claude-code", budget=2000)

Trigger on:
- Debugging an error — past sessions may have hit the same issue
- Working in an unfamiliar repo/service — check for project-specific conventions
- Making architecture or design decisions — check for prior decisions
- Any task where you think "I might have seen this before"

Do NOT skip this step. The cost is one tool call; the benefit is avoiding repeated mistakes.

### When to write (ghost_put)
Store memories when you encounter:
- Debugging insights (error → root cause → fix)
- Architecture or design decisions with rationale
- User corrections (store the correct information)
- Patterns and conventions discovered
- Non-obvious gotchas that cost time

Use namespace `agent:claude-code` for general knowledge, or `project:<name>` for project-specific.
Use descriptive keys (e.g. "auth-flow-decision", "db-migration-gotcha").
Set importance 0.6-0.8 for most learnings, 0.9+ for critical decisions.
Set priority "high" for important learnings, "critical" for must-never-forget.

### When to curate (ghost_curate)
Use ghost_curate to act on individual memories:
  ghost_curate(ns="agent:claude-code", key="old-pattern", op="archive")

Operations: promote (tier up), demote (tier down), boost (importance +0.2),
diminish (importance -0.2), archive (dormant), delete (soft-delete),
pin (always in context), unpin (remove from always-on).

### When to reflect (ghost_reflect)
Run ghost_reflect when ghost_context returns compaction_suggested: true,
or after a long session with many stored learnings.
```

**Why the strong language matters:** LLM agents respond to directive framing. "MUST" and "Do NOT" create behavioral anchors that generic suggestions don't. The `ghost_context` call before work is the single highest-value habit — it prevents the agent from re-discovering things it already learned in prior sessions.

### Key Behaviors

**Namespace conventions** — Namespaces represent agent identity. Each namespace is one agent's isolated memory space:
- `agent:<name>` — per-agent memory space (e.g. `agent:pikamini`, `agent:coder`)

Memories are isolated by namespace — no cross-namespace visibility.

**Tag conventions** — Tags are first-class metadata for categorization and filtering:
- `identity` — core agent persona (name, personality, appearance)
- `lore` — background knowledge (relationships, fun facts)
- `chat:<id>` — per-conversation context
- `project:<name>` — project knowledge
- `learning` — accumulated insights
- `convention` — coding/writing rules
- `user:<name>` — per-user preferences

**Pinned memories** — Set `pinned: true` for memories that should always be loaded in context (Phase 1 of context assembly). Pinned memories are exempt from all lifecycle/reflect rules. Use for core identity, critical preferences, and always-on knowledge.

**Compaction signal** — When `ghost_context` exhausts its budget and skips candidates, the response includes `"compaction_suggested": true`. The agent should then call `ghost_reflect` to promote/decay/prune memories and free up space.

**Token budgets** — `ghost_context` accepts a `budget` parameter (default 4000 tokens). The agent can tune this based on available context window space.

### Tips for Better Memory Usage

1. **Be specific in CLAUDE.md** — Generic instructions like "use ghost" don't work well. List concrete scenarios and examples.

2. **Use tags for scoping** — Tag memories with `project:<name>`, `chat:<id>`, etc. for filtering. Search and context support `tags` parameter.

3. **Importance scores matter** — They affect retrieval ranking. Use 0.5 for general notes, 0.7-0.8 for useful learnings, 0.9+ for critical decisions.

4. **Tier selection** — Default tier is `stm` (subject to decay). Use `sensory` for raw transient observations (auto-deleted if unaccessed). Set `tier: "ltm"` for knowledge that should persist long-term.

5. **Pin critical memories** — Set `pinned: true` for core identity, critical preferences, and knowledge that must always be in context. Pinned memories bypass lifecycle decay.

6. **Reflect periodically** — The reflect cycle promotes frequently-accessed STM memories to LTM and decays unused ones. Without it, STM memories accumulate without curation.

### Curating Memories (ghost_curate)

`ghost_curate` provides direct, instance-level control over individual memories. Unlike `ghost_reflect` (which applies rules to all memories), `ghost_curate` lets you act on a specific memory by namespace and key.

**Operations:**

| Op | Effect |
|----|--------|
| `promote` | Tier up: dormant → stm → ltm |
| `demote` | Tier down: ltm → stm → dormant |
| `boost` | Importance +0.2 (caps at 1.0) |
| `diminish` | Importance -0.2 (floors at 0.1) |
| `archive` | Move to dormant tier |
| `delete` | Soft-delete (recoverable) |
| `pin` | Always loaded in context, exempt from decay |
| `unpin` | Remove from always-on context |

**Review workflow** — Use `ghost_context` or `ghost_search` to find memories, then `ghost_curate` to act on specific ones:

```
# 1. Find memories about a topic
ghost_search(query="database migration", ns="project:myapp")

# 2. Promote a useful one
ghost_curate(ns="project:myapp", key="pg-migration-steps", op="promote")

# 3. Archive an outdated one
ghost_curate(ns="project:myapp", key="old-mysql-config", op="archive")

# 4. Boost importance of a frequently-needed fact
ghost_curate(ns="project:myapp", key="connection-pool-settings", op="boost")
```

**When to use curate vs reflect:**
- `ghost_curate` — You know which specific memory to act on (intent-driven)
- `ghost_reflect` — Run bulk lifecycle maintenance across all memories (rule-driven)

---

## Go Library

For Go agents, import ghost directly:

```go
import memory "github.com/rcliao/ghost"

store, err := memory.NewSQLiteStore("~/.ghost/memory.db")
if err != nil { log.Fatal(err) }
defer store.Close()
```

### Storing Memories

```go
mem, err := store.Put(ctx, memory.PutParams{
    NS:         "project:myapp",
    Key:        "auth-architecture",
    Content:    "Using JWT with refresh tokens, 15min access / 7d refresh",
    Kind:       "semantic",       // semantic | episodic | procedural (auto-detected from tier if omitted)
    Priority:   "high",           // low | normal | high | critical
    Importance: 0.8,              // 0.0-1.0, affects retrieval ranking
    Tier:       "ltm",            // sensory | stm (default) | ltm | dormant
    Pinned:     false,            // true = always loaded in context, exempt from decay
    Tags:       []string{"auth", "architecture"},
})
```

### Retrieving Context

```go
result, err := store.Context(ctx, memory.ContextParams{
    NS:     "project:myapp",
    Query:  "authentication flow",
    Budget: 2000, // max tokens
})

for _, mem := range result.Memories {
    fmt.Printf("[%s] %s (score: %.2f)\n", mem.Key, mem.Content, mem.Score)
}

if result.CompactionSuggested {
    // Too many memories competing for budget — run reflect
    store.Reflect(ctx, memory.ReflectParams{NS: "project:myapp"})
}
```

### Logging Exchanges

For conversational agents, store exchanges as episodic memory with TTL and tags:

```go
store.Put(ctx, memory.PutParams{
    NS:         "agent:mybot",
    Key:        fmt.Sprintf("exchange-%d", time.Now().UnixMilli()),
    Content:    fmt.Sprintf("User: %s\nAssistant: %s", userMsg, response),
    Kind:       "episodic",
    Tags:       []string{"chat:123"},
    TTL:        "7d",
    Importance: 0.3,
})
```

### Curating Individual Memories

```go
// Promote a useful memory from STM to LTM
result, err := store.Curate(ctx, memory.CurateParams{
    NS:  "project:myapp",
    Key: "auth-architecture",
    Op:  "promote",  // promote | demote | boost | diminish | archive | delete | pin | unpin
})
fmt.Printf("%s: %s → %s\n", result.Op, result.OldTier, result.NewTier)
```

### Running Reflect

```go
result, err := store.Reflect(ctx, memory.ReflectParams{
    NS:     "project:myapp",   // optional, empty = all namespaces
    DryRun: false,
})
fmt.Printf("Evaluated: %d, Promoted: %d, Decayed: %d, Deleted: %d\n",
    result.MemoriesEvaluated, result.Promoted, result.Decayed, result.Deleted)
```

---

## CLI Integration

For non-Go agents, use the CLI via subprocess:

```bash
# Store a memory
ghost put -n "project:myapp" -k "decision-db" \
  --kind semantic --importance 0.8 \
  "Chose PostgreSQL over MySQL for JSONB support"

# Get context for a task
ghost context -n "project:myapp" -q "database setup" --budget 2000

# Search for specific knowledge
ghost search "PostgreSQL" -n "project:myapp"

# Curate a specific memory
ghost curate -n "project:myapp" -k "old-decision" --op archive
ghost curate -n "project:myapp" -k "key-insight" --op promote
ghost curate -n "project:myapp" -k "critical-fact" --op boost

# Run lifecycle maintenance
ghost reflect --ns "project:myapp"
ghost reflect --dry-run  # preview without applying
```

All output is JSON by default, making it easy to parse in any language.

### Example: Python Agent

```python
import subprocess, json

def ghost_put(ns, key, content, importance=0.5):
    subprocess.run([
        "ghost", "put", "-n", ns, "-k", key,
        "--importance", str(importance), content
    ])

def ghost_context(query, ns=None, budget=2000):
    cmd = ["ghost", "context", "-q", query, "--budget", str(budget)]
    if ns:
        cmd.extend(["-n", ns])
    result = subprocess.run(cmd, capture_output=True, text=True)
    return json.loads(result.stdout)
```

---

## Memory Lifecycle Patterns

### Pattern: Session Summarization

After N conversational exchanges, consolidate old episodic memories into a single semantic summary:

1. List episodic exchanges for the chat namespace
2. Keep the most recent 5 exchanges intact
3. Merge older exchanges into a summary
4. Store the summary as semantic memory
5. Delete the individual exchanges

This prevents episodic memory from growing unboundedly while preserving the gist.

### Pattern: Memory Review with Curate

Use `ghost_search` or `ghost_context` to surface memories, then `ghost_curate` to act on individual ones. This is the recommended workflow for LLM agents doing memory hygiene:

1. Query memories for a topic: `ghost_search(query="deployment")`
2. Review each result — is it still accurate? still useful?
3. Promote valuable ones: `ghost_curate(ns, key, op="promote")`
4. Archive outdated ones: `ghost_curate(ns, key, op="archive")`
5. Boost frequently-needed facts: `ghost_curate(ns, key, op="boost")`

This is more natural for LLMs than defining reflect rules, since agents reason better about individual memories than about policies.

### Pattern: Compaction-Triggered Reflect

When `ghost_context` returns `compaction_suggested: true`:

1. Run `ghost_reflect` to promote/decay/prune
2. Optionally run `ghost gc` to hard-delete expired memories
3. Re-query context — it should now fit better within budget

### Pattern: Dual Memory (MEMORY.md + Ghost)

When using Claude Code, both its built-in MEMORY.md and ghost can coexist:

- **MEMORY.md**: Quick-reference facts always loaded into context (user info, project paths, active tasks)
- **Ghost**: Deeper knowledge that benefits from search, scoring, and lifecycle management (debugging insights, architectural decisions, patterns)

MEMORY.md is your always-on scratchpad. Ghost is your searchable long-term knowledge base.

### Pattern: System Prompt Injection

For chat agents, load pinned memories into the system prompt on every request:

```go
// Load always-on context via Context() — Phase 1 loads pinned memories
result, _ := store.Context(ctx, memory.ContextParams{
    NS:     "agent:mybot",
    Query:  "",        // empty query = pinned only
    Budget: 2000,
})

systemPrompt := "## Core Knowledge\n"
for _, m := range result.Memories {
    systemPrompt += "- " + m.Content + "\n"
}
```

### Pattern: Per-Query Context Injection

Prepend relevant memories to the user message before sending to the LLM:

```go
result, _ := store.Context(ctx, memory.ContextParams{
    NS:    "agent:mybot",
    Query: userMessage,
    Tags:  []string{"chat:123"},
    Budget: 2000,
})

augmented := "[Relevant memories]\n"
for _, m := range result.Memories {
    augmented += "- " + m.Content + "\n"
}
augmented += "[End memories]\n\n" + userMessage
```

---

## Automated Memory via Claude Code Hooks

CLAUDE.md instructions tell the agent _when_ to use ghost, but agents tend to forget — especially during long debugging sessions where the focus is on solving the problem, not recording learnings. Claude Code **hooks** can automate memory capture as a safety net.

### How Hooks Work

Hooks are shell commands that fire on specific Claude Code lifecycle events. Relevant events for memory automation:

| Hook Event | When It Fires | Use For |
|------------|--------------|---------|
| `Stop` | Agent finishes responding to a prompt | Capture learnings from each turn |
| `PreCompact` | Before context window compression | Capture knowledge before it's lost in long sessions |

Both hooks run with `async: true` so they never block the user.

### Architecture: Shell Script + Headless Claude

The recommended approach uses `type: "command"` hooks that call shell scripts. Each script:

1. Reads hook input from stdin (JSON with `transcript_path` and/or `session_id`)
2. Extracts the session transcript (JSONL format)
3. Pipes it to `claude -p` (headless mode) for LLM-powered analysis
4. Executes the resulting `ghost put` CLI commands

This avoids relying on `type: "agent"` hooks (which may not inherit MCP servers) and keeps the integration self-contained via the ghost CLI.

### Setup

Add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PreCompact": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "~/.claude/hooks/ghost-precompact.sh",
            "timeout": 120000,
            "async": true
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "~/.claude/hooks/ghost-stop.sh",
            "timeout": 120000,
            "async": true
          }
        ]
      }
    ]
  }
}
```

### Hook Script (shared pattern)

Both hooks use the same pattern. The LLM outputs a **JSON array** (not shell commands) to avoid `eval`-related content corruption — backticks, braces, and angle brackets in memory content would otherwise be interpreted by the shell.

`~/.claude/hooks/ghost-precompact.sh`:

```bash
#!/usr/bin/env bash
# Ghost memory curator — PreCompact hook
# Reads hook input from stdin, extracts transcript, pipes to claude -p for analysis.
# LLM outputs JSON array; we parse with jq and call ghost put safely (no eval).
# Runs async so it doesn't block compaction.

set -euo pipefail

DEBUG_LOG="/tmp/ghost-precompact-debug.log"
HOOK_INPUT=$(cat)

echo "=== $(date -Iseconds) ===" >> "$DEBUG_LOG"
echo "$HOOK_INPUT" | jq '.' >> "$DEBUG_LOG" 2>&1 || echo "$HOOK_INPUT" >> "$DEBUG_LOG"

# Extract transcript path from hook input
TRANSCRIPT_PATH=$(echo "$HOOK_INPUT" | jq -r '.transcript_path // empty' 2>/dev/null)

if [ -z "$TRANSCRIPT_PATH" ]; then
  SESSION_ID=$(echo "$HOOK_INPUT" | jq -r '.session_id // empty' 2>/dev/null)
  if [ -n "$SESSION_ID" ]; then
    TRANSCRIPT_PATH=$(find ~/.claude/projects -name "${SESSION_ID}*.jsonl" 2>/dev/null | head -1)
    echo "Found transcript via session_id: $TRANSCRIPT_PATH" >> "$DEBUG_LOG"
  fi
fi

if [ -z "$TRANSCRIPT_PATH" ] || [ ! -f "$TRANSCRIPT_PATH" ]; then
  echo "No readable transcript found, skipping" >> "$DEBUG_LOG"
  exit 0
fi

echo "Processing transcript: $TRANSCRIPT_PATH" >> "$DEBUG_LOG"

TRANSCRIPT_TAIL=$(tail -100 "$TRANSCRIPT_PATH" 2>/dev/null || echo "")

if [ -z "$TRANSCRIPT_TAIL" ]; then
  echo "Transcript empty or unreadable" >> "$DEBUG_LOG"
  exit 0
fi

PROMPT='You are a memory curator for an AI coding agent. Analyze the following Claude Code session transcript (JSONL format) and extract memories at two confidence levels.

## Tier: stm (short-term memory)
Confirmed learnings worth remembering. Use for:
- Debugging insights (error → root cause → fix)
- Architecture or design decisions with rationale
- User corrections or stated preferences
- Non-obvious gotchas confirmed through experience
- Solutions that took multiple attempts to find

## Tier: sensory (raw observations)
Unconfirmed or partial observations that might be useful later. Use for:
- File paths, service names, or repo structure noticed during work
- Error messages encountered (even if not yet resolved)
- Patterns noticed but not yet confirmed across multiple instances
- Tools, commands, or workflows seen in use
- Context about what the session was working on

sensory memories are automatically decayed if never accessed, so err on the side of capturing them.

## Rules
- NEVER use tier "ltm" — long-term memory is only reached through promotion by the user or reflect lifecycle.
- If the session was trivial (just a greeting, a single file read, or a /clear), output an empty JSON array: []
- Include the project name in tags when inferrable from file paths or repo names (e.g. "project:ghost", "project:internal-api-campaigns-gql").

## Output format
Output a JSON array of objects. Each object has:
  - "key": descriptive kebab-case key like "spanner-empty-array-gotcha"
  - "kind": one of "semantic", "episodic", "procedural"
  - "priority": one of "low", "normal", "high", "critical"
  - "tier": one of "sensory", "stm"
  - "tags": comma-separated string like "learning,project:foo"
  - "content": the insight in one or two sentences (plain text, no backticks or shell metacharacters)

Example output:
[
  {"key": "redis-nil-vs-empty", "kind": "semantic", "priority": "high", "tier": "stm", "tags": "learning,debugging,project:myapp", "content": "Redis GET returns nil (not empty string) for missing keys. The Go redis client returns redis.Nil error, not an empty string."},
  {"key": "myapp-uses-redis-for-sessions", "kind": "episodic", "priority": "low", "tier": "sensory", "tags": "project:myapp", "content": "The myapp service stores user sessions in Redis with a 24h TTL, configured in src/config/redis.js."}
]

Output ONLY valid JSON. No markdown fences, no explanation.'

RESULT=$(echo "$TRANSCRIPT_TAIL" | claude -p --model claude-sonnet-4-6 "$PROMPT" 2>> "$DEBUG_LOG")

echo "Claude output:" >> "$DEBUG_LOG"
echo "$RESULT" >> "$DEBUG_LOG"

if [ -z "$RESULT" ]; then
  echo "No output from claude" >> "$DEBUG_LOG"
  exit 0
fi

# Validate JSON
if ! echo "$RESULT" | jq 'type' > /dev/null 2>&1; then
  echo "Invalid JSON output, skipping" >> "$DEBUG_LOG"
  exit 0
fi

COUNT=$(echo "$RESULT" | jq 'length')
if [ "$COUNT" -eq 0 ]; then
  echo "No learnings found" >> "$DEBUG_LOG"
  exit 0
fi

echo "Processing $COUNT learnings..." >> "$DEBUG_LOG"

# Iterate over each learning and call ghost put safely via jq extraction
echo "$RESULT" | jq -c '.[]' | while IFS= read -r item; do
  KEY=$(echo "$item" | jq -r '.key')
  KIND=$(echo "$item" | jq -r '.kind')
  PRIORITY=$(echo "$item" | jq -r '.priority')
  TIER=$(echo "$item" | jq -r '.tier // "stm"')
  TAGS=$(echo "$item" | jq -r '.tags')
  CONTENT=$(echo "$item" | jq -r '.content')

  # Never allow hooks to write directly to ltm
  if [ "$TIER" = "ltm" ]; then
    TIER="stm"
  fi

  echo "Storing: $KEY (tier=$TIER)" >> "$DEBUG_LOG"
  ghost put -n "agent:claude-code" -k "$KEY" --kind "$KIND" -p "$PRIORITY" --tier "$TIER" -t "$TAGS" "$CONTENT" >> "$DEBUG_LOG" 2>&1 || echo "Failed to store: $KEY" >> "$DEBUG_LOG"
done

echo "Done" >> "$DEBUG_LOG"
```

### Stop Hook Differences

`~/.claude/hooks/ghost-stop.sh` uses the same JSON-based pattern with two additions:

1. **Skips trivial sessions** — exits early if there's no `last_assistant_message` (instant exits, `/clear`) or if the transcript is under 50 lines.
2. **Samples head + tail** — takes the first 50 and last 150 lines of the transcript to capture the full arc of the session, since the interesting parts may be at the beginning.
3. **Unsets CLAUDECODE** — prevents nested Claude Code detection when calling `claude -p` from within a session.

### Design Notes

1. **Two-tier capture** — The LLM assigns each memory to either `sensory` (raw observations, unconfirmed patterns) or `stm` (confirmed learnings). Sensory memories are automatically decayed if never accessed, so the hooks can capture liberally without polluting long-term storage. LTM is never written directly by hooks — it's only reached through user-initiated promotion or reflect lifecycle rules.

2. **JSON output, not shell commands** — The original approach had the LLM output `ghost put` shell commands that were `eval`'d. This caused content corruption: backticks were interpreted as command substitution (e.g., `` `executeCampaignQuery` `` → "command not found"), braces triggered syntax errors, and one memory accidentally ran `npx next build` and stored 4,743 tokens of lint output. The JSON approach parses each field with `jq` and passes them as arguments, avoiding shell interpretation entirely.

3. **Shell scripts + `claude -p`** — More reliable than `type: "agent"` hooks because agent subprocesses may not inherit MCP servers. Shell scripts call the ghost CLI directly, which always works.

4. **Both hooks are async** — Neither blocks the user. PreCompact runs during compaction; Stop runs after the agent responds. Memory storage happens in the background.

5. **Transcript extraction** — Hook input provides `transcript_path` or `session_id`. The scripts try `transcript_path` first, then fall back to finding the JSONL file by session_id under `~/.claude/projects/`.

6. **Debug logs** — Both scripts log to `/tmp/ghost-{precompact,stop}-debug.log` for troubleshooting.

## Active Learning via Skill + Loop

Hooks capture learnings passively at lifecycle boundaries (compaction, session end). For **active, on-demand** capture, use a Claude Code skill that runs within the session with full tool access.

### The `/ghost-learn` Skill

A custom skill (`~/.claude/skills/ghost-learn/SKILL.md`) that reviews the current session or repo for learnings and stores them via ghost MCP tools directly — no shell escaping, no transcript parsing.

**Modes:**

| Mode | What it does |
|------|-------------|
| `/ghost-learn` or `/ghost-learn chat` | Review the current conversation for learnings |
| `/ghost-learn repo` | Scan the current repo for conventions, patterns, and architecture |
| `/ghost-learn both` | Do both |

**Workflow:**

1. Calls `ghost_context` to check existing memories (prevents duplicates)
2. Reviews conversation for stm-worthy learnings (debugging insights, decisions, corrections) and sensory-worthy observations (file paths, error messages, patterns noticed)
3. For repo mode, scans key files (README, Makefile, CLAUDE.md, etc.) for non-obvious conventions
4. Stores via `ghost_put` MCP tool with appropriate tier, kind, and project tags
5. Reports a summary of what was captured

**Same tier policy as hooks:** sensory for raw observations, stm for confirmed learnings, never ltm.

### Periodic Capture with `/loop`

Combine the skill with `/loop` for periodic mid-session capture:

```
/loop 15m /ghost-learn
```

This fills the gap where hooks don't fire — long sessions that never hit context limits. Each loop run focuses on what's new in the conversation since the last run.

### Hooks vs Skill: When to Use Which

| | Hooks (passive) | Skill (active) |
|--|----------------|----------------|
| **Trigger** | Lifecycle events (compaction, stop) | On-demand or periodic via `/loop` |
| **Context** | Transcript tail only (100-200 lines) | Full conversation + repo access |
| **Tools** | Ghost CLI via shell | Ghost MCP tools directly |
| **Best for** | Safety net — captures what the agent forgot to store | Deliberate review — richer analysis with repo exploration |
| **Tier policy** | sensory or stm only (ltm blocked) | Same — sensory or stm only |

The two approaches are complementary: hooks ensure nothing is lost even if the agent forgets, while the skill provides richer, more contextual capture when invoked.

## Reflect Rules

Ghost ships with 7 built-in rules (including sensory tier lifecycle and similarity merge). Pinned memories are exempt from all rules. You can add custom rules for your use case:

```bash
# Archive procedural memories older than 30 days with low access
ghost rule set \
  --name "archive-old-procedures" \
  --cond-tier stm \
  --cond-kind procedural \
  --cond-age-gt 720 \
  --cond-access-lt 2 \
  --action ARCHIVE

# Promote high-importance memories quickly
ghost rule set \
  --name "fast-promote-important" \
  --cond-tier stm \
  --cond-age-gt 12 \
  --cond-access-gt 2 \
  --action PROMOTE

# Merge similar episodic memories (deduplication)
ghost rule set \
  --name "merge-similar-episodes" \
  --cond-tier stm \
  --cond-kind episodic \
  --cond-similarity-gt 0.85 \
  --action MERGE \
  --action-params '{"strategy": "keep_highest_importance"}'
```

Rules are evaluated in two passes during `ghost reflect`:
1. **Per-memory pass** — first matching rule wins per memory (standard conditions: tier, age, access count, etc.)
2. **Similarity merge pass** — rules with `--cond-similarity-gt` compare embeddings pairwise and consolidate similar memories. The survivor (highest importance) keeps the union of tags and summed access/utility counts. Absorbed memories are soft-deleted with `merged_into` links.

The built-in `sys-merge-similar` rule automatically deduplicates STM memories with >0.9 cosine similarity.
