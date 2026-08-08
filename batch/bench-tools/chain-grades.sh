#!/usr/bin/env bash
# Runs the distinctness grading pass strictly AFTER the relevance pass.
# Not concurrently: the passes write the same cache file, and the running one
# holds an in-memory copy it re-saves whole, so an overlap loses one of them.
set -u
cd "$(dirname "$0")/../.."
while pgrep -f "select-sweep.mjs grades$" >/dev/null 2>&1; do sleep 30; done
echo "=== relevance pass done at $(date -Is) ==="
node batch/bench-tools/select-sweep.mjs grades --distinct
echo "=== distinct pass done at $(date -Is) ==="
node batch/bench-tools/select-sweep.mjs ablate --split train
echo "=== held-out check ==="
node batch/bench-tools/select-sweep.mjs check --split test --spike 6
