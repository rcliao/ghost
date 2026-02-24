#!/bin/bash
PROJECT="agent-memory"
AM="$HOME/go/bin/agent-memory"
WORKDIR="$HOME/src/pikamini/projects/$PROJECT"

# Allow passing a new task as an argument: ./test-claude-memory.sh "my new task"
if [ $# -ge 1 ]; then
  $AM put -n "task:$PROJECT" -k "objective" "$*" >/dev/null
  $AM rm -n "plan:$PROJECT" -k "current" 2>/dev/null || true
  $AM rm -n "reflect:$PROJECT" -k "latest" 2>/dev/null || true
  $AM rm -n "task:$PROJECT" -k "status" 2>/dev/null || true
  echo "Task set: $*"
fi

# Lock guard with TTL
LOCK=$($AM get -n "session:$PROJECT" -k "lock" 2>/dev/null | jq -r '.content // empty' 2>/dev/null)
if [ -n "$LOCK" ]; then echo "Another session is running, exiting."; exit 0; fi
$AM put -n "session:$PROJECT" -k "lock" --ttl 15m "running" >/dev/null
trap '$AM rm -n "session:$PROJECT" -k "lock" 2>/dev/null || true' EXIT

# Recall from memory
TASK=$($AM get -n "task:$PROJECT" -k "objective" 2>/dev/null | jq -r '.content // empty' 2>/dev/null)
if [ -z "$TASK" ]; then echo "No task set. Run: $0 \"your task here\""; exit 1; fi

PLAN=$($AM get -n "plan:$PROJECT" -k "current" 2>/dev/null | jq -r '.content // empty' 2>/dev/null)
PLAN="${PLAN:-No plan yet — create one from the task}"
REFLECTION=$($AM get -n "reflect:$PROJECT" -k "latest" 2>/dev/null | jq -r '.content // empty' 2>/dev/null)
REFLECTION="${REFLECTION:-First run}"

echo "--- Starting agent run ---"
echo "Task: $TASK"
echo "Plan: $(echo "$PLAN" | head -1)"
echo "Reflection: $(echo "$REFLECTION" | head -1)"
echo ""

# Build prompt (direct variable expansion, no sed)
PROMPT="You are an autonomous coding agent working on the '$PROJECT' project.
Each run you do ONE focused step, then save your state.

## Task
$TASK

## Current Plan
$PLAN

## Last Reflection
$REFLECTION

## Memory CLI
You have access to a persistent memory store via '$AM'. Use it to read/write state:
  $AM put -n \"NAMESPACE\" -k \"KEY\" \"CONTENT\"   # store a memory
  $AM get -n \"NAMESPACE\" -k \"KEY\"               # retrieve a memory
  $AM list -n \"NAMESPACE\"                        # list memories in a namespace
  $AM search \"query\"                             # search across all memories

## After completing your step, you MUST:
1. Save your updated plan:
   $AM put -n \"plan:$PROJECT\" -k \"current\" \"YOUR UPDATED PLAN\"
2. Save your reflection:
   $AM put -n \"reflect:$PROJECT\" -k \"latest\" \"WHAT YOU DID AND WHAT'S NEXT\"
3. If the task is fully complete, mark it done:
   $AM put -n \"task:$PROJECT\" -k \"status\" \"done\""

cd "$WORKDIR"
unset CLAUDECODE

LOGFILE="/tmp/agent-$PROJECT-$(date +%Y%m%d).log"
echo "=== Run $(date) ===" >> "$LOGFILE"

claude -p "$PROMPT" --allowedTools "Bash,Write,Edit,Read" \
  2>&1 | tee -a "$LOGFILE"

# Show post-run state
echo ""
echo "--- Post-run state ---"
echo "Plan:"
$AM get -n "plan:$PROJECT" -k "current" 2>/dev/null | jq -r '.content // empty' 2>/dev/null || echo "  (none)"
echo ""
echo "Reflection:"
$AM get -n "reflect:$PROJECT" -k "latest" 2>/dev/null | jq -r '.content // empty' 2>/dev/null || echo "  (none)"
echo ""
STATUS=$($AM get -n "task:$PROJECT" -k "status" 2>/dev/null | jq -r '.content // empty' 2>/dev/null)
if [ "$STATUS" = "done" ]; then
  echo "Task status: DONE"
else
  echo "Task status: in progress (run again to continue)"
fi
