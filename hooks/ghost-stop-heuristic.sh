#!/usr/bin/env bash
# Ghost memory capture — Stop hook, LLM-FREE tier.
#
# Captures memories from the USER'S OWN PROMPTS using `ghost capture` —
# deterministic heuristics (entity salience + intent classifiers), NO
# `claude -p`, no API key required. Runs offline and instantly.
#
# Input: the prompt buffer written by ghost-user-prompt.sh, NOT the session
# transcript. This is the whole point. The UserPromptSubmit hook receives
# `.prompt` — exactly what the user typed — while the transcript multiplexes
# user speech with harness control messages (<task-notification>, slash-command
# caveats), hook-injected context, skill bodies and tool results, all labelled
# type=user. Four rounds of pattern-stripping failed to separate them: junk kept
# regenerating under new keys (settings-schema text stored as `show-preferred-os`,
# a skill body as `whether-specific-decision`). Reading the prompt buffer makes
# that class of leakage structurally impossible instead of pattern-suppressed.
#
# Two-tier note: this is the mechanical tier — it learns what the USER said
# (conventions, corrections, preferences). ghost-stop.sh is the LLM tier and
# reads the full transcript, where it has the judgment to distinguish synthesis
# from noise. Both converge on the same `ghost put`/dedup path.
#
# Configure via environment variables:
#   GHOST_BIN           — path to ghost binary (default: ghost on PATH)
#   GHOST_AGENT_NS      — agent namespace (falls back to GHOST_NS, then agent:claude-code)
#   GHOST_CAPTURE_MIN   — min salience 0..1 (default: 0.55)
#   GHOST_CAPTURE_MAX   — max memories per capture (default: 8)
#   GHOST_CAPTURE_GROWTH — min new buffered prompts before capturing (default: 3)
set -uo pipefail

GHOST="${GHOST_BIN:-ghost}"
AGENT_NS="${GHOST_AGENT_NS:-${GHOST_NS:-agent:claude-code}}"
MIN_SALIENCE="${GHOST_CAPTURE_MIN:-0.55}"
MAX_CAP="${GHOST_CAPTURE_MAX:-8}"
DEBUG_LOG="/tmp/ghost-stop-heuristic-debug.log"

trap 'echo "ghost-stop-heuristic.sh failed at line $LINENO" >&2; echo "Error at line $LINENO" >> "$DEBUG_LOG"' ERR

HOOK_INPUT=$(cat)
echo "=== $(date -Iseconds) ===" >> "$DEBUG_LOG"

SESSION_ID=$(echo "$HOOK_INPUT" | jq -r '.session_id // empty' 2>/dev/null)
CWD=$(echo "$HOOK_INPUT" | jq -r '.cwd // empty' 2>/dev/null)
PROJECT_NAME=$(basename "${CWD:-unknown}")
DATE=$(date +%Y-%m-%d)
SESSION_TAG="session:${SESSION_ID:-$(date +%s)}"

PROMPT_BUF="/tmp/ghost-prompt-buffer/${SESSION_ID:-default}.txt"
if [ ! -f "$PROMPT_BUF" ]; then
  echo "No prompt buffer for session ${SESSION_ID:-?}, skipping" >> "$DEBUG_LOG"
  exit 0
fi

# Debounce + delta-only: the Stop hook fires after EVERY assistant turn. Without
# a watermark it re-reads the same prompts and re-captures them under suffix-
# variant keys (css/css-a). Capture only prompts added since the last run, and
# only once a few have accumulated.
GROWTH_THRESHOLD="${GHOST_CAPTURE_GROWTH:-3}"
STATE_DIR="/tmp/ghost-stop-heuristic-state"
mkdir -p "$STATE_DIR" 2>/dev/null || true
STATE_FILE="$STATE_DIR/${SESSION_ID:-nosession}.lines"
CUR_LINES=$(wc -l < "$PROMPT_BUF" 2>/dev/null | tr -d ' '); CUR_LINES=${CUR_LINES:-0}
LAST_LINES=$(cat "$STATE_FILE" 2>/dev/null || echo 0); LAST_LINES=${LAST_LINES:-0}
if [ "$((CUR_LINES - LAST_LINES))" -lt "$GROWTH_THRESHOLD" ]; then
  echo "Debounce: $((CUR_LINES - LAST_LINES)) new prompts (<$GROWTH_THRESHOLD), skipping" >> "$DEBUG_LOG"
  exit 0
fi
# Advance the watermark immediately so overlapping Stop fires don't all proceed.
echo "$CUR_LINES" > "$STATE_FILE" 2>/dev/null || true

PROMPT_TEXT=$(tail -n "+$((LAST_LINES + 1))" "$PROMPT_BUF" | tail -200)

if [ -z "$(echo "$PROMPT_TEXT" | tr -d '[:space:]')" ]; then
  echo "Buffer delta empty, skipping" >> "$DEBUG_LOG"
  exit 0
fi

echo "Capturing from $(echo "$PROMPT_TEXT" | wc -l | tr -d ' ') user prompts (ns=$AGENT_NS)" >> "$DEBUG_LOG"

echo "$PROMPT_TEXT" | "$GHOST" capture \
  -n "$AGENT_NS" \
  --source "$SESSION_TAG" \
  -t "project:${PROJECT_NAME},date:${DATE}" \
  --min-salience "$MIN_SALIENCE" \
  --max "$MAX_CAP" \
  --speaker user \
  >> "$DEBUG_LOG" 2>&1 || echo "capture failed (non-fatal)" >> "$DEBUG_LOG"

# Lightweight reflect so new memories flow through the lifecycle. Silent on error.
"$GHOST" reflect --ns "$AGENT_NS" >> "$DEBUG_LOG" 2>&1 || echo "reflect failed (non-fatal)" >> "$DEBUG_LOG"

echo "Done" >> "$DEBUG_LOG"
