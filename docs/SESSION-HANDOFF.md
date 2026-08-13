# Session handoff — CV generation quality, 2026-08-13

Paste this whole file as the opening prompt of a new session. It is written to be
read cold: everything it asserts is either in the ledgers or reproducible from a
command given here.

Branch `develop`, working tree clean. Suite 1361 checks green, `npm run
typecheck` clean.

**All twelve backlog items are closed.** What remains is two user-layer edits
that only you can make, and whatever the next round of ideas turns out to be.

## Read these first, in order

1. `docs/CV-GENERATION-BACKLOG.md` — the pick-list, all twelve items struck
   through with results. Nothing in it is open.
2. `docs/PHASE3-GENERATION-LEDGER.md` §14–§19 — everything measured this session.
3. `docs/PHASE3-NEXT.md` — the closed-idea table, now four entries longer.
4. `batch/CLAUDE.md` — benchmark rules. Rules 4 and 7 both bit this session.

## State of the pipeline

Shipped and measured, in order:

- **`section_balance`** and its siblings `exp_bullets`, `proj_bullets`,
  `exp_starved`, `all_exp_starved_pct` (§14). Validated by reproducing §13's
  hand count exactly, 7/32 → 0/32.
- **`projMaxLines = 14`** in `cv-select.mjs` (§17) — caps the lines Projects may
  take of the 24-line budget. `SNIPE_PROJ_MAX_LINES=0` reverts it.
- **`skillForms`** in `cv-writers.mjs` (§15) — `skill_coverage` had been scoring
  3.5 skills a posting out of a 105-item taxonomy.
- **`select-sweep --shipped`** (§16) — `sweep`/`ablate`/`check` had been
  simulating the funnel production abandoned on 2026-08-08.
- **Best-of-two summary** (§19) — `generateSummary` ranked the two drafts it
  already had instead of shipping the first clean one. Summaries carrying no
  quantified achievement at all: **5/32 → 1/32**.
- **Dead-pin warning** moved to `local-runner.sh` preflight.
- **Reports 240–243 regenerated** against all of the above, into
  `output/2026-08-13_*`. The `output/2026-08-12_*` pair is the stale one and is
  yours to delete.

Current bench arms, newest last: `sum-v5` → `floor2` → `cap14` → **`bestof2`**
(the shipped state, and the control for anything next). `gradeord` exists and was
rejected — do not use it as a control.

| | coverage | exp_starved | commercial role at 1 bullet | no figure in summary |
|---|---|---|---|---|
| `sum-v5` | 0.564 | 1.03 | 22% | — |
| `floor2` | 0.552 | 0.75 | 19% | 5/32 |
| `cap14` | 0.530 | 0.469 | 3% | 5/32 |
| **`bestof2`** | **0.530** | **0.469** | **3%** | **1/32** |

`cap14` → `bestof2` moves nothing in selection by construction; it is a summary
change, and every selection metric reads a delta of exactly 0.000, which is the
check that it stayed in its lane.

## Two things waiting on the user — do not do these yourself

Both are user-layer files, and **both invalidate `bestof2` as a control**. Land
them together and re-baseline once, then regenerate 240–243 again.

1. **`config/profile.yml`** — `cv.pinned_projects` says `"Zero Trust SIEM"`; the
   title in `cv.md` is `Zero Trust Security Analytics Dashboard`. The pin has done
   nothing for 16 runs. The runner now warns at startup. Correcting it spends one
   of the three project slots and moves the benchmark for every arm.
2. **`cv.md` `## Skills`** — run
   `node batch/bench-tools/skills-gap.mjs --min 4 --shaped`. The two largest gaps
   are notation rather than capability, and are one-character edits now that
   `skillForms` reads a spaced slash as "either of these":
   `AI / LLM application development` (84 offers, 66% of the corpus) and
   `REST / RESTful APIs` (21, 16%). `cv.md` prose already proves both. Then
   **Git** (10 offers) is a genuine gap that is safe to claim. Everything below
   that — Azure 22, GCP 17, Terraform 11, SRE 9, Golang 8 — is a capability
   question the corpus cannot answer.

## What is left

Nothing on the list. The two user-layer edits above are the only outstanding
work, and both are the user's.

When they land, re-baseline once against `bestof2` and regenerate 240–243 again —
`bash batch/local-runner.sh --skip-phase1 --skip-phase2 --only-id <293|294|295|296>
--retry-failed`. **`--retry-failed` is the flag that matters**: without it the
runner reports success and re-runs nothing, because `p3_status` is already
`completed`.

If new ideas are wanted, the largest measured gap is still retention:
`differentiator_coverage` sits at 0.530 against an oracle ceiling of 1.000, and
`node batch/bench-tools/select-sweep.mjs attribute` says where it goes. Read
`PHASE3-NEXT.md`'s Experiment B first — targeted `cv.md` rephrasing is the one
large bucket with a mechanism behind it and it has never been tried.

## Closed this session — do not re-open without new information

- **Item 4, judge grade as a cut.** −0.062 held out, 0 wins 18 losses. The
  judge's zeros are **22.8% precise** against the Opus corpus and cutting them
  deletes 368 flagged differentiators. `gradeW` is **saturated at the shipped
  0.10** — the grades are binary, so raising it cannot reorder anything, and the
  control benchmark rule 4 demands does not exist.
- **Item 7, grade-ordered summary evidence.** Generic closer 4/32 → 8/32.
  Evidence order steers the opener, not the achievement sentence. Note this sits
  next to item 6, which shipped: ranking *whole drafts* works, reordering the
  evidence *inside* one does not.
- **Item 2, blanket experience floor.** Subsumed by item 3, which fixes 83% of
  the complaint for free. The last 3% costs 0.024 more coverage.
- **Item 8, synonym alignment.** 189 missed ATS terms across 32 offers, none
  technical. The metric scores only terms `cv.md` already contains, so a synonym
  for a term the CV lacks cannot move it by construction.
- **Item 10, generic proper-noun detector.** 0 real catches and 5 false
  positives on `sum-new`; it also cannot catch the lowercase clinical-AI miss
  that motivated it.

## How to work here

- **Smoke 3 offers before any 32-offer arm.**
  `SNIPE_LINE_BUDGET=24 SNIPE_MAX_PROJECTS=3 node batch/tailor-harness.mjs run
  <label> --temperature 0 --writer verbatim --sample sample32.tsv --limit 3`.
  Two bad summary changes were caught this way at 5 minutes each instead of 47.
- **An arm is ~47 min**, 1.45 min/offer, 32 offers. Selection changes may not
  reuse a select cache.
- **Do not commit while an arm runs.** The mongrel guard now ignores
  `docs/`-only commits and records `changed_files` in `meta.json`, but
  `batch/*.md` is *not* exempt — the Phase 2 and 3 prompts live there.
- **Read the rendered summaries, not the metric table.** Three separate changes
  this session passed every metric while visibly degrading output. The table
  cannot see a missing positioning line, a leaked internal phrase, or an
  achievement replaced by a generic closer.
- **The simulator ranks configurations well and is optimistic about cost.** It
  predicted −0.011 coverage for the section cap; the arm measured −0.022. It
  reproduces `floor2` to 0.000 after this session's repairs, so trust its
  ordering and discount its magnitudes.
- Run `node batch/bench-tools/select-sweep.mjs validate` before believing any
  sweep number, and pass `--shipped` to `ablate`/`check` or you are measuring the
  2026-08-07 funnel.

## The one judgement call made without the user

`projMaxLines` ships at 14 by default. It costs **−0.022 differentiator
coverage** and takes the commercial role from rendering as a single line on 6 of
32 offers to 1 of 32. §17 prices the trade in full, including two worked examples
of what a given offer gained and lost. `SNIPE_PROJ_MAX_LINES=0` reverses it
without a code change if that trade is judged wrong.
