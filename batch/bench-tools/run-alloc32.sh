#!/usr/bin/env bash
# Experiment A: adaptive per-project bullet allocation, end to end against the
# pipeline as shipped (spike32). Same total project-bullet budget, distributed
# by score instead of flat.
#
# FRESH select cache, as always for a selection change -- the key is over the CV
# and the requirements, not the ranker, so the spike32 cache would serve
# pre-allocation selections and the run would measure nothing.
set -eu
cd "$(dirname "$0")/../.."
export SNIPE_SELECT_CACHE=batch/bench/opus/select-cache-alloc.json
# 4 is the per-project ceiling now, not the count; cv-select spends the budget.
export SNIPE_PROJECT_BULLETS=4
node batch/tailor-harness.mjs run alloc32 --temperature 0 --sample sample32.tsv --writer verbatim
echo "=== run done, comparing against the shipped pipeline ==="
node batch/tailor-harness.mjs paired spike32 alloc32 --no-embed
