# Ghost Integration Guide

How to integrate Ghost into custom agents, bots, and scripts. For Claude Code specifically, see [quickstart-claude-code.md](quickstart-claude-code.md).

## Integration Methods

| Method | Best For | Setup |
|--------|----------|-------|
| **Go Library** | Custom Go agents, Telegram bots, orchestrators | `import memory "github.com/rcliao/ghost"` |
| **CLI** | Shell scripts, cron jobs, any language via subprocess | `ghost put`, `ghost search`, `ghost context` |
| **MCP Server** | Any MCP-compatible client | `ghost mcp-serve` as stdio transport |

---

## Go Library

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
    Kind:       "semantic",       // semantic | episodic | procedural
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

For conversational agents, store exchanges as episodic memory with TTL:

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

### Curating Memories

```go
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

### System Prompt Injection

Load pinned memories into the system prompt on every request:

```go
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

### Per-Query Context Injection (RAG)

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

## CLI Integration

For non-Go agents, use the CLI via subprocess. All output is JSON by default.

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

# Manage edges
ghost edge -n "project:myapp" --from-key db-decision --to-key db-migration -r depends_on
ghost edge -n "project:myapp" --from-key db-decision --list

# Run lifecycle maintenance
ghost reflect --ns "project:myapp"
ghost reflect --dry-run  # preview without applying
```

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

### Session Summarization

After N conversational exchanges, consolidate old episodic memories into a single semantic summary:

1. List episodic exchanges for the chat namespace
2. Keep the most recent 5 exchanges intact
3. Merge older exchanges into a summary
4. Store the summary as semantic memory
5. Delete the individual exchanges

### Memory Review with Curate

Use `ghost_search` or `ghost_context` to surface memories, then `ghost_curate` to act on specific ones:

1. Query memories for a topic: `ghost_search(query="deployment")`
2. Review each result — is it still accurate? still useful?
3. Promote valuable ones: `ghost_curate(ns, key, op="promote")`
4. Archive outdated ones: `ghost_curate(ns, key, op="archive")`
5. Boost frequently-needed facts: `ghost_curate(ns, key, op="boost")`

### Compaction-Triggered Reflect

When `ghost_context` returns `compaction_suggested: true`:

1. Run `ghost_reflect` to promote/decay/prune
2. Optionally run `ghost gc` to hard-delete expired memories
3. Re-query context — it should now fit better within budget

### Consolidation Workflow

When many memories accumulate on a topic:

1. Run `ghost clusters -n <ns>` to discover groups
2. Review each cluster — do they belong together?
3. Write a summary and consolidate:
   ```bash
   ghost consolidate -n agent:mybot --summary-key auth-overview \
     --keys "auth-jwt,auth-expiry,auth-cookies" \
     --content "Auth: JWT+RSA256, 24h expiry, refresh via cookies"
   ```
4. The summary gets `contains` edges to sources; in context assembly, the summary is preferred and children are suppressed

---

## Reflect Rules

Ghost ships with 7 built-in rules. Pinned memories are exempt from all rules. Add custom rules:

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
1. **Per-memory pass** — first matching rule wins per memory
2. **Similarity merge pass** — rules with `--cond-similarity-gt` compare embeddings pairwise and consolidate

---

## Key Concepts

**Namespaces** — Agent identity. Each namespace is isolated. Use `agent:<name>` for per-agent memory.

**Tags** — First-class metadata: `identity`, `lore`, `project:<name>`, `chat:<id>`, `learning`, `convention`, `user:<name>`.

**Pinned memories** — Always loaded in context (Phase 1). Exempt from lifecycle decay.

**Compaction signal** — `ghost_context` returns `compaction_suggested: true` when budget is exhausted. Trigger `ghost_reflect`.

**Token budgets** — `ghost_context` accepts a `budget` parameter (default 4000 tokens).

**Curate vs Reflect** — `curate` acts on a specific memory (intent-driven). `reflect` applies rules across all memories (rule-driven).
