#!/usr/bin/env bash
#
# Copy the user layer out of the Node checkout into this one.
#
# The two implementations read the same tree shape, so this is a file copy and
# nothing more — no conversion, no migration, no schema. Run it as many times as
# you like while the cutover is in progress; the Node checkout is only ever read.
#
#   scripts/import-data.sh [node-repo]      default ~/projects/snipe
#   scripts/import-data.sh ~/projects/snipe --force
#
# --force is needed once this checkout's state file is newer than the source's,
# which means work has happened HERE that a copy would overwrite.
set -euo pipefail

SRC="${1:-$HOME/projects/snipe}"
DST="$(cd "$(dirname "$0")/.." && pwd)"
FORCE=""
for a in "$@"; do [ "$a" = "--force" ] && FORCE=1; done

[ -d "$SRC" ] || { echo "no such checkout: $SRC" >&2; exit 1; }
[ -f "$SRC/cv.md" ] || { echo "$SRC does not look like a snipe checkout (no cv.md)" >&2; exit 1; }
[ "$SRC" != "$DST" ] || { echo "source and destination are the same tree" >&2; exit 1; }

# A run holding the lock here would be writing the very files about to be
# replaced.
if [ -f "$DST/batch/state/runner.pid" ] && command -v flock >/dev/null &&
   ! flock -n "$DST/batch/state/runner.pid" true 2>/dev/null; then
  echo "a run is active in $DST — stop it first (snipe stop)" >&2
  exit 1
fi

# Work already done here is not something a copy gets to discard silently.
if [ -z "$FORCE" ] && [ -f "$DST/batch/state/local-state.tsv" ] &&
   [ "$DST/batch/state/local-state.tsv" -nt "$SRC/batch/local-state.tsv" ]; then
  echo "$DST/batch/state/local-state.tsv is newer than the source's." >&2
  echo "Runs have happened here since the last import. Re-run with --force to overwrite." >&2
  exit 1
fi

# copy <relative path> — a file or a whole directory, skipping what the source
# does not have. The trailing slash is stripped deliberately: `rsync src/dir/`
# means "the CONTENTS of dir", which lands them in the destination's parent, and
# on a repo root that is 500 loose report files next to go.mod.
copy() {
  local rel="${1%/}"
  [ -e "$SRC/$rel" ] || return 0
  local parent
  parent="$DST/$(dirname "$rel")"
  mkdir -p "$parent"
  rsync -a "$SRC/$rel" "$parent/"
}

# Personalisation.
for f in cv.md article-digest.md portals.yml config/profile.yml config/profile.md; do
  copy "$f"
done

# The record: tracker, reports, generated applications.
for d in data reports output interview-prep; do
  copy "$d/"
done

# The Node keeps all of this flat in batch/. This checkout splits it: state/ is
# the record and cache/ is derived, so "delete the caches" is one directory
# rather than a list of filenames to get right. Both sides are named here, so
# the rename is visible rather than implied.
#
# The runner's lock file is deliberately absent — it is a lock over a live
# process, and a copied one is a lie about this machine.
mkdir -p "$DST/batch/state" "$DST/batch/cache"
into() { # into <destination path> <source path under batch/>
  [ -e "$SRC/batch/$2" ] || return 0
  rsync -a "$SRC/batch/$2" "$DST/$1"
}
into batch/state/local-state.tsv  local-state.tsv
into batch/state/batch-input.tsv  batch-input.tsv
into batch/state/queue.txt        snipe-queue.txt
into batch/state/applied.tsv      applied.tsv
into batch/state/skipped.tsv      skipped.tsv
into batch/state/declined.tsv     p1-declined.tsv
# Six Opus calls to rebuild — derived, but not disposable.
into batch/state/cv-bank.json     cv-bank.json
into batch/cache/cv-index.json    cv-index.json
into batch/cache/jd-index.json    jd-index.json
into batch/cache/cv-spike.json    cv-spike.json
into batch/cache/judge-shots.json judge-shots.json
into batch/cache/timings.tsv      timings.tsv

for d in jds scores evals errors logs tracker-additions labels; do
  copy "batch/$d/"
done
mkdir -p "$DST/batch/work"

# Prompt overrides move from the Node's batch/ to this checkout's prompts/,
# which is where the code root keeps them.
mkdir -p "$DST/prompts"
for f in "$SRC"/batch/*.local.md; do
  [ -e "$f" ] && rsync -a "$f" "$DST/prompts/"
done

rm -f "$DST/batch/state/runner.pid"

# Nothing lives loose in batch/ any more, which makes the check exact: a file
# there means a copy landed a directory's CONTENTS where the directory should
# have gone. It is the one way this script can fail and still print a plausible
# summary, so it is checked rather than trusted.
LOOSE=$(find "$DST/batch" -maxdepth 1 -type f 2>/dev/null | wc -l)
if [ "$LOOSE" -gt 0 ]; then
  echo "warning: $LOOSE loose files in $DST/batch — an artifact directory was flattened" >&2
fi

count() { find "$1" -maxdepth 1 "${@:2}" 2>/dev/null | wc -l; }
lines() { [ -f "$1" ] && wc -l < "$1" || echo 0; }

printf '%s → %s\n' "$SRC" "$DST"
printf '  %s offers · %s reports · %s output dirs · %s queued\n' \
  "$(lines "$DST/batch/state/local-state.tsv")" \
  "$(count "$DST/reports" -name '*.md')" \
  "$(count "$DST/output" -mindepth 1 -type d)" \
  "$(lines "$DST/batch/state/queue.txt")"
