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

Ghost's MCP server includes built-in instructions that tell the agent what ghost is and how to use it. However, agents tend to under-use ghost_put unless explicitly instructed. The most effective way to encourage memory writes is through a **CLAUDE.md** file in your project root.

Example CLAUDE.md section:

```markdown
## Ghost Memory (MCP)

You have access to a persistent memory system via Ghost MCP tools.

### When to write (ghost_put)
Store memories when you encounter:
- Project decisions and architecture choices
- Debugging insights and root causes
- User corrections (store the correct information)
- Patterns and conventions discovered
- Cross-project knowledge

Use descriptive keys (e.g. "auth-flow-decision", "db-migration-gotcha").
Set importance 0.6-0.8 for most learnings, 0.9+ for critical decisions.

### When to retrieve (ghost_context)
At the start of a task, query ghost for relevant context:
  ghost_context(query="<current task>", ns="project:<name>", budget=2000)

### When to curate (ghost_curate)
Use ghost_curate to act on individual memories:
  ghost_curate(ns="project:myapp", key="old-pattern", op="archive")

Operations: promote (tier up), demote (tier down), boost (importance +0.2),
diminish (importance -0.2), archive (→dormant), delete (soft-delete).

Use this when reviewing memories from ghost_context or ghost_search and
deciding which ones to keep, promote, or remove.

### When to reflect (ghost_reflect)
Run ghost_reflect when ghost_context returns compaction_suggested: true,
or after a long session with many stored learnings.
```

### Key Behaviors

**Namespace conventions** — The server instructions suggest these conventions:
- `identity` — core agent identity
- `lore` — background knowledge
- `user:<name>` — per-user preferences
- `<app>:<scope>` — app-specific data

**Compaction signal** — When `ghost_context` exhausts its budget and skips candidates, the response includes `"compaction_suggested": true`. The agent should then call `ghost_reflect` to promote/decay/prune memories and free up space.

**Token budgets** — `ghost_context` accepts a `budget` parameter (default 4000 tokens). The agent can tune this based on available context window space.

### Tips for Better Memory Usage

1. **Be specific in CLAUDE.md** — Generic instructions like "use ghost" don't work well. List concrete scenarios and examples.

2. **Namespace per project** — Use `project:<name>` namespaces so memories are scoped and searchable. Cross-project knowledge can go in `coder:learnings` or similar shared namespaces.

3. **Importance scores matter** — They affect retrieval ranking. Use 0.5 for general notes, 0.7-0.8 for useful learnings, 0.9+ for critical decisions.

4. **Tier selection** — Default tier is `stm` (subject to decay). Use `sensory` for raw transient observations (auto-deleted if unaccessed). Set `tier: "ltm"` for knowledge that should persist long-term. Only use `identity` tier for core agent identity facts.

5. **Reflect periodically** — The reflect cycle promotes frequently-accessed STM memories to LTM and decays unused ones. Without it, STM memories accumulate without curation.

### Curating Memories (ghost_curate)

`ghost_curate` provides direct, instance-level control over individual memories. Unlike `ghost_reflect` (which applies rules to all memories), `ghost_curate` lets you act on a specific memory by namespace and key.

**Operations:**

| Op | Effect |
|----|--------|
| `promote` | Tier up: dormant → stm → ltm → identity |
| `demote` | Tier down: identity → ltm → stm → dormant |
| `boost` | Importance +0.2 (caps at 1.0) |
| `diminish` | Importance -0.2 (floors at 0.1) |
| `archive` | Move to dormant tier |
| `delete` | Soft-delete (recoverable) |

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
    Tier:       "ltm",            // sensory | stm (default) | ltm | identity | dormant
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

For conversational agents, store exchanges as episodic memory with TTL:

```go
store.Put(ctx, memory.PutParams{
    NS:         "myapp:chat:123",
    Key:        fmt.Sprintf("exchange-%d", time.Now().UnixMilli()),
    Content:    fmt.Sprintf("User: %s\nAssistant: %s", userMsg, response),
    Kind:       "episodic",
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
    Op:  "promote",  // promote | demote | boost | diminish | archive | delete
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

For chat agents, load identity/lore memories into the system prompt on every request:

```go
// Load always-on identity context
identityMems, _ := store.List(ctx, memory.ListParams{NS: "identity", Limit: 100})
loreMems, _ := store.List(ctx, memory.ListParams{NS: "lore", Limit: 100})

systemPrompt := "## Identity\n"
for _, m := range identityMems {
    systemPrompt += "- " + m.Content + "\n"
}
systemPrompt += "\n## Lore\n"
for _, m := range loreMems {
    systemPrompt += "- " + m.Content + "\n"
}
```

### Pattern: Per-Query Context Injection

Prepend relevant memories to the user message before sending to the LLM:

```go
result, _ := store.Context(ctx, memory.ContextParams{
    NS:    "myapp:chat:123*",
    Query: userMessage,
    Budget: 2000,
})

augmented := "[Relevant memories]\n"
for _, m := range result.Memories {
    augmented += "- " + m.Content + "\n"
}
augmented += "[End memories]\n\n" + userMessage
```

---

## Reflect Rules

Ghost ships with 7 built-in rules (including sensory tier lifecycle). You can add custom rules for your use case:

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
