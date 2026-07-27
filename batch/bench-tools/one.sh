#!/usr/bin/env bash
# usage: one.sh <bench-name> <offer-id> [extra args...]
# Single-offer spot check — falsify a prompt change in ~3 min before committing
# an hour of GPU to a full 18-offer run.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"
NAME="${1:?usage: one.sh <bench-name> <offer-id>}"; ID="${2:?}"; shift 2
BENCH="batch/bench/$NAME"
mkdir -p "$BENCH"
url=$(node -e "try{console.log(require('./batch/scores/$ID.json').url||'n/a')}catch(e){console.log('n/a')}" 2>/dev/null | tail -1)
node batch/staged-evaluator.mjs --id "$ID" --url "$url" --report-num "$ID" \
  --model snipe-eval --bench-dir "$BENCH" "$@" 2>&1 | tail -5
echo "--- evidence table ---"
awk '/JD Requirement/,/^$/' "$BENCH"/reports/${ID}-*.md | cut -c1-200
node -e "
const e=require('./$BENCH/evals/$ID.json');
console.log('score',e.score,'cv_match',e.cv_match,'coverage',e.cv_coverage,'ns',e.north_star);
"
