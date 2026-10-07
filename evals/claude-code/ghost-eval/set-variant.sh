#!/usr/bin/env bash
# Picks the A/B variant every ghost call in the eval uses, by rewriting
# fixture/config.json (run build-fixture.sh first).
#   ./set-variant.sh baseline
#   ./set-variant.sh edges-off            GHOST_EDGE_EXPANSION=off
#   ./set-variant.sh edge-min 0.1         GHOST_EDGE_MIN_SCORE=0.1
#   GHOST_BIN=/path/to/ghost ./set-variant.sh ...   (test an unreleased build)
set -euo pipefail
cd "$(dirname "$0")"
GHOST="${GHOST_BIN:-$(jq -r '.ghost' fixture/config.json)}"
MODELS="$(jq -r '.env.GHOST_MODELS_DIR // empty' fixture/config.json)"
MODELS="${MODELS:-${GHOST_MODELS_DIR:-$HOME/.ghost/models}}"
case "${1:-baseline}" in
baseline) ENV='{}' ;;
edges-off) ENV='{"GHOST_EDGE_EXPANSION":"off"}' ;;
edge-min) ENV="{\"GHOST_EDGE_MIN_SCORE\":\"${2:?score}\"}" ;;
*) echo "unknown variant: $1" >&2; exit 2 ;;
esac
NAME="${1:-baseline}${2:+-$2}"
jq -n --arg g "$GHOST" --arg v "$NAME" --arg m "$MODELS" --argjson e "$ENV" '{ghost: $g, variant: $v, env: ($e + {GHOST_MODELS_DIR: $m})}' >fixture/config.json
cat fixture/config.json
