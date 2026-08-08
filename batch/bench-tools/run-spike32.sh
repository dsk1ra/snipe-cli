#!/usr/bin/env bash
# End-to-end confirmation: all three shipped changes together, judge ON.
# Deliberately a FRESH select cache -- the old one is keyed on the CV and the
# requirements, not on the ranker, so reusing it would serve pre-spike
# selections and the run would measure nothing.
set -eu
cd "$(dirname "$0")/../.."
export SNIPE_SELECT_CACHE=batch/bench/opus/select-cache-spike.json
export SNIPE_PROJECT_BULLETS=2
node batch/tailor-harness.mjs run spike32 --temperature 0 --sample sample32.tsv --writer verbatim
echo "=== run done, comparing ==="
node batch/tailor-harness.mjs paired vbp2 spike32 --no-embed
echo "=== vs the pipeline as it was at session start ==="
node batch/tailor-harness.mjs paired ctl32 spike32 --no-embed
