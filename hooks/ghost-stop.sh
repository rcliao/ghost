#!/usr/bin/env bash
# Ghost memory capture — Stop hook
# Captures learnings when Claude finishes responding, then consolidates
# them under a session summary node for hierarchical recall.
#
# Configure via environment variables:
#   GHOST_BIN       — path to ghost binary (default: ghost on PATH)
#   GHOST_AGENT_NS  — agent namespace (default: agent:claude-code)
#   GHOST_LLM_MODEL — model for extraction (default: claude-sonnet-4-6)
set -uo pipefail

GHOST="${GHOST_BIN:-ghost}"
AGENT_NS="${GHOST_AGENT_NS:-agent:claude-code}"
LLM_MODEL="${GHOST_LLM_MODEL:-claude-sonnet-4-6}"
DEBUG_LOG="/tmp/ghost-stop-debug.log"

# Trap errors so Claude Code gets stderr output instead of silent failure
trap 'echo "ghost-stop.sh failed at line $LINENO" >&2; echo "Error at line $LINENO" >> "$DEBUG_LOG"' ERR

HOOK_INPUT=$(cat)

echo "=== $(date -Iseconds) ===" >> "$DEBUG_LOG"

# Extract environment context
SESSION_ID=$(echo "$HOOK_INPUT" | jq -r '.session_id // empty' 2>/dev/null)
CWD=$(echo "$HOOK_INPUT" | jq -r '.cwd // empty' 2>/dev/null)
PROJECT_NAME=$(basename "${CWD:-unknown}")
DATE=$(date +%Y-%m-%d)
SESSION_TAG="session:${SESSION_ID:-$(date +%s)}"

echo "Environment: project=$PROJECT_NAME cwd=$CWD session=$SESSION_TAG" >> "$DEBUG_LOG"

# Extract transcript path
TRANSCRIPT_PATH=$(echo "$HOOK_INPUT" | jq -r '.transcript_path // empty' 2>/dev/null)

if [ -z "$TRANSCRIPT_PATH" ]; then
  if [ -n "$SESSION_ID" ]; then
    TRANSCRIPT_PATH=$(find ~/.claude/projects -name "${SESSION_ID}*.jsonl" 2>/dev/null | head -1)
  fi
fi

if [ -z "$TRANSCRIPT_PATH" ] || [ ! -f "$TRANSCRIPT_PATH" ]; then
  echo "No readable transcript found, skipping" >> "$DEBUG_LOG"
  exit 0
fi

# Skip trivial sessions (< 50 lines)
LINE_COUNT=$(wc -l < "$TRANSCRIPT_PATH" 2>/dev/null || echo "0")
if [ "$LINE_COUNT" -lt 50 ]; then
  echo "Transcript too short ($LINE_COUNT lines), skipping" >> "$DEBUG_LOG"
  exit 0
fi

# Sample head + tail, truncated to ~60k chars to avoid "prompt too long" with claude -p
# JSONL lines can be huge, so line count alone isn't safe
TRANSCRIPT_SAMPLE=$(head -30 "$TRANSCRIPT_PATH" 2>/dev/null; echo "---"; tail -70 "$TRANSCRIPT_PATH" 2>/dev/null)
TRANSCRIPT_SAMPLE=$(echo "$TRANSCRIPT_SAMPLE" | head -c 60000)

EXTRACT_PROMPT='You are a memory curator for an AI coding agent. Analyze the following Claude Code session transcript (JSONL format) and extract ONLY confirmed, valuable learnings.

## What to capture (tier: stm)
ONLY store memories that pass this bar — would a future session benefit from knowing this?
- Debugging insights: error -> root cause -> confirmed fix
- Architecture or design decisions with clear rationale
- User corrections or stated preferences
- Non-obvious gotchas that were confirmed through testing
- Solutions that required multiple attempts to find

## What NOT to capture
- Raw observations, file paths, or service names (these are noise, not learnings)
- Error messages without a resolution
- Patterns noticed but not confirmed
- What was being worked on (session summaries cover this)
- Anything that could be re-derived by reading the code or git log
- Git status, branch names, PR numbers without technical insight

## Rules
- Use tier "stm" for all learnings. Do NOT use "sensory" or "ltm".
- If the session was trivial or produced no genuine insights, output an empty array: []
- Be selective: 2-4 high-quality memories are better than 10 low-quality ones.
- Include the project name in tags when inferrable from file paths or repo names.

## Output format
Output a JSON array of objects. Each object has:
  - "key": descriptive kebab-case key
  - "kind": one of "semantic", "episodic", "procedural"
  - "priority": one of "low", "normal", "high", "critical"
  - "tier": "stm"
  - "tags": comma-separated string like "learning,project:foo"
  - "content": the insight in one or two sentences (plain text)

Also include one final object with key "session-summary" that summarizes what was accomplished
in this session in 1-2 sentences. This will become the consolidation summary node.

Output ONLY valid JSON. No markdown fences, no explanation.'

# Unset CLAUDECODE to prevent nested detection
unset CLAUDECODE

RESULT=$(echo "$TRANSCRIPT_SAMPLE" | claude -p --no-session-persistence --model "$LLM_MODEL" "$EXTRACT_PROMPT" 2>> "$DEBUG_LOG")

echo "Claude output:" >> "$DEBUG_LOG"
echo "$RESULT" >> "$DEBUG_LOG"

if [ -z "$RESULT" ]; then
  echo "No output from claude" >> "$DEBUG_LOG"
  exit 0
fi

# Extract JSON array: strip fences, then grab from first [ to last ]
RESULT=$(echo "$RESULT" | sed '/^```/d')
RESULT=$(echo "$RESULT" | sed -n '/^\[/,/^\]/p')

if ! echo "$RESULT" | jq 'type' > /dev/null 2>&1; then
  echo "Invalid JSON output, skipping" >> "$DEBUG_LOG"
  exit 0
fi

COUNT=$(echo "$RESULT" | jq 'length')
if [ "$COUNT" -eq 0 ]; then
  echo "No learnings found" >> "$DEBUG_LOG"
  exit 0
fi

# Separate session summary from individual learnings
SUMMARY_CONTENT=$(echo "$RESULT" | jq -r '.[] | select(.key == "session-summary") | .content' 2>/dev/null)
LEARNINGS=$(echo "$RESULT" | jq -c '[.[] | select(.key != "session-summary")]')
LEARNING_COUNT=$(echo "$LEARNINGS" | jq 'length')

echo "Processing $LEARNING_COUNT learnings + summary..." >> "$DEBUG_LOG"

# Store individual learnings, collect keys for consolidation
KEYS_FILE=$(mktemp)
trap 'rm -f "$KEYS_FILE"' EXIT

echo "$LEARNINGS" | jq -c '.[]' | while IFS= read -r item; do
  KEY=$(echo "$item" | jq -r '.key')
  KIND=$(echo "$item" | jq -r '.kind')
  PRIORITY=$(echo "$item" | jq -r '.priority')
  TIER=$(echo "$item" | jq -r '.tier // "stm"')
  TAGS=$(echo "$item" | jq -r '.tags')
  CONTENT=$(echo "$item" | jq -r '.content')

  if [ "$TIER" = "ltm" ]; then
    TIER="stm"
  fi

  # Append session tag to existing tags
  TAGS="${TAGS},${SESSION_TAG}"

  echo "Storing: $KEY (tier=$TIER)" >> "$DEBUG_LOG"
  PUT_OUTPUT=$($GHOST put -n "$AGENT_NS" -k "$KEY" --kind "$KIND" -p "$PRIORITY" --tier "$TIER" -t "$TAGS" --dedup "$CONTENT" 2>> "$DEBUG_LOG")
  if [ $? -eq 0 ] && [ -n "$PUT_OUTPUT" ]; then
    # Dedup may return a different key — use the actual stored key for consolidation
    ACTUAL_KEY=$(echo "$PUT_OUTPUT" | jq -r '.key // empty' 2>/dev/null)
    if [ -n "$ACTUAL_KEY" ]; then
      echo "$ACTUAL_KEY" >> "$KEYS_FILE"
    else
      echo "$KEY" >> "$KEYS_FILE"
    fi
    echo "$PUT_OUTPUT" >> "$DEBUG_LOG"
  else
    echo "Failed to store: $KEY" >> "$DEBUG_LOG"
  fi
done

# Read collected keys (while loop runs in subshell due to pipe, so use file)
STORED_KEYS=$(cat "$KEYS_FILE" 2>/dev/null | tr '\n' ',' | sed 's/,$//')

# Consolidate under a session summary node if we have >= 2 learnings and a summary
KEY_COUNT=$(cat "$KEYS_FILE" 2>/dev/null | wc -l | tr -d ' ')
if [ "$KEY_COUNT" -ge 2 ] && [ -n "$SUMMARY_CONTENT" ]; then
  SUMMARY_KEY="session-${PROJECT_NAME}-${DATE}-${SESSION_ID:0:8}"
  SUMMARY_TAGS="project:${PROJECT_NAME},${SESSION_TAG}"

  echo "Consolidating $KEY_COUNT learnings under $SUMMARY_KEY" >> "$DEBUG_LOG"
  $GHOST consolidate -n "$AGENT_NS" \
    --summary-key "$SUMMARY_KEY" \
    --keys "$STORED_KEYS" \
    --content "$SUMMARY_CONTENT" \
    --tags "$SUMMARY_TAGS" \
    >> "$DEBUG_LOG" 2>&1 || echo "Failed to consolidate" >> "$DEBUG_LOG"
else
  echo "Skipping consolidation: $KEY_COUNT keys, summary='${SUMMARY_CONTENT:0:50}'" >> "$DEBUG_LOG"
fi

# Lightweight reflect: prune expired sensory, decay stale edges.
# Runs after capture so new memories are included. Silent on error.
echo "Running lightweight reflect..." >> "$DEBUG_LOG"
$GHOST reflect --ns "$AGENT_NS" >> "$DEBUG_LOG" 2>&1 || echo "Reflect failed (non-fatal)" >> "$DEBUG_LOG"

echo "Done" >> "$DEBUG_LOG"
