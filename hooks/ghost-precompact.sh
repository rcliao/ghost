#!/usr/bin/env bash
# Ghost memory capture — PreCompact hook
# Reads transcript, pipes to claude -p for LLM-powered extraction,
# stores results via ghost put CLI. Runs async.
#
# Configure via environment variables:
#   GHOST_BIN       — path to ghost binary (default: ghost on PATH)
#   GHOST_AGENT_NS  — agent namespace (default: agent:claude-code)
#   GHOST_LLM_MODEL — model for extraction (default: claude-sonnet-4-6)
set -uo pipefail

GHOST="${GHOST_BIN:-ghost}"
AGENT_NS="${GHOST_AGENT_NS:-agent:claude-code}"
LLM_MODEL="${GHOST_LLM_MODEL:-claude-sonnet-4-6}"
DEBUG_LOG="/tmp/ghost-precompact-debug.log"

# Trap errors so Claude Code gets stderr output instead of silent failure
trap 'echo "ghost-precompact.sh failed at line $LINENO" >&2; echo "Error at line $LINENO" >> "$DEBUG_LOG"' ERR

HOOK_INPUT=$(cat)

echo "=== $(date -Iseconds) ===" >> "$DEBUG_LOG"

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

# Truncate to ~60k chars to avoid "prompt too long" with claude -p
TRANSCRIPT_TAIL=$(tail -70 "$TRANSCRIPT_PATH" 2>/dev/null | head -c 60000 || echo "")

if [ -z "$TRANSCRIPT_TAIL" ]; then
  echo "Transcript empty or unreadable" >> "$DEBUG_LOG"
  exit 0
fi

PROMPT='You are a memory curator for an AI coding agent. Analyze the following Claude Code session transcript (JSONL format) and extract memories at two confidence levels.

## Tier: stm (short-term memory)
Confirmed learnings worth remembering. Use for:
- Debugging insights (error -> root cause -> fix)
- Architecture or design decisions with rationale
- User corrections or stated preferences
- Non-obvious gotchas confirmed through experience

## Tier: sensory (raw observations)
Unconfirmed or partial observations. Use for:
- File paths, service names, or repo structure noticed
- Error messages encountered (even if not yet resolved)
- Patterns noticed but not yet confirmed
- Context about what the session was working on

sensory memories are automatically decayed if never accessed, so err on the side of capturing them.

## Rules
- NEVER use tier "ltm" — long-term memory is only reached through promotion.
- If the session was trivial (just a greeting, a single file read, or a /clear), output an empty JSON array: []
- Include the project name in tags when inferrable from file paths or repo names.

## Output format
Output a JSON array of objects. Each object has:
  - "key": descriptive kebab-case key
  - "kind": one of "semantic", "episodic", "procedural"
  - "priority": one of "low", "normal", "high", "critical"
  - "tier": one of "sensory", "stm"
  - "tags": comma-separated string like "learning,project:foo"
  - "content": the insight in one or two sentences (plain text)

Output ONLY valid JSON. No markdown fences, no explanation.'

RESULT=$(echo "$TRANSCRIPT_TAIL" | claude -p --no-session-persistence --model "$LLM_MODEL" "$PROMPT" 2>> "$DEBUG_LOG")

echo "Claude output:" >> "$DEBUG_LOG"
echo "$RESULT" >> "$DEBUG_LOG"

if [ -z "$RESULT" ]; then
  echo "No output from claude" >> "$DEBUG_LOG"
  exit 0
fi

# Extract JSON array: strip fences, then grab from first [ to last ]
RESULT=$(echo "$RESULT" | sed '/^```/d')
RESULT=$(echo "$RESULT" | sed -n '/^\[/,/^\]/p')

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
  $GHOST put -n "$AGENT_NS" -k "$KEY" --kind "$KIND" -p "$PRIORITY" --tier "$TIER" -t "$TAGS" "$CONTENT" >> "$DEBUG_LOG" 2>&1 || echo "Failed to store: $KEY" >> "$DEBUG_LOG"
done

echo "Done" >> "$DEBUG_LOG"
