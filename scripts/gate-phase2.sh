#!/usr/bin/env bash
# Phase 2 equivalence gate: evaluate the same offer through the Node
# implementation and this one, and diff the report and the artifact.
#
# The Node side runs from a COPY of its own code inside the fixture tree.
# staged-evaluator.mjs resolves PROJECT_DIR as `resolve(__dirname, '..')` and
# writes reports/, batch/evals/ and batch/jds/ under it — so running it in place
# overwrites real evaluation data. It did, once. The copy is not tidiness; it is
# the only thing that makes this script safe to run.
#
#   scripts/gate-phase2.sh <node-repo> <id> [id...]
set -euo pipefail

NODE_REPO="${1:?usage: gate-phase2.sh <node-repo> <id> [id...]}"; shift
IDS=("$@")
[ ${#IDS[@]} -gt 0 ] || { echo "give at least one offer id"; exit 2; }

GO_REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${SNIPE_GATE_DIR:-/tmp/snipe-gate2}"

rm -rf "$WORK"
mkdir -p "$WORK/js/batch/jds" "$WORK/js/batch/scores" "$WORK/js/batch/evals" \
         "$WORK/js/config" "$WORK/js/reports" "$WORK/js/data" \
         "$WORK/go/batch/jds" "$WORK/go/batch/scores" "$WORK/go/batch/state" \
         "$WORK/go/config" "$WORK/go/reports"

# The Node code, sandboxed: batch/ and the modules it imports, nothing else.
cp "$NODE_REPO"/batch/*.mjs "$WORK/js/batch/"
cp "$NODE_REPO"/batch/*-prompt*.md "$WORK/js/batch/"
mkdir -p "$WORK/js/tracker" && cp "$NODE_REPO"/tracker/*.mjs "$WORK/js/tracker/"
ln -s "$NODE_REPO/node_modules" "$WORK/js/node_modules"

# SNIPE_DUMP writes each /api/chat body to a file per stage, on both sides. When
# two implementations produce different prose from the same model at temperature
# 0, the difference is in the request — and on a 30B each guess costs minutes.
# The shim is generated here rather than kept as a file because this script
# deletes and rebuilds the fixture on every run.
cat > "$WORK/js/dump.mjs" <<'SHIM'
import { writeFileSync, mkdirSync } from 'fs';
const dir = process.env.SNIPE_DUMP;
if (dir) {
  mkdirSync(dir, { recursive: true });
  const real = globalThis.fetch;
  let n = 0;
  globalThis.fetch = (url, opts) => {
    if (String(url).endsWith('/api/chat') && opts?.body) {
      const label = ['p2.jdparse', 'p2.evidence', 'p2.judgment'][n++] || `call${n}`;
      writeFileSync(`${dir}/${label}.json`, opts.body);
    }
    return real(url, opts);
  };
}
await import('./batch/staged-evaluator.mjs');
SHIM

# The Go tree keeps its state under batch/state/; only the Node checkout is flat.
sdir() { [ "$1" = go ] && echo "$WORK/go/batch/state" || echo "$WORK/js/batch"; }

for side in js go; do
  cp "$NODE_REPO/cv.md" "$WORK/$side/cv.md"
  cp "$NODE_REPO/config/profile.md" "$NODE_REPO/config/profile.yml" "$WORK/$side/config/"
  [ -f "$NODE_REPO/article-digest.md" ] && cp "$NODE_REPO/article-digest.md" "$WORK/$side/"
  printf 'id\turl\tsource\tnotes\n' > "$(sdir "$side")/batch-input.tsv"
  printf 'id\turl\tp1_status\tp1_score\tp1_archetype\tp2_status\tp2_report_num\tp3_status\terror\tretries\n' \
    > "$(sdir "$side")/local-state.tsv"
done

for id in "${IDS[@]}"; do
  url=$(awk -F'\t' -v i="$id" '$1==i{print $2; exit}' "$NODE_REPO/batch/batch-input.tsv")
  url="${url:-manual:gate-$id}"
  score=$(awk -F'\t' -v i="$id" '$1==i{print $4; exit}' "$NODE_REPO/batch/local-state.tsv")
  for side in js go; do
    cp "$NODE_REPO/batch/jds/$id.txt" "$WORK/$side/batch/jds/"
    [ -f "$NODE_REPO/batch/scores/$id.json" ] && cp "$NODE_REPO/batch/scores/$id.json" "$WORK/$side/batch/scores/"
    printf '%s\t%s\tgate\t-\n' "$id" "$url" >> "$(sdir "$side")/batch-input.tsv"
    printf '%s\t%s\tscored\t%s\t-\t-\t-\t-\t-\t0\n' "$id" "$url" "${score:--}" \
      >> "$(sdir "$side")/local-state.tsv"
  done
done

# --no-rag on both sides: the calibration corpus is the evals directory, which is
# empty in a fixture and full in the real tree. Comparing across that compares
# two different prompts, not two implementations.
echo "── Node ──"
for id in "${IDS[@]}"; do
  url=$(awk -F'\t' -v i="$id" '$1==i{print $2; exit}' "$WORK/js/batch/batch-input.tsv")
  ( cd "$WORK/js" && SNIPE_TIMING= SNIPE_DUMP="$WORK/dump/js/$id" node dump.mjs \
      --id "$id" --url "$url" --report-num "$id" --model snipe-eval --no-rag ) >/dev/null
done

echo "── Go ──"
( cd "$GO_REPO" && go build -o "$WORK/snipe" ./cmd/snipe )
# One Go process handles every offer, and the dump is named by stage alone — so
# the run is done per offer to keep the two sides' dumps comparable. It costs
# nothing: --only-id skips the rest without a model call.
for id in "${IDS[@]}"; do
  SNIPE_HOME="$WORK/go" SNIPE_TIMING= SNIPE_NO_RAG=1 SNIPE_DUMP="$WORK/dump/go/$id" \
    "$WORK/snipe" run --skip-phase1 --skip-phase3 --p1-threshold 0 --only-id "$id" >/dev/null
done

# The request bodies, before the reports. A stage whose request already differs
# explains every difference downstream of it, and saying which stage that is
# turns "the prose changed" into one place to look.
for id in "${IDS[@]}"; do
  for stage in p2.jdparse p2.evidence p2.judgment; do
    a="$WORK/dump/js/$id/$stage.json"; b="$WORK/dump/go/$id/$stage.json"
    [ -f "$a" ] && [ -f "$b" ] || continue
    if cmp -s "$a" "$b"; then
      echo "#$id $stage request: identical"
    else
      echo "#$id $stage request: DIFFERS  ($a vs $b)"
    fi
  done
done
echo

exec python3 "$(dirname "${BASH_SOURCE[0]}")/compare-phase2.py" "$WORK" "${IDS[@]}"
