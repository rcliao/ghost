#!/usr/bin/env bash
# Ghost memory retrieval — UserPromptSubmit hook
# Injects per-prompt relevant memories with small budget.
# Deduplicates against keys already loaded by SessionStart hook.
#
# Configure via environment variables:
#   GHOST_BIN       — path to ghost binary (default: ghost on PATH)
#   GHOST_AGENT_NS  — agent namespace (default: agent:claude-code)
set -euo pipefail

GHOST="${GHOST_BIN:-ghost}"
AGENT_NS="${GHOST_AGENT_NS:-agent:claude-code}"

HOOK_INPUT=$(cat)
QUERY=$(echo "$HOOK_INPUT" | jq -r '.prompt // empty' 2>/dev/null)
SESSION_ID=$(echo "$HOOK_INPUT" | jq -r '.session_id // empty' 2>/dev/null)
CWD=$(echo "$HOOK_INPUT" | jq -r '.cwd // empty' 2>/dev/null)
PROJECT_NAME=$(basename "${CWD:-unknown}")

if [ -z "$QUERY" ] || [ ${#QUERY} -lt 10 ]; then
  exit 0  # Skip trivial prompts
fi

# No project tag filter — let relevance scoring do the work instead of
# hard-filtering. Cross-project knowledge is often valuable.

# Budget 1500 tokens — with the 400-token-per-memory cap, this fits ~4-6 memories
RAW=$($GHOST context "$QUERY" -n "$AGENT_NS" --budget 1500 2>/dev/null || echo "{}")

# Deduplicate against SessionStart keys
KEYS_FILE="/tmp/ghost-session-keys-${SESSION_ID:-default}"
if [ -f "$KEYS_FILE" ]; then
  # Filter out keys already loaded by SessionStart
  MEM=$(echo "$RAW" | jq -r --slurpfile loaded <(jq -R . "$KEYS_FILE" | jq -s .) \
    '.memories[]? | select(.key as $k | ($loaded[0] // []) | index($k) | not) | "[\(.key)] \(.content)"' 2>/dev/null || echo "")
else
  MEM=$(echo "$RAW" | jq -r '.memories[]? | "[\(.key)] \(.content)"' 2>/dev/null || echo "")
fi

if [ -n "$MEM" ]; then
  echo -e "[Ghost Memory — Relevant]\n$MEM\n[End Ghost Memory]"
fi
