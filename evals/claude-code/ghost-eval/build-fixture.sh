#!/usr/bin/env bash
# Builds fixture/memory.db for the ghost-eval cases: ghost's own synthetic eval
# seed (58 memories) as realistic distractors, plus one memory per case.
# Rebuild before every eval run so runs start from the same store.
set -euo pipefail
cd "$(dirname "$0")"
GHOST="${GHOST_BIN:-$(command -v ghost)}"
DB=fixture/memory.db
mkdir -p fixture
rm -f "$DB" "$DB-wal" "$DB-shm"

# Distractors: the eval:ghost seed (auth, db, deploy, frontend, perf ... topics).
"$GHOST" --db "$DB" eval --budget 2000 >/dev/null

put() { "$GHOST" --db "$DB" put -n eval:ghost -k "$1" -t "project:acme-api" --kind semantic "$2" >/dev/null; }

# must-have: a repo convention the code does not reveal
put acme-api-changes-log "acme-api convention: every change to an HTTP endpoint (new, changed or removed route) must add a dated entry to docs/api-changes.md at the repo root, creating the file if it does not exist. Reviewers block PRs that change routes without it."
# must-have: an operational fact found in an earlier session
put acme-staging-deploy "acme-api staging deploy command: make deploy ENV=stg REGION=usw2. The Makefile rejects ENV=staging; stg and usw2 are the only valid staging values."
# must-have: a stated user preference
put user-script-language-pref "The user prefers JavaScript (Node .mjs files) over Python for scripts and small tools in the acme repos, because the agent tooling is JS. Do not write new Python scripts."
# stale on purpose: the repo moved to config/settings.toml
put acme-config-location "acme-api configuration lives in config/settings.yaml; the server port is under server.port."
# explicit recall: a past decision the prompt only alludes to
put acme-retry-policy "Retry policy decided 2026-09 for acme-api's outbound HTTP client: at most 3 attempts in total, exponential backoff starting at 200ms and doubling (200ms, then 400ms), retry only on 5xx responses and network errors, never on 4xx. Use constants MaxAttempts = 3 and BaseDelay = 200 * time.Millisecond."

# stale on purpose: an extra step on a file the repo no longer has
put acme-api-port-k8s "When acme-api's port changes, also update targetPort in deploy/k8s/service.yaml or the health checks fail after deploy."

# Round 2: competing memories on the same topics. Each is true for a sibling
# repo and wrong for acme-api, so retrieval has to rank and the agent has to tell them apart.
putx() { "$GHOST" --db "$DB" put -n eval:ghost -k "$1" -t "$2" --kind semantic "$3" >/dev/null; }
putx acme-billing-staging-deploy project:acme-billing "acme-billing staging deploy: make deploy ENV=staging REGION=use1 (billing still uses the old environment names)."
putx acme-web-changelog project:acme-web "acme-web convention: every user-facing change gets a line in CHANGELOG.md under the Unreleased heading."
putx acme-web-retry project:acme-web "acme-web's frontend fetch wrapper retries 5 times with a fixed 1s delay on any error."
putx acme-tools-python project:acme-tools "acme-tools scripts are written in Python 3.12 and run with uv; keep new tools in that repo consistent."

printf '{"ghost":"%s"}\n' "$GHOST" >fixture/config.json
echo "fixture: $("$GHOST" --db "$DB" list -n eval:ghost --limit 1000 2>/dev/null | jq 'length' 2>/dev/null || echo '?') memories in eval:ghost"
