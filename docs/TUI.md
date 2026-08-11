# snipe-tui.mjs — interaction detail

Behaviour of the TUI's non-obvious rows and keys. The entry-point map and the
data contract are in the root `CLAUDE.md`.


A failed queue row renders as `✗ Company — Role  see error  retry | debug`
(→ focuses each, Enter fires it). It ends at `debug` and drops the job link every
other row carries — `see error` is already a link, and the posting is not what you
act on next; `o` and the tracker still reach it:

- **see error** — a `file://` hyperlink to `batch/errors/<id>.txt`, written by the
  TUI's poll from the fullest source available. That is the `fatal()` JSON in
  `batch/scores/<id>.json` / `batch/evals/<id>.json`, **not** the log: both
  scripts write `fatal()` to stdout and only stderr to `batch/logs/`, so every
  score and eval log is 0 bytes. Phase 3 is the reverse — its log is the whole
  artifact. The state row's `error` column is truncated at 200 chars, so it is
  only the fallback.
- **retry** — re-runs the offer through all three phases with
  `local-runner.sh --only-id N --retry-failed`, overwriting the last attempt.
  Not a plain re-queue: `drain_queue` runs `--only-id N` with no flags, and the
  three phase guards (`:899`, `:976`, `:1021`) all key off the stored status, so
  a row that is `scored` + `evaled` with no report skips Phases 1–2 and fails
  Phase 3 forever. `--retry-failed` overrides all three and clears the
  `MAX_RETRIES` gate. Costs a fresh report number per attempt, orphaning the old
  one. `unavailable` rows get no retry — the runner refuses expired postings.
- **debug** — opens the *input* that phase read, to be edited in place before the
  retry reads it back: the fetched JD (`batch/jds/<id>.txt`) for Phases 1–2, the
  Phase 2 report for Phase 3. Falls back to the `score`/`eval` payload when the
  input never landed, which is itself the diagnosis. The file, not its folder —
  `batch/jds/` holds every JD ever fetched.

Mapping check: `node snipe-tui.mjs --retry-plan <p1s> <p2s> <p3s> <rnum> <id>`.

The footer **message** (right-hand side) is feedback on the last keypress, not a
log: `setMsg` stamps `S.msgAt` and `poll()` sweeps it after `MSG_TTL_MS` (5 s,
`SNIPE_MSG_TTL_MS` to shrink it for tests, as `SNIPE_REJECT_GRACE_MS` already
does). No countdown — it just goes. The sweep rides poll's existing 1 s tick, so
a message lives 5–6 s rather than exactly 5.

The footer hint line names **only what the focused element does** (`hintFor()`),
plus ` · ? keys`. The full reference is `HELP_ROWS`, shown by `?` as an overlay
that replaces the body and is closed by the next keypress (Ctrl-C excepted — a
help screen must not trap a quit). One place, not two: the window is routinely a
quarter of a screen wide, and a footer that spells every key out just truncates.
`HELP_ROWS` is ordered most-used first, since a short terminal clips the tail.
Two navigation tests used to assert on that footer string and so only proved the
hint line; they now anchor on each tab's own body.

A P1-gated row renders as `· Company — Role  P1 2.0 | proceed?  link` — the
same yellow action, because the `--p1-threshold` gate is a cost heuristic over a
4B pre-screen score, not a verdict. The number is `p1_score` straight from
`local-state.tsv`, drawn through the same `ScoreText` colour bands as an eval
score, because "how close was it" is the only input to the decision the row is
asking for. Rows whose `p1_score` is `-` fall back to the bare `P1-gated` label.

`proceed?` is a **question**, so it takes an answer as well as Enter — but only
while it is the focused stop, so no new global hotkeys and `q` still quits:

- **y** / **Enter** — proceed (below).
- **n** / **Backspace** / **Delete** — dismiss. Nothing on the pipeline changes;
  the row was already finished. The offer retires and the row renders as
  `· Company — Role  P1 2.0  link`. The answer is written to
  `batch/p1-declined.tsv` (`readMarkMap`/`writeMarkMap`, same id→ISO shape as
  `applied.tsv`/`skipped.tsv`) because the row list is rebuilt from disk every
  second and from scratch on every launch — an in-memory "no" would come back.
  Undo is deleting the line. Deliberately **not** the `x` skip mark: `toggleMark`
  requires an eval and drives the tracker to SKIP, and a gated offer has neither
  an eval nor a report number to `syncTracker` against.

- **proceed?** — `local-runner.sh --only-id N --p1-threshold 0`. Nothing is
  re-scored: the Phase 1 offer list skips anything already `scored` (`:918`), so
  the cached score and JD are reused and the run starts at Phase 2 — reusing
  Phase 1's work is what the flag already does, so there is no second code path
  for it. No `--retry-failed`: the row is `p1-gated`, not failed, and Phase 2's
  own guard (`p2_status != evaled`) already lets it through. Phase 3 follows on
  its own if the eval clears `--threshold`. The tracker row written by
  `write_tracker_p1_skip` is updated in place by the merge's company+role dedup —
  but only if the eval scores *higher* than the P1 pre-score; below it, the merge
  keeps the older row and the stale "no report — P1-gated" note with it.

