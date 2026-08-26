#!/usr/bin/env bash
# Renders source.tex both ways over every cached cv-content.json and diffs.
#
# fill-cv-template.mjs reads cv.md, config/profile.yml and templates/ out of its
# own repo and writes only to --output, so it is safe to run in place — the
# output goes to a scratch dir, never into the offer folder it came from.
set -uo pipefail
REPO="${1:-$HOME/projects/snipe}"
WORK="${WORK:-$(mktemp -d)}"
GO="${GO:-$(cd "$(dirname "$0")/.." && pwd)}"

# Ladder steps, as local-pdf-offer.mjs drives them: skills, bullets, projBullets.
STEPS=("0 4 4" "0 3 2" "5 3 2" "4 2 1")
ROLE="${ROLE:-}"

# The corpus carries only the current content shape. The synthetic fixtures cover
# the branches it never took: the legacy string fields, an explicit empty array
# (which means "render the CV's own", not "fall back"), a JSON null, a missing
# key, an unmatched project name and an invented skill item.
FIXTURES="${FIXTURES:-$(dirname "$0")/fixtures/fill}"

pass=0; fail=0
LIMIT="${LIMIT:-0}"
n=0
for content in "$FIXTURES"/*.json "$REPO"/output/*/cv-content.json; do
  n=$((n+1)); [ "$LIMIT" != 0 ] && [ $n -gt "$LIMIT" ] && break
  id=$(basename "$(dirname "$content")")
  for step in "${STEPS[@]}"; do
    read -r ms mb mpb <<<"$step"
    tag="$id/$ms-$mb-$mpb"
    js="$WORK/js.tex"; go="$WORK/go.tex"
    args=(--content "$content" --output "$js")
    [ "$ms"  != 0 ] && args+=(--max-skills "$ms")
    [ "$mb"  != 0 ] && args+=(--max-bullets "$mb")
    [ "$mpb" != 0 ] && args+=(--max-project-bullets "$mpb")
    [ -n "$ROLE" ] && args+=(--role "$ROLE")
    if ! node "$REPO/batch/fill-cv-template.mjs" "${args[@]}" >/dev/null 2>&1; then
      echo "  $tag: node failed"; fail=$((fail+1)); continue
    fi
    if ! "$GO/gate" fill "$REPO" "$content" "$go" "$ms" "$mb" "$mpb" "$ROLE"; then
      echo "  $tag: go failed"; fail=$((fail+1)); continue
    fi
    if diff -q "$js" "$go" >/dev/null; then
      pass=$((pass+1))
    else
      fail=$((fail+1))
      echo "  $tag: DIFFERS"
      diff "$js" "$go" | head -12
    fi
  done
done
echo
echo "fill: $pass/$((pass+fail)) identical$([ $fail -eq 0 ] && echo ' — GREEN')"
[ $fail -eq 0 ]
