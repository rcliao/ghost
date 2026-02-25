#!/bin/bash
# run-plan.sh — Autonomous plan runner with self-evaluation.
# Usage: ./scripts/run-plan.sh [plan.md]
#
# Flow per task:
#   1. Execute: Claude implements the task
#   2. Test:    go test ./... (mechanical gate)
#   3. Review:  Claude reviews the diff against the goal (judgment gate)
#   4. Decide:  done → check off, needs_revision → retry, needs_human → stop

PROJECT="agent-memory"
AM="$HOME/go/bin/agent-memory"
WORKDIR="$HOME/src/pikamini/projects/agent-memory"
PLAN_FILE="${1:-$WORKDIR/plan.md}"
CONVENTIONS_FILE="$WORKDIR/CONVENTIONS.md"
LOGFILE="/tmp/agent-$PROJECT-$(date +%Y%m%d).log"
MAX_RETRIES=1

# Load conventions if present
CONVENTIONS=""
if [ -f "$CONVENTIONS_FILE" ]; then
  CONVENTIONS=$(cat "$CONVENTIONS_FILE")
fi

if [ ! -f "$PLAN_FILE" ]; then
  echo "Plan file not found: $PLAN_FILE"
  exit 1
fi

log() { echo "[$(date +%H:%M:%S)] $*" | tee -a "$LOGFILE"; }

next_task() {
  grep -n '^\- \[ \]' "$PLAN_FILE" | head -1
}

check_off() {
  sed -i '' "${1}s/- \[ \]/- [x]/" "$PLAN_FILE"
}

mark_failed() {
  sed -i '' "${1}s/- \[ \]/- [!]/" "$PLAN_FILE"
}

mark_needs_human() {
  sed -i '' "${1}s/- \[ \]/- [?]/" "$PLAN_FILE"
}

# --- Mechanical gate: run tests ---
run_tests() {
  log "Quality gate: go test ./..."
  cd "$WORKDIR"
  TEST_OUTPUT=$(go test ./... 2>&1)
  TEST_EXIT=$?
  if [ $TEST_EXIT -eq 0 ]; then
    log "Tests passed."
    return 0
  else
    log "Tests FAILED."
    echo "$TEST_OUTPUT" | tail -20 | tee -a "$LOGFILE"
    return 1
  fi
}

# --- Execute: Claude implements the task ---
run_task() {
  local task_text="$1"
  local feedback="$2"  # empty on first attempt, review feedback on retry

  PREV_REFLECTIONS=$($AM list -n "reflect:$PROJECT" -l 5 2>/dev/null | jq -r '.[].content // empty' 2>/dev/null)
  PREV_REFLECTIONS="${PREV_REFLECTIONS:-No previous task history}"

  FEEDBACK_BLOCK=""
  if [ -n "$feedback" ]; then
    FEEDBACK_BLOCK="
## Feedback From Review
A reviewer found issues with your previous attempt. You MUST address this:
$feedback"
  fi

  PROMPT="You are an autonomous coding agent working on the '$PROJECT' project.
Complete this task fully in this session.

## Your Task
$task_text

## Context From Previous Tasks
$PREV_REFLECTIONS
$FEEDBACK_BLOCK

## Memory CLI
Persistent memory store available at '$AM':
  $AM put -n \"NS\" -k \"KEY\" \"CONTENT\"
  $AM get -n \"NS\" -k \"KEY\"
  $AM search \"query\"

## When done, save a reflection:
$AM put -n \"reflect:$PROJECT\" -k \"task-\$(date +%s)\" \"BRIEF SUMMARY OF WHAT YOU DID\"

## Project Conventions
$CONVENTIONS

## Rules
- Complete the ENTIRE task in this session
- Run 'go test ./...' before finishing to make sure nothing is broken
- Do NOT modify plan.md, run-plan.sh, test-claude-memory.sh, or CONVENTIONS.md
- Keep changes minimal and focused on the task
- If the task is ambiguous or would require violating a convention, STOP and explain what you need clarified in your reflection instead of guessing"

  cd "$WORKDIR"
  unset CLAUDECODE

  log "Executing task..."
  claude -p "$PROMPT" --allowedTools "Bash,Write,Edit,Read" \
    2>&1 | tee -a "$LOGFILE"
}

# --- Judgment gate: Claude reviews the diff ---
review_task() {
  local task_text="$1"
  local diff_output="$2"
  local test_output="$3"

  REVIEW_PROMPT="You are a strict code reviewer evaluating whether an automated agent completed a task correctly.
You are the last line of defense before code is accepted. Be critical.

## Task That Was Assigned
$task_text

## Project Conventions
$CONVENTIONS

## Git Diff (what the agent changed)
\`\`\`
$diff_output
\`\`\`

## Test Results
$test_output

## Your Job
Evaluate the changes against BOTH the task description AND the project conventions.

## Check for correctness:
- Does the diff actually implement what the task asked for?
- Is the implementation correct and idiomatic?
- Are there bugs, edge cases, or missing error handling?

## Check for convention violations (MUST flag as needs_human):
- Does it change the database schema without the task explicitly calling for it?
- Does it add new CLI subcommands, data models, or Store interface methods that aren't specified in the task?
- Does it break backwards compatibility with existing data or CLI output?
- Does it touch more than 8 non-test files?
- Does it add new dependencies?

## Check for scope creep (flag as needs_revision or needs_human):
- Did the agent make design decisions that the task didn't specify?
- Did the agent add features, flags, or behaviors beyond what was asked?
- Is the agent guessing at requirements instead of keeping to what's specified?

## You MUST respond with EXACTLY one of these three verdicts on the FIRST line:
VERDICT: done
VERDICT: needs_revision
VERDICT: needs_human

Rules for choosing:
- done: Implementation matches the task, follows conventions, no scope creep
- needs_revision: Has bugs or incomplete work that the agent can fix (give specific feedback)
- needs_human: Agent made design decisions not in the task spec, violated conventions, or the task is too ambiguous to implement without human guidance. PREFER this over done when the agent had to guess."

  cd "$WORKDIR"
  unset CLAUDECODE

  log "Reviewing changes..."
  REVIEW_OUTPUT=$(claude -p "$REVIEW_PROMPT" 2>&1)
  echo "$REVIEW_OUTPUT" | tee -a "$LOGFILE"

  # Extract verdict from first line
  VERDICT=$(echo "$REVIEW_OUTPUT" | grep -o 'VERDICT: [a-z_]*' | head -1 | cut -d' ' -f2)
  echo "$VERDICT"
}

# --- Main loop ---

log "=== Plan run started ==="
log "Plan: $PLAN_FILE"
log ""

TASK_NUM=0
while true; do
  NEXT=$(next_task)
  if [ -z "$NEXT" ]; then
    log "All tasks complete!"
    break
  fi

  LINE_NUM=$(echo "$NEXT" | cut -d: -f1)
  TASK_TEXT=$(echo "$NEXT" | cut -d: -f2- | sed 's/^- \[ \] //')
  TASK_NUM=$((TASK_NUM + 1))

  log "=========================================="
  log "Task $TASK_NUM: $TASK_TEXT"
  log "=========================================="

  # Snapshot file state before the task so we can diff just this task's changes
  cd "$WORKDIR"
  SNAP_DIR=$(mktemp -d)
  git diff > "$SNAP_DIR/before.diff"
  git ls-files --others --exclude-standard > "$SNAP_DIR/before-untracked.txt"

  FEEDBACK=""
  FINAL_VERDICT=""

  for attempt in $(seq 0 $MAX_RETRIES); do
    if [ "$attempt" -gt 0 ]; then
      log "--- Retry $attempt/$MAX_RETRIES (addressing review feedback) ---"
    fi

    # Step 1: Execute
    run_task "$TASK_TEXT" "$FEEDBACK"

    # Step 2: Mechanical gate
    cd "$WORKDIR"
    TEST_OUTPUT=$(go test ./... 2>&1)
    TEST_EXIT=$?
    if [ $TEST_EXIT -ne 0 ]; then
      log "Tests FAILED."
      echo "$TEST_OUTPUT" | tail -20 | tee -a "$LOGFILE"
      FEEDBACK="Tests failed with:\n$TEST_OUTPUT"
      continue
    fi
    log "Tests passed."

    # Step 3: Judgment gate — compute incremental diff (just this task's changes)
    cd "$WORKDIR"
    git diff > "$SNAP_DIR/after.diff"
    git ls-files --others --exclude-standard > "$SNAP_DIR/after-untracked.txt"

    # Diff of diffs: what changed between before and after this task
    DIFF_OUTPUT=$(diff "$SNAP_DIR/before.diff" "$SNAP_DIR/after.diff" 2>/dev/null | grep '^[><]' || true)

    # New files created during this task
    NEW_FILES=$(comm -13 <(sort "$SNAP_DIR/before-untracked.txt") <(sort "$SNAP_DIR/after-untracked.txt"))
    if [ -n "$NEW_FILES" ]; then
      for f in $NEW_FILES; do
        DIFF_OUTPUT="$DIFF_OUTPUT

--- new file: $f ---
$(head -100 "$WORKDIR/$f")"
      done
    fi

    if [ -z "$DIFF_OUTPUT" ]; then
      log "No changes detected. Skipping review."
      FINAL_VERDICT="done"
      break
    fi

    VERDICT=$(review_task "$TASK_TEXT" "$DIFF_OUTPUT" "$TEST_OUTPUT")

    # Parse the last line (the extracted verdict)
    VERDICT_LINE=$(echo "$VERDICT" | tail -1)

    if [ "$VERDICT_LINE" = "done" ]; then
      FINAL_VERDICT="done"
      break
    elif [ "$VERDICT_LINE" = "needs_human" ]; then
      FINAL_VERDICT="needs_human"
      # Store the review reasoning for the human
      REVIEW_REASON=$(echo "$VERDICT" | head -n -1)
      $AM put -n "review:$PROJECT" -k "task-$TASK_NUM" "$REVIEW_REASON" >/dev/null 2>&1
      break
    else
      # needs_revision — extract feedback for retry
      FEEDBACK=$(echo "$VERDICT" | head -n -1)
      log "Reviewer requested revision."
    fi
  done

  # Step 4: Decide
  case "$FINAL_VERDICT" in
    done)
      check_off "$LINE_NUM"
      log "Task $TASK_NUM: DONE"
      ;;
    needs_human)
      mark_needs_human "$LINE_NUM"
      log "Task $TASK_NUM: NEEDS HUMAN — reviewer flagged a decision for you."
      log "Review: $AM get -n review:$PROJECT -k task-$TASK_NUM"
      log "Stopping. Fix or approve, then re-run."
      exit 0
      ;;
    *)
      mark_failed "$LINE_NUM"
      log "Task $TASK_NUM: FAILED after $((MAX_RETRIES + 1)) attempts."
      log "Stopping. Fix manually, then re-run."
      exit 1
      ;;
  esac

  log "Pausing 5s before next task..."
  sleep 5
done

log ""
log "=== Plan complete ==="
cat "$PLAN_FILE" | tee -a "$LOGFILE"
