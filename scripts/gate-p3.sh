#!/usr/bin/env bash
# Compares the tailored content — selection, verbatim writer, skill block,
# education modules — against local-pdf-offer.mjs over real offers.
#
# Both sides are given cv.md with its entity headings respelled to `###`: the JS
# parser matches only `##`/`###` headings, so on the live bold-heading CV its
# selector silently returns the CV unchanged and there is nothing to gate. The
# respelling is generated from cv.md on every run rather than kept as a fixture
# file — the fixture drifted twice, and a gate run against a stale copy of the CV
# is a gate against nothing. Not one word of content is changed.
#
# Needs Ollama for the embedder. Both sides embed the same texts, so the
# comparison is of the arithmetic on top, not of the vectors.
#
# Expect 30-32 of 32. The residual is always the same shape: two bullets inside
# one entry swapping places, both kept. Each implementation computes its own
# spike background and the two means differ by ~7e-5 — the vectors are identical
# (Ollama is deterministic) and both cosines are the same expression, so it is
# float accumulation order over 302 report sets. That is enough to flip bullets
# whose scores are within 1e-4, and nothing else. A failure that changes WHICH
# bullets are kept, or any other field, is a real regression.
#
# Run it twice before believing a failure: a cold batch/cache/cv-spike.json is
# rebuilt by the first offer of the run and read by the rest, so the first
# comparison of a cold run is made against a different mean than the other 31.
set -uo pipefail
GO="$(cd "$(dirname "$0")/.." && pwd)"
# Absolute, so a relative argument cannot mean one directory to cp and another
# to a symlink target. The JS harnesses resolve theirs for the same reason.
REPO="$(cd "${1:-$GO}" && pwd)"
WORK="${WORK:-$(mktemp -d)}"
CV="$WORK/cv-headings.md"
mkdir -p "$WORK"
awk '
  /^\*\*[^*]+\*\*[ \t]*$/ {
    name = $0
    sub(/^\*\*/, "", name); sub(/\*\*[ \t]*$/, "", name)
    if (name ~ /^(Summary|Experience|Projects|Education|Certifications|Skills|Languages)$/)
      print "## " name
    else
      print "### " name
    next
  }
  { print }
' "$GO/cv.md" > "$CV"
LIMIT="${LIMIT:-8}"

# Build the gate rather than trusting whatever is lying at the repo root. It was
# a day stale the first time anyone looked, which makes this a gate against the
# last binary someone happened to build — the same shape as gate-phase1.sh
# spending three days comparing a real Node artifact against a Go side that
# wrote nothing because the fixture moved and the script did not.
( cd "$GO" && go build -o "$GO/gate" ./cmd/gate ) || exit 2

# The spike term is a mean over every past report, so both trees must hold the
# same reports/ or the two backgrounds differ by ~0.001 and flip near-ties in the
# bullet ranking. Sync the corpus (data, not code — the oracle stays frozen) and
# drop both caches so each rebuilds over the same offers.
for f in "$GO"/reports/*.md; do
  [ -f "$REPO/reports/$(basename "$f")" ] || cp "$f" "$REPO/reports/"
done

pass=0; fail=0; n=0
for ev in "$GO"/batch/evals/*.json; do
  id=$(basename "$ev" .json)
  jd="$GO/batch/jds/$id.txt"
  num=$(python3 -c "import json,sys;d=json.load(open('$ev'));print(d.get('report_num') or '')" 2>/dev/null)
  [ -z "$num" ] && continue
  report=$(ls "$GO"/reports/"$num"-*.md 2>/dev/null | head -1)
  [ -f "$jd" ] && [ -n "$report" ] || continue
  score=$(python3 -c "import json;print(json.load(open('$ev')).get('score') or 0)")
  python3 -c "import sys;sys.exit(0 if float('$score')>=3.0 else 1)" || continue

  n=$((n+1)); [ "$LIMIT" != 0 ] && [ $n -gt "$LIMIT" ] && break

  rm -rf "$WORK/js" && mkdir -p "$WORK/js"
  # SNIPE_PROJ_MAX_LINES: local-pdf-offer.mjs still defaults it to 14, the value
  # swept against the old 24-line budget. cv-select.mjs's own default moved to 13
  # when the budget became 22 (the same 58% share of a smaller page) and the
  # caller's env default was never updated with it. Both sides are pinned to 13
  # so the gate compares implementations rather than that stale default.
  # SNIPE_ADDITIONS pins the tracker write to scratch. It only fires below
  # threshold, which --threshold 0 rules out, but the oracle runs in this
  # checkout and a real tracker row must not depend on an argument.
  if ! (cd "$REPO" && SNIPE_PROJ_MAX_LINES=13 SNIPE_ADDITIONS="$WORK/additions" \
        node batch/local-pdf-offer.mjs \
        --id "$id" --url "x" --report-path "$report" --report-num "$num" \
        --jd-file "$jd" --eval-score "$score" --company "gate" --role "gate" \
        --date 2026-01-01 --threshold 0 --cv-file "$CV" --bench-dir "$WORK/js" \
        >/dev/null 2>&1); then
    echo "  #$id: node failed"; fail=$((fail+1)); continue
  fi
  jsraw=$(ls "$WORK"/js/*/cv-content.json 2>/dev/null | head -1)
  [ -n "$jsraw" ] || { echo "  #$id: node wrote no content"; fail=$((fail+1)); continue; }

  "$GO/gate" p3norm "$jsraw" "$WORK/js.json" || { fail=$((fail+1)); continue; }
  "$GO/gate" p3 "$GO" "$CV" "$report" "$jd" "$WORK/go.json" || { fail=$((fail+1)); continue; }

  if diff -q "$WORK/js.json" "$WORK/go.json" >/dev/null; then
    pass=$((pass+1))
  else
    fail=$((fail+1))
    echo "  #$id: DIFFERS"
    diff "$WORK/js.json" "$WORK/go.json" | head -20
  fi
done
echo
echo "p3: $pass/$((pass+fail)) identical$([ $fail -eq 0 ] && echo ' — GREEN')"
[ $fail -eq 0 ]
