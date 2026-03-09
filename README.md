# ghost

Persistent memory for AI agents. Text in, text out. SQLite-backed, single binary, no server.

Part of the [Ghost in the Shell](https://github.com/rcliao?tab=repositories&q=shell-) ecosystem.

## Install

```bash
go install github.com/rcliao/ghost/cmd/ghost@latest
```

## Quick Start

```bash
# Store a memory
ghost put -n "identity" -k "name" "Pikamini — a girl pikachu plush"

# Store with priority
ghost put -n "user:ev" -k "allergies" -p critical "Allergic to peanuts"

# Store with TTL (auto-expires)
ghost put -n "myapp:session" -k "token" --ttl 24h "abc123"

# Pipe content from stdin
cat session-notes.md | ghost put -n "myapp:logs" -k "session-2026-02-16" --kind episodic

# Retrieve latest version
ghost get -n "identity" -k "name"

# Get all versions
ghost get -n "identity" -k "name" --history

# Get specific version
ghost get -n "identity" -k "name" -v 1

# List all memories in a namespace
ghost list -n "identity"

# List with filters
ghost list -n "myapp:logs" --kind episodic --tags "deploy,infra"

# List keys only
ghost list -n "myapp:logs" --keys-only

# Search memories
ghost search -n "identity" "pikachu"
ghost search "deploy"

# Database stats
ghost stats

# Export memories
ghost export -n "identity" > backup.json

# Import memories
ghost import < backup.json

# Soft-delete (recoverable)
ghost rm -n "lore" -k "old-thing"

# Hard-delete all versions (permanent)
ghost rm -n "lore" -k "old-thing" --all-versions --hard
```

## Namespace Conventions

Namespaces use `:` as separator. Ghost recommends these conventions:

| Namespace | Purpose |
|-----------|---------|
| `identity` | Core agent identity — name, personality, appearance |
| `lore` | Background knowledge, relationships, trivia |
| `user:<name>` | Per-user preferences and context |
| `<app>:<scope>` | App-specific data (e.g. `shell:chat:123`, `coder:learnings`) |

Well-known namespaces (`identity`, `lore`, `user:*`) are app-agnostic and shared across all apps using the same ghost DB. App-scoped namespaces are isolated by prefix. See [Architecture](docs/ARCHITECTURE.md) for details.

## Commands

| Command  | Description |
|----------|-------------|
| `put`    | Store a memory (positional arg or stdin) |
| `get`    | Retrieve a memory by namespace and key |
| `list`   | List memories with filters |
| `search` | Search memory content by keyword/substring |
| `rm`     | Soft-delete or hard-delete a memory |
| `stats`  | Show database statistics |
| `export` | Export memories as JSON |
| `import` | Import memories from JSON (stdin) |

## Storage

Database location (in order of precedence):
1. `--db` flag
2. `$GHOST_DB` environment variable (also supports legacy `$AGENT_MEMORY_DB`)
3. `~/.ghost/memory.db`

## Output

All output is JSON by default. Pipe to `jq` for pretty-printing:

```bash
ghost list -n "project:myapp" | jq .
```

## Versioning

Storing to an existing key creates a new version. Old versions are preserved:

```bash
ghost put -n "ns" -k "config" "version 1"
ghost put -n "ns" -k "config" "version 2"
ghost get -n "ns" -k "config"           # returns v2
ghost get -n "ns" -k "config" --history  # returns [v2, v1]
```

## TTL / Expiry

Memories can have a time-to-live. Expired memories are automatically filtered from `list`, `get`, and `search` results:

```bash
ghost put -n "session" -k "cache" --ttl 7d "temporary data"
ghost put -n "session" -k "token" --ttl 24h "short-lived"
```

Supported formats: `7d` (days), `24h` (hours), `30m` (minutes), `60s` (seconds).

## Chunking

Long content is automatically split into chunks for search indexing. Chunks are internal — you always get back full memory content. Search queries match across chunks too.

## Dependencies

- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — Pure Go SQLite (no CGo)
- [github.com/oklog/ulid/v2](https://github.com/oklog/ulid) — ULID generation
- [github.com/spf13/cobra](https://github.com/spf13/cobra) — CLI framework

## License

MIT
