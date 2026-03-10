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

### PreCompact Hook Script

`~/.claude/hooks/ghost-precompact.sh`:

```bash
#!/usr/bin/env bash
# Ghost memory curator — PreCompact hook
# Reads hook input from stdin, extracts transcript, pipes to claude -p for analysis.
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

# Extract the last ~100 lines of the transcript for analysis
TRANSCRIPT_TAIL=$(tail -100 "$TRANSCRIPT_PATH" 2>/dev/null || echo "")

if [ -z "$TRANSCRIPT_TAIL" ]; then
  echo "Transcript empty or unreadable" >> "$DEBUG_LOG"
  exit 0
fi

PROMPT='You are a memory curator. Analyze the following Claude Code session transcript (JSONL format) for notable learnings worth remembering long-term.

Look for:
- Debugging insights (error encountered → root cause found → fix applied)
- Architecture or design decisions with rationale
- User corrections or stated preferences
- Non-obvious gotchas or patterns discovered
- Solutions that took multiple attempts to find

If the session was routine (simple file reads, straightforward edits, no surprises), output NOTHING.

If you find notable learnings, output ONLY shell commands to store them, one per learning.
The ghost CLI syntax is:
  ghost put -n "agent:claude-code" -k "<descriptive-key>" --kind semantic -p high -t learning "<the insight in one or two sentences>"

Available flags:
  -n, --ns        Namespace (always use "agent:claude-code")
  -k, --key       Descriptive key like "spanner-empty-array-gotcha"
  --kind          Kind: semantic, episodic, procedural
  -p, --priority  Priority: low, normal, high, critical
  -t, --tags      Comma-separated tags
  --tier          Storage tier: sensory, stm (default), ltm

Use priority "high" for important learnings, "critical" for must-never-forget decisions.
Output ONLY the ghost put commands, nothing else. No explanation, no markdown.'

COMMANDS=$(echo "$TRANSCRIPT_TAIL" | claude -p --model claude-sonnet-4-6 "$PROMPT" 2>> "$DEBUG_LOG")

echo "Claude output:" >> "$DEBUG_LOG"
echo "$COMMANDS" >> "$DEBUG_LOG"

if [ -z "$COMMANDS" ]; then
  echo "No learnings found" >> "$DEBUG_LOG"
  exit 0
fi

# Execute only lines starting with "ghost put"
echo "$COMMANDS" | grep '^ghost put' | while IFS= read -r cmd; do
  echo "Executing: $cmd" >> "$DEBUG_LOG"
  eval "$cmd" >> "$DEBUG_LOG" 2>&1 || echo "Failed: $cmd" >> "$DEBUG_LOG"
done

echo "Done" >> "$DEBUG_LOG"
```

### Stop Hook Script

`~/.claude/hooks/ghost-stop.sh` — Same pattern, but reads more transcript lines (200 vs 100) since this is the final chance to capture learnings from the session:

```bash
#!/usr/bin/env bash
# Ghost memory curator — Stop hook
# Same pattern as PreCompact but reads more transcript (last 200 lines).
# Runs async so it doesn't block session exit.

set -euo pipefail

DEBUG_LOG="/tmp/ghost-stop-debug.log"
HOOK_INPUT=$(cat)

# ... (same transcript extraction logic as PreCompact) ...

# Use more lines for Stop hook since this is the final chance
TRANSCRIPT_TAIL=$(tail -200 "$TRANSCRIPT_PATH" 2>/dev/null || echo "")

# ... (same prompt and execution logic) ...

# NOTE: Unset CLAUDECODE env var to avoid nested Claude Code detection
COMMANDS=$(echo "$TRANSCRIPT_TAIL" | unset CLAUDECODE 2>/dev/null; claude -p --model claude-sonnet-4-6 "$PROMPT" 2>> "$DEBUG_LOG")
```

The Stop hook includes `unset CLAUDECODE` before calling `claude -p` to prevent nested Claude Code detection issues.

### Design Notes

1. **Shell scripts + `claude -p`** — More reliable than `type: "agent"` hooks because agent subprocesses may not inherit MCP servers. Shell scripts call the ghost CLI directly, which always works.

2. **Both hooks are async** — Neither blocks the user. PreCompact runs during compaction; Stop runs after the agent responds. Memory storage happens in the background.

3. **Transcript extraction** — Hook input provides `transcript_path` or `session_id`. The scripts try `transcript_path` first, then fall back to finding the JSONL file by session_id under `~/.claude/projects/`.

4. **Safety** — Only lines starting with `ghost put` are executed. The LLM is prompted to output nothing for routine sessions, keeping noise low.

5. **Debug logs** — Both scripts log to `/tmp/ghost-{precompact,stop}-debug.log` for troubleshooting.

## Reflect Rules

Ghost ships with 6 built-in rules (including sensory tier lifecycle). Pinned memories are exempt from all rules. You can add custom rules for your use case:

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
```

Rules are evaluated in priority order during `ghost reflect`. First matching rule wins per memory.
