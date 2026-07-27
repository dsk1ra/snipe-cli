#!/usr/bin/env bash
# usage: bench.sh <bench-name> [extra staged-evaluator args...]
# Runs the 18 labelled offers into batch/bench/<bench-name> at temp 0.
# Determinism was verified, so one run per variant is a valid A/B.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"
NAME="${1:?usage: bench.sh <bench-name> [args...]}"; shift
BENCH="batch/bench/$NAME"
rm -rf "$BENCH"
# The log redirect resolves through $BENCH, so it must exist before the loop —
# without this every offer failed instantly on "No such file or directory".
mkdir -p "$BENCH/logs"
IDS="34 38 39 51 45 37 42 43 86 75 56 35 40 108 32 36 52 53"
start=$(date +%s)
for id in $IDS; do
  url=$(node -e "try{console.log(require('./batch/scores/$id.json').url||'n/a')}catch(e){console.log('n/a')}" 2>/dev/null | tail -1)
  node batch/staged-evaluator.mjs --id "$id" --url "$url" --report-num "$id" \
    --model snipe-eval --bench-dir "$BENCH" "$@" >"$BENCH/logs/$id.log" 2>&1 \
    && echo "  #$id ok" || echo "  #$id FAILED (see $BENCH/logs/$id.log)"
done
echo "DONE $NAME in $(( ($(date +%s)-start)/60 ))m"
