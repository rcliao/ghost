#!/usr/bin/env bash
# Ghost memory retrieval — SessionStart hook
# Injects environment-aware context at session start.
# Output goes to stdout → Claude Code injects as additionalContext.
#
# Configure via environment variables:
#   GHOST_BIN       — path to ghost binary (default: ghost on PATH)
#   GHOST_AGENT_NS  — agent namespace (default: agent:claude-code)
set -euo pipefail

GHOST="${GHOST_BIN:-ghost}"
AGENT_NS="${GHOST_AGENT_NS:-agent:claude-code}"

# Guard against recursive invocation
if [ "${GHOST_SESSION_HOOK:-}" = "1" ]; then
  exit 0
fi
export GHOST_SESSION_HOOK=1

# Read hook input for environment context
HOOK_INPUT=$(cat)
PROJECT_DIR=$(echo "$HOOK_INPUT" | jq -r '.cwd // empty' 2>/dev/null)
SESSION_ID=$(echo "$HOOK_INPUT" | jq -r '.session_id // empty' 2>/dev/null)
SOURCE=$(echo "$HOOK_INPUT" | jq -r '.source // "startup"' 2>/dev/null)
PROJECT_NAME=$(basename "${PROJECT_DIR:-unknown}")
TIMESTAMP=$(date -Iseconds)

# Build environment-aware query so ghost scores relevance properly
QUERY="working in ${PROJECT_NAME} at ${PROJECT_DIR:-unknown}, session ${SOURCE} at ${TIMESTAMP}"

# Project-scoped tag filter: when working in a known project, prefer project-tagged memories
TAG_ARGS=""
if [ -n "$PROJECT_NAME" ] && [ "$PROJECT_NAME" != "unknown" ] && [ "$PROJECT_NAME" != "/" ]; then
  TAG_ARGS="-t project:${PROJECT_NAME}"
fi

MEMORIES=""

# Agent-level memories — 2000 tokens total, with project tag filtering when available
if [ -n "$TAG_ARGS" ]; then
  # First: project-scoped context (1500 tokens)
  PROJECT_RAW=$($GHOST context "$QUERY" -n "$AGENT_NS" $TAG_ARGS --budget 1500 2>/dev/null || echo "{}")
  PROJECT_MEM=$(echo "$PROJECT_RAW" | jq -r '.memories[]? | "[\(.key)] \(.content)"' 2>/dev/null || echo "")
  # Extract project keys for dedup
  PROJECT_KEYS=$(echo "$PROJECT_RAW" | jq -r '.memories[]?.key // empty' 2>/dev/null)

  # Then: pinned/general context (500 tokens) without project filter, dedup against project keys
  GENERAL_RAW=$($GHOST context "$QUERY" -n "$AGENT_NS" --budget 500 2>/dev/null || echo "{}")
  if [ -n "$PROJECT_KEYS" ]; then
    GENERAL_MEM=$(echo "$GENERAL_RAW" | jq -r --argjson pkeys "$(echo "$PROJECT_KEYS" | jq -R . | jq -s .)" \
      '.memories[]? | select(.key as $k | ($pkeys | index($k)) | not) | "[\(.key)] \(.content)"' 2>/dev/null || echo "")
  else
    GENERAL_MEM=$(echo "$GENERAL_RAW" | jq -r '.memories[]? | "[\(.key)] \(.content)"' 2>/dev/null || echo "")
  fi

  if [ -n "$PROJECT_MEM" ]; then
    MEMORIES="## Project: ${PROJECT_NAME}\n${PROJECT_MEM}"
  fi
  if [ -n "$GENERAL_MEM" ]; then
    MEMORIES="${MEMORIES}\n## General\n${GENERAL_MEM}"
  fi
else
  # No project context — general query only
  AGENT_RAW=$($GHOST context "$QUERY" -n "$AGENT_NS" --budget 2000 2>/dev/null || echo "{}")
  AGENT_MEM=$(echo "$AGENT_RAW" | jq -r '.memories[]? | "[\(.key)] \(.content)"' 2>/dev/null || echo "")
  if [ -n "$AGENT_MEM" ]; then
    MEMORIES="## Agent Knowledge\n$AGENT_MEM"
  fi
fi

if [ -z "$MEMORIES" ]; then
  exit 0
fi

# Write loaded keys to temp file for UserPromptSubmit dedup
KEYS_FILE="/tmp/ghost-session-keys-${SESSION_ID:-default}"
if [ -n "$TAG_ARGS" ]; then
  { echo "$PROJECT_RAW"; echo "$GENERAL_RAW"; } 2>/dev/null | jq -r '.memories[]?.key // empty' 2>/dev/null | sort -u > "$KEYS_FILE" 2>/dev/null || true
else
  echo "$AGENT_RAW" 2>/dev/null | jq -r '.memories[]?.key // empty' 2>/dev/null | sort -u > "$KEYS_FILE" 2>/dev/null || true
fi

echo -e "[Ghost Memory — Session Start]\n$MEMORIES\n[End Ghost Memory]"
