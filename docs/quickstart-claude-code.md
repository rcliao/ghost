# Ghost + Claude Code Setup Guide

Everything you need to use Ghost as persistent memory in Claude Code — MCP server, hooks, CLAUDE.md instructions, and the `/ghost-learn` skill.

---

## 1. Install Ghost and Add MCP Server

```bash
# Install ghost binary
go install github.com/rcliao/ghost/cmd/ghost@latest

# Add as user-scoped MCP server (available in all projects)
claude mcp add --scope user --transport stdio ghost -- ghost mcp-serve
```

Or for project-scoped (add to `.mcp.json` in repo root):

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

This exposes 6 tools to the agent:

| Tool | Purpose |
|------|---------|
| `ghost_put` | Store or update a memory (auto-links similar memories via edges) |
| `ghost_search` | Full-text search with ranking |
| `ghost_context` | Budget-aware context assembly with edge expansion |
| `ghost_edge` | Create, remove, or list weighted edges between memories |
| `ghost_curate` | Instance-level lifecycle actions on a single memory |
| `ghost_reflect` | Run lifecycle rules across all memories (promote, decay, prune, edge decay) |

---

## 2. Add Hooks

Hooks automate memory capture and retrieval at Claude Code lifecycle boundaries. Add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/path/to/.claude/hooks/ghost-session-start.sh",
            "timeout": 15000
          }
        ]
      }
    ],
    "PreCompact": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/path/to/.claude/hooks/ghost-precompact.sh",
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
            "command": "/path/to/.claude/hooks/ghost-stop.sh",
            "timeout": 120000,
            "async": true
          }
        ]
      }
    ]
  }
}
```

### Hook Types

There are two categories: **capture** hooks (extract learnings) and **retrieval** hooks (inject context).

#### Capture Hooks

| Hook Event | When It Fires | Use For |
|------------|--------------|---------|
| `Stop` | Agent finishes responding | Capture learnings from each turn |
| `PreCompact` | Before context compression | Capture knowledge before it's summarized away |

Both run with `async: true` so they never block the user.

#### Retrieval Hooks

| Hook Event | When It Fires | Use For |
|------------|--------------|---------|
| `SessionStart` | Session begins, resumes, or recovers from compaction | Load ghost context at session start |
| `UserPromptSubmit` | Before Claude processes each user message | Inject per-prompt relevant memories (RAG-style) |
| `SessionEnd` | Session terminates (exit, /clear, logout) | Final sync, cleanup, or session summary |

Retrieval hooks output to stdout with exit code 0 — Claude Code injects that output as `additionalContext` into the agent's context window.

#### Choosing a Hook Strategy

| Strategy | Hooks Used | Tradeoff |
|----------|-----------|----------|
| **Capture only** | `Stop` + `PreCompact` | Agent must call `ghost_context` explicitly |
| **Capture + session retrieval** (recommended) | Above + `SessionStart` | Agent starts with context loaded; no per-prompt overhead |
| **Full RAG** | Above + `UserPromptSubmit` | Every prompt gets relevant memories; adds latency per turn |
| **Full lifecycle** | All above + `SessionEnd` | Complete coverage; session summaries stored on exit |

### SessionStart Hook

Outputs raw ghost context as `additionalContext`. Does NOT call `claude -p` — this avoids infinite loop risk.

```bash
#!/usr/bin/env bash
# ~/.claude/hooks/ghost-session-start.sh
set -euo pipefail

# Guard against recursive invocation
if [ "${GHOST_SESSION_HOOK:-}" = "1" ]; then
  exit 0
fi
export GHOST_SESSION_HOOK=1

MEMORIES=""

# Agent-level memories
AGENT_RAW=$(ghost context "general session context" -n "agent:claude-code" --budget 1500 2>/dev/null || echo "{}")
AGENT_MEM=$(echo "$AGENT_RAW" | jq -r '.memories[]? | "[\(.key)] \(.content)"' 2>/dev/null || echo "")
if [ -n "$AGENT_MEM" ]; then
  MEMORIES="## Agent Knowledge\n$AGENT_MEM"
fi

if [ -z "$MEMORIES" ]; then
  exit 0
fi

echo -e "[Ghost Memory — Session Start]\n$MEMORIES\n[End Ghost Memory]"
```

**Matcher options for `SessionStart`:**
- `"startup"` — fresh session start
- `"resume"` — resuming a previous session
- `"compact"` — session recovering after context compaction (particularly useful — restores knowledge the compaction summary may have dropped)
- No matcher — fires on all session types

### PreCompact / Stop Hooks (Capture)

Both use the same pattern: read transcript from stdin, pipe to `claude -p` for LLM-powered extraction, store results via `ghost put` CLI.

The LLM outputs a **JSON array** (not shell commands) to avoid content corruption — backticks, braces, and angle brackets would otherwise be interpreted by the shell.

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

**Stop hook differences:** `ghost-stop.sh` uses the same pattern with:
1. Skips trivial sessions (no `last_assistant_message` or transcript under 50 lines)
2. Samples head + tail (first 50 + last 150 lines to capture the full session arc)
3. Unsets `CLAUDECODE` to prevent nested detection when calling `claude -p`

### UserPromptSubmit Hook (Per-Prompt RAG)

Injects relevant memories before each prompt. Adds latency, so best for deep knowledge base domains:

```bash
#!/usr/bin/env bash
# ~/.claude/hooks/ghost-user-prompt.sh
set -euo pipefail

HOOK_INPUT=$(cat)
QUERY=$(echo "$HOOK_INPUT" | jq -r '.user_prompt // empty' 2>/dev/null)

if [ -z "$QUERY" ] || [ ${#QUERY} -lt 10 ]; then
  exit 0  # Skip trivial prompts
fi

ghost context -n "agent:claude-code" -q "$QUERY" --budget 1000 --format plain 2>/dev/null || true
```

### Design Notes

1. **Two-tier capture** — sensory (raw observations) or stm (confirmed learnings). LTM only via promotion or reflect.
2. **JSON output, not shell commands** — avoids content corruption from backticks, braces, angle brackets.
3. **Shell scripts + `claude -p`** — more reliable than `type: "agent"` hooks (which may not inherit MCP servers).
4. **Both capture hooks are async** — neither blocks the user.
5. **Debug logs** — both scripts log to `/tmp/ghost-{precompact,stop}-debug.log`.

---

## 3. Add CLAUDE.md Instructions

Add to `~/.claude/CLAUDE.md` (global) or project CLAUDE.md to teach the agent when to use ghost. **Use strong, directive language** — soft instructions like "consider using ghost" get ignored.

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

### When to link (ghost_edge)
Use ghost_edge to create associations between related memories:
  ghost_edge(ns="agent:claude-code", from_key="jwt-config", to_key="auth-overview", rel="depends_on")

Relation types: relates_to, contradicts, depends_on, refines, contains, merged_into.
Edges are auto-created on put when embedding similarity is high, but manual edges
for contradicts/depends_on/refines capture relationships embeddings can't.

### When to consolidate
When many memories exist about the same topic, use the full consolidation workflow:

1. Run ghost_reflect — it links similar memories and returns linked_clusters
2. Check ghost clusters to see all groups:
   ghost_edge(ns="agent:claude-code", from_key="some-key", op="list")
   or via CLI: ghost clusters -n agent:claude-code
3. Review the cluster and write a summary, then consolidate:
   ghost consolidate -n agent:claude-code --summary-key auth-overview \
     --keys "auth-jwt,auth-expiry,auth-cookies" \
     --content "Auth overview: JWT+RSA256, 24h expiry, refresh via cookies"

This creates a summary memory with `contains` edges to each source memory.
In context assembly, the summary is preferred over its children via parent
boosting (summaries appear even when children match the query), and children
are automatically suppressed — reducing redundancy and saving token budget.
All original memories are preserved (lossless, LCM-like compaction).

### When to reflect (ghost_reflect)
Run ghost_reflect when ghost_context returns compaction_suggested: true,
or after a long session with many stored learnings.
Reflect links similar memories (non-destructive), decays unused edges,
and prunes very weak ones. Check linked_clusters in the response to see
which memory groups could benefit from consolidation.
```

**Why the strong language matters:** LLM agents respond to directive framing. "MUST" and "Do NOT" create behavioral anchors that generic suggestions don't. The `ghost_context` call before work is the single highest-value habit.

---

## 4. Active Learning: `/ghost-learn` Skill

Hooks capture learnings passively at lifecycle boundaries. For **active, on-demand** capture, use a Claude Code skill.

Create `~/.claude/skills/ghost-learn/SKILL.md`:

| Mode | What it does |
|------|-------------|
| `/ghost-learn` or `/ghost-learn chat` | Review the current conversation for learnings |
| `/ghost-learn repo` | Scan the current repo for conventions, patterns, and architecture |
| `/ghost-learn both` | Do both |

**Workflow:**
1. Calls `ghost_context` to check existing memories (prevents duplicates)
2. Reviews conversation for stm-worthy learnings and sensory-worthy observations
3. For repo mode, scans key files (README, Makefile, CLAUDE.md, etc.)
4. Stores via `ghost_put` MCP tool with appropriate tier, kind, and project tags

Combine with `/loop` for periodic mid-session capture:

```
/loop 15m /ghost-learn
```

### Hooks vs Skill

| | Hooks (passive) | Skill (active) |
|--|----------------|----------------|
| **Trigger** | Lifecycle events (compaction, stop) | On-demand or periodic via `/loop` |
| **Context** | Transcript tail only (100-200 lines) | Full conversation + repo access |
| **Tools** | Ghost CLI via shell | Ghost MCP tools directly |
| **Best for** | Safety net — captures what the agent forgot | Deliberate review — richer analysis |

The two approaches are complementary.

---

## 5. Verify

Start a new Claude Code session — you should see ghost memories injected at startup. The MCP tools (`ghost_put`, `ghost_edge`, etc.) should be available.

---

## Tips

1. **Be specific in CLAUDE.md** — Generic instructions like "use ghost" don't work. List concrete scenarios.
2. **Use tags for scoping** — `project:<name>`, `chat:<id>` for filtering in search and context.
3. **Importance scores matter** — 0.5 for general notes, 0.7-0.8 for useful learnings, 0.9+ for critical decisions.
4. **Pin critical memories** — `pinned: true` for core identity, critical preferences, and always-on knowledge.
5. **Reflect periodically** — Promotes frequently-accessed STM to LTM, decays unused memories, links similar ones.
6. **Dual memory works** — Claude Code's MEMORY.md (always-on scratchpad) + Ghost (searchable long-term knowledge base) coexist naturally.
