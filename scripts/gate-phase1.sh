#!/usr/bin/env bash
# Phase 1 equivalence gate (safe in place: ollama-scorer.mjs writes only to stdout)
#: score the same offers through the Node
# implementation and this one, and diff the artifacts.
#
# Both run now, against the same cv.md and profile, on the same cached JDs.
# batch/evals is NOT a control — it is a historical archive whose calibration
# corpus grew underneath it, so it scores about 0.5 higher than the same code
# does today. Compare two runs made now, or compare nothing.
#
#   scripts/gate-phase1.sh <node-repo> [n]
#
# Volatile fields (timestamps, paths, the model name) are stripped before the
# diff; everything else, prose included, must match byte for byte. Phase 1 is
# greedy at temperature 0, so any difference is signal.
set -euo pipefail

NODE_REPO="${1:?usage: gate-phase1.sh <node-repo> [n]}"
N="${2:-12}"
GO_REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${SNIPE_GATE_DIR:-/tmp/snipe-gate}"

rm -rf "$WORK"; mkdir -p "$WORK/home/batch/jds" "$WORK/home/batch/state" "$WORK/home/config" "$WORK/js"
cp "$NODE_REPO/cv.md" "$WORK/home/cv.md"
cp "$NODE_REPO/config/profile.md" "$NODE_REPO/config/profile.yml" "$WORK/home/config/"

# Spread the sample across the id range rather than taking the first n: the
# early ids are all one scan from one portal.
mapfile -t IDS < <(ls "$NODE_REPO/batch/jds" | sed 's/\.txt$//' | sort -n | awk -v n="$N" '
  { a[NR]=$0 } END { step = (NR>n) ? int(NR/n) : 1; for (i=1; i<=NR && c<n; i+=step) { print a[i]; c++ } }')

# The Go tree keeps its state under batch/state/; only the Node checkout is flat.
printf 'id\turl\tsource\tnotes\n' > "$WORK/home/batch/state/batch-input.tsv"
for id in "${IDS[@]}"; do
  cp "$NODE_REPO/batch/jds/$id.txt" "$WORK/home/batch/jds/"
  url=$(awk -F'\t' -v i="$id" '$1==i{print $2; exit}' "$NODE_REPO/batch/batch-input.tsv")
  printf '%s\t%s\tgate\t-\n' "$id" "${url:-manual:gate-$id}" >> "$WORK/home/batch/state/batch-input.tsv"
done
echo "sample: ${IDS[*]}"

echo "── Node ──"
for id in "${IDS[@]}"; do
  url=$(awk -F'\t' -v i="$id" '$1==i{print $2; exit}' "$WORK/home/batch/state/batch-input.tsv")
  ( cd "$NODE_REPO" && SNIPE_TIMING= node batch/ollama-scorer.mjs \
      --id "$id" --url "$url" --jd-file "$WORK/home/batch/jds/$id.txt" ) > "$WORK/js/$id.json" \
    || { echo "node scorer failed on #$id — see $WORK/js/$id.json" >&2; exit 1; }
done

echo "── Go ──"
( cd "$GO_REPO" && go build -o "$WORK/snipe" ./cmd/snipe )
SNIPE_HOME="$WORK/home" SNIPE_TIMING= "$WORK/snipe" run \
  --p1-threshold 0 --skip-phase2 --skip-phase3 >/dev/null

norm() {
  python3 -c "
import json,sys
d=json.load(open(sys.argv[1]))
for k in ('jd_file','scored_at','id','url','model','status'): d.pop(k,None)
print(json.dumps(d,indent=2,sort_keys=True,ensure_ascii=False))" "$1"
}

ok=0; bad=0
for id in "${IDS[@]}"; do
  if diff -q <(norm "$WORK/js/$id.json") <(norm "$WORK/home/batch/scores/$id.json") >/dev/null 2>&1; then
    ok=$((ok+1))
  else
    bad=$((bad+1))
    echo "#$id DIFFERS:"
    diff <(norm "$WORK/js/$id.json") <(norm "$WORK/home/batch/scores/$id.json") | head -20
  fi
done

echo
if [ "$bad" -eq 0 ]; then echo "phase1: $ok/$ok identical — GREEN"; else
  echo "phase1: $ok/$((ok+bad)) identical — $bad DIFFER"; exit 1; fi
