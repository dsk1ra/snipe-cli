# Phase 3 — what to do next

Written to end a session, not to summarise one. Everything measured so far is in
`docs/PHASE3-RETENTION-LEDGER.md`; this is only the part that decides what
happens next. Read that file's §4.7b first for where the numbers stand.

## Where things stand

Three changes shipped (PR #15), measured together end to end, 32 offers, judge
on, paired per offer:

| `ctl32` → `spike32` | before | after | delta | w-l | p |
|---|---|---|---|---|---|
| `differentiator_coverage` | 0.311 | **0.548** | +0.237 | 24-2 | <0.001 |
| `grade_yield` | 0.622 | 0.710 | +0.088 | 23-7 | 0.005 |
| `ats_coverage` | 0.636 | 0.696 | +0.061 | 25-4 | <0.001 |
| `grounding` | 0.971 | **1.000** | +0.029 | 26-0 | <0.001 |
| `noise_rate` | 0.301 | 0.278 | −0.023 | 13-14 | ns |
| `metric_fab` / `product_fab` / `num_lost` | 0.000 | 0.000 | — | — | — |

The oracle ceiling at the current page budget is **1.000**, so **0.452 of
headroom remains**. That is the target.

**Superseded by Experiment A** (below): coverage is now 0.649 and 0.351 of
headroom remains. The attribution below was re-run under the new allocation —
314 missed differentiators became 278, the "beaten by its own project siblings"
bucket 153 → 125. The shares barely moved, so the reading still stands; the
counts do not.

## Why the remaining 0.452 is lost

Reproduce with `node batch/bench-tools/select-sweep.mjs attribute` — do not
trust the numbers below without re-running it, they move when the ranker does.

```
split=all · n=127 offers · 314 missed differentiators

cause                                        count  share  fixable by
  beaten by its own project siblings           153   49%  wording / ranker
  project never made the cut                    71   23%  project scoring
  >2 differentiators in one project             67   21%  ALLOCATION ONLY
  experience bullet lost                        23    7%  wording / ranker
```

The 21% is the interesting number. Those are offers where a project **did**
reach the page, had three or more differentiators flagged for that posting, and
only two bullets fit. No ranker and no rewrite can recover them — the budget is
allocated as a flat `PROJ_BULLETS` per project regardless of how well any one
project matches.

## Experiment A — DONE, shipped. +0.101 differentiator coverage

Ran 2026-08-07. The hypothesis held: the page budget was right and its
distribution was not.

`cv-select.mjs` now spends one total project-bullet budget (8 — `maxProjects × 2`,
exactly what the flat rule rendered) across the projects that survived the top-4
cut: one bullet apiece, then the remainder to the highest-scoring bullets
anywhere, capped at 4 per project. Greedy over pooled bullets rather than a
proportional split of the scores, because the scores are cosines in a narrow band
— a proportional rule off 0.58 vs 0.55 is noise, while "whose next bullet is
best" is the question the page is actually asking.

End to end, 32 offers, judge on, fresh select cache, paired (`spike32` → `alloc32`):

| metric | before | after | delta | w-l | p |
|---|---|---|---|---|---|
| `differentiator_coverage` | 0.548 | **0.649** | +0.101 CI [0.039, 0.161] | 15-4 | 0.019 |
| `grade_yield` | 0.710 | 0.741 | +0.030 CI [0.012, 0.049] | 20-10 | 0.099 |
| `mean_grade` | 1.666 | 1.705 | +0.039 | 20-10 | 0.099 |
| `ats_coverage` | 0.696 | 0.681 | **−0.015** CI [−0.028, −0.002] | 7-17 | 0.064 |
| `mean_bullets` | 8.000 | 8.000 | — | — | — |
| `grounding` / `metric_fab` / `product_fab` / `num_lost` | — | — | unmoved | — | — |

`mean_bullets` identical to three decimals is the load-bearing row: this is
redistribution, not a bigger page. Realised shapes are 4/2/1/1 (4 offers),
4/1/1/2 (5), 3/3/1/1 (2) and eleven others; 2/2/2/2 no longer appears at all.

**The simulator predicted +0.096 and the real run measured +0.101** — the closest
sim-to-real agreement yet, and the reason it was worth generalising `simulate()`
first. `projCap 2` reproduces the old flat allocation at exactly +0.000, so the
generalisation is null-safe rather than a new ranker wearing a flag.

Held out over 66 offers before the end-to-end run: +0.077 CI [0.040, 0.115],
27-7, p=0.0008 — against a *spike-6* baseline. `check`'s baseline was spike-0,
which double-counted spike's own gain and read as +0.132; it now takes
`--base-spike`. Cap 6 was worth a further +0.006 and renders three projects as a
bare title, so cap 4 ships.

Page cap holds: all 32 offers render at 2 pages on ladder step 0, no descent.

### Two things this turned up

1. **`trim()`'s metric-bullet guarantee overrides the ranker at keep=1**, which
   nothing reached before allocation existed. All 57 single-slot project bullets
   carry a digit against a 72% base rate (p ≈ 1e-8); the swap fires on 42% of
   1-slot projects. It displaced a flagged differentiator exactly once in 32
   offers, so it is not eating the gain — but 22 bullets chosen for having a
   number rather than for matching the posting is the first suspect for the
   `ats_coverage` −0.015. Cheap to test offline before spending another 37 min.
2. **The 42 s judge figure was never a measurement.** It was the plan's estimate,
   superseded by `p3-judge 66 s/call` in the generation ledger and left standing
   in this ledger's §4.11 heading and `batch/CLAUDE.md`. Both corrected. Current wall
   clock is 67 s/offer median (n=32 spike32, n=32 alloc32 — the allocation adds
   no model call and measured +0.4 s, noise).

## Experiment A — the original plan, for the record

**Hypothesis.** The page budget is right; its *distribution* is not. A posting
that is 80% about the thing one project did should spend more of its project
budget on that project and less on the others.

**Why it is first.** It is the only thing that can touch the 21%, it likely
helps the 23% too (a project scored by its best bullet is a coarse gate), it
costs no model call, and it does not disturb `cv.md` or the label corpus.

**Shape.** Today `local-pdf-offer.mjs` renders `SNIPE_PROJECT_BULLETS` (2) from
every project and `cv-select.mjs` keeps the top `maxProjects` (4). Instead,
distribute a fixed *total* project-bullet budget (8, matching today's 4×2) across
projects in proportion to score — e.g. 4/2/1/1 for a sharply matching posting,
4×2 when the field is flat. Guarantee ≥1 per shipped project so no project
becomes a bare title.

**Measure.** `select-sweep.mjs` simulates the funnel, so sweep the allocation
rule offline for free before touching the pipeline. `simulate()` currently
hardcodes `PROJ_BULLETS` as a per-entity cap — that is the line to generalise.
Tune on `--split train`, confirm with `check --split test`, then one end-to-end
`run-spike32.sh`-style run to confirm, and **`validate` must still pass** or the
simulator no longer describes the pipeline.

**Watch for.** The 2-page cap is hard and already binding — 3 bullets on every
project rendered a 3-page PDF. Total budget must stay constant; this is
redistribution, not expansion. The density ladder in `local-pdf-offer.mjs:934`
drops `projBullets` before experience bullets, so a per-project scheme has to
survive being ladder-trimmed.

## Experiment C — CLOSED. The project gate is not the constraint

Ran 2026-08-07, straight after A, because A was predicted to help the "project
never made the cut" bucket and moved it by exactly nothing: 71 differentiators
lost to it before allocation and 71 after. Allocation only spends the budget on
projects the gate already admitted, so the gate looked like the obvious next
lever.

It is not, and the attribution says why rather than merely that:

| | total misses | project dropped | over slots | siblings | exp |
|---|---|---|---|---|---|
| shipped (keep 4) | 281 | **71** | 60 | 126 | 24 |
| keep all 5, same budget | 282 | **0** | 106 | 152 | 24 |

Admitting the fifth project drives its bucket to zero and the same 71 losses
reappear as +46 "more differentiators than slots" and +26 "beaten by its own
project siblings". **The 8-bullet budget is the binding constraint, not the
gate.** The CV has 5 projects and ships 4, so the gate chooses which one to
drop; re-scoring that choice is near zero-sum by construction and measured that
way (`gateK` 4: +0.008 held out, 4 of 66 offers, p=0.375).

The consequence for what comes next: of the 281 remaining misses, 60 are
arithmetically unreachable at this budget and **150 (siblings + experience) are
ranking or wording problems** — which is Experiment B's territory, and now the
only large bucket with a mechanism behind it.

## Experiment B — targeted `cv.md` rephrasing (now first)

Attacks the largest remaining bucket (45% after A, 125 of 278 misses). More
fragile than A was, and the `attribute` table it selects candidates from must be
re-read under the new allocation before any rewrite is chosen — the numbers
quoted below predate it.

**Mechanism.** A bullet that reads moderately relevant to *every* posting is
discounted by spike, correctly. Cutting connective tissue lowers a bullet's
corpus mean without lowering its peak cosine — a pure spike gain. Making a
bullet narrower is *not* the same edit and can lose more than it wins.

**Candidates**, worst first, from the `attribute` table. `generic` is the corpus
mean; high means the wording is the problem, low means it is crowding and
rewriting will not help.

**#21 — Zero Trust opener** (29/56 missed, ships 41%, generic **0.550**, worst in the CV)
> current: Won MongoDB as client through a competitive pitch; served as Project Manager and Lead Engineer for a 6-person cross-functional team (3 software, 2 security engineers), achieving Distinction across implementation, presentation, and evaluation

Four unrelated signals; "competitive pitch" and "achieving Distinction across
implementation, presentation, and evaluation" are academic framing that matches
everything weakly.
> proposed: Won MongoDB as a paying client through competitive pitch, then led delivery as Project Manager and Lead Engineer over 6 people — 3 software, 2 security engineers; graded Distinction.

**#32 — Kafka/RabbitMQ** (25/50, ships 35%, generic 0.510)
The distinctive claim — both brokers built, choice made on evidence — is buried
behind "Conducted a comparative broker analysis"; 58 words over four topics.
> proposed: Built parallel Kafka and RabbitMQ implementations to choose between them on evidence, selecting Kafka for its replayable log: independent consumer offsets let inventory, finance, and fulfilment consume the same events at their own pace, with disk retention giving audit trail and replay for failure recovery.

**#26 — Snipe fabrication cut** (37/62, ships 32%, generic 0.488)
62 words leading with metrics; the engineering ("schema-constrained evaluator",
"evaluation harness") sits in the final clause where a requirement match is
least likely to land.
> proposed: Replaced free-form generation with a schema-constrained evaluator and a benchmark harness gating every model and prompt change, cutting fabricated job requirements ~9× (8.2% → 0.9% of extracted claims) over 115 offers and raising rank agreement +0.317 Spearman (95% CI [0.153, 0.498], 0 of 4,000 resamples favouring baseline).

**#31 — saga pattern** (15/19, ships 23%, generic 0.455)
Reads like a textbook definition with no system in it.
> proposed: Coordinated distributed transactions across inventory, orders and fulfilment with the saga pattern, trading strict consistency for compensating actions across asynchronous service boundaries.

**Leave alone.** #17 / #18 / #19 (Re:Link, generic 0.398–0.441) and #27 (0.380,
the most specific bullet in the CV, still ships 17%). These are sharp already
and lose to crowding — Experiment A is their fix, not rewording.

### Rules for editing `cv.md` without destroying $49 of labels

The 128 label files key `grades` and `differentiators` by **atom id**, and the
`why` fields judge what a bullet *demonstrates*. So:

- **Rephrasing is free.** Same count, same order, same claims → ids still mean
  the same atom. Migrate `label.atoms[].text` in all 128 files mechanically
  (the *metrics* match output by text overlap; the grades do not).
- **Re-scoping is not.** Adding or removing a technology, metric or scope claim
  changes what the grade was about.
- **Merging, splitting, adding, deleting or reordering bullets voids the entire
  corpus**, not just the touched atoms, because every id shifts.

After any edit: regenerate exemplars (`node batch/goldset.mjs export-shots --ids
5,50`) or the judge silently disables itself; `cv-index.json` and `cv-spike.json`
self-invalidate; gold-set ticks whose atom text moved will drop and need
re-ticking. Cheap insurance if re-scoping: re-grade ~12 offers (~$4.50) and check
the new grades agree with the old ids — not the full $49.

Each proposed rewrite is testable for **$0**: embed the new text, recompute
corpus means, re-simulate over the 128 offers, keep only what measurably helps.
Do that rather than shipping them on judgement.

## Open defect — the exemplars pin every project bullet to 0

Found 2026-08-07 while building graded exemplars, and larger than the
binarisation it was found under.

Exemplars carry `wantText`, the atoms a human ticked, and `judgeGradesFull`
demonstrates them as `want.has(text) ? 3 : 0` over its item list. Those items
are per bullet. A ticked *project* enters `wantText` as its `cvAtoms` text,
which is the project **title** — `cvAtoms` makes a project one atom (14 atoms)
while `cv-select` and the label corpus are per bullet (33). No item
`cv-select` grades is ever a title, so:

```
shipped exemplar   demonstrated histogram   non-zero project bullets
offer 5            28/0/0/5                 0/24
offer 50           30/0/0/3                 0/24
```

Both demonstrations teach the judge that all 24 project bullets are 0, whatever
the human ticked. Projects hold 24 of 33 atoms and most differentiators, and
demonstrations beat the system prompt. `loadExemplars`' guard misses it because
it validates `want` against `cvAtoms` text — the same space that produced the
titles, not the space the consumer matches in.

### Attempt 1 — graded exemplars from the Opus corpus. Rejected, −0.148

The retention ledger scoped the fix as needing a human to re-rate 0–3. It does
not: `batch/bench/opus/labels/` already grades every atom 0–3 on 128 offers, per
bullet, in exactly `cv-select`'s text space (verified 33 of 33 identical and in
order). Built as `export-shots --graded`, it fixes both defects at once —
offers 50 and 103 demonstrate 11/9/8/5 and 6/9/10/8, with 16/24 and 19/24
project bullets non-zero.

It works as designed and ranks worse. Held out on `goldset-2`, 12 offers:

| variant | binary (shipped) | graded | delta |
|---|---|---|---|
| `judge` alone | 0.801 | 0.656 | −0.145 |
| `cos+judge-0.10` (shipped blend) | 0.781 | 0.633 | **−0.148** |

The judge's output distribution moved exactly as intended — mid-scale usage
32% → 83% — and accuracy fell anyway. **So the binary output was not itself what
was costing accuracy.** Three explanations this run cannot separate:

1. **Rubric mismatch**, the main suspect: the demonstrations teach the Opus
   labeller's 0–3 rubric and the metric scores against the goldset human's
   keep/drop ticks. Two people, two notions of relevant.
2. **The metric leans binary by construction.** Pair accuracy over binary gold
   ticks rewards a judge that separates hard into 0/3; a graded spread is a
   weaker separator even where it is more truthful. Standing rule 5 — ask what
   the metric reads if the change lands — and it was asked after the 22 minutes
   rather than before.
3. The exemplar pair moved `{5,50}` → `{50,103}`; the binary control on
   `{50,103}` was the option not taken.

### Attempt 2 — expand a ticked project into its bullets. Rejected, −0.045

Keeps the binary taste the gold sheet rewards and fixes only the space bug: a
ticked project contributes its own bullets to `wantText`, so its bullets
demonstrate as 3 instead of 0. Imports no second rubric, and uses the **same
`{5,50}` pair production ships**, so attempt 1's exemplar-pair confound is gone.
`loadExemplars`' guard had to widen to the consumer's space (`parts`) or the
expanded file is dropped as unmatched — the guard validated titles only, so the
broken form passed and the correct one would not have.

| variant | shipped (broken) | expanded | delta |
|---|---|---|---|
| `cos+judge-0.10` (shipped blend) | 0.781 | 0.736 | **−0.045** |
| `judge` alone | 0.801 | 0.727 | −0.074 |
| `prior-only`, `base-cos` (no judge) | 0.810, 0.545 | identical | — |

The no-judge rows are byte-identical across arms, so this is not benchmark rule
6's zero-width plumbing failure.

### What both attempts mean

**The defect is load-bearing.** Two independent fixes, one graded and one
binary, both make ranking worse — so the judge's accuracy does not come from
faithfully reproducing what the human ticked. What the two fixes share is
*generosity*: the shipped exemplars demonstrate 5 and 3 of 33 items as non-zero,
expansion takes that to 18 and 12, grading takes it to 22 and 27. Accuracy falls
monotonically as the demonstrations get more generous (0.781 → 0.736 → 0.633).

That is standing rule 8 arriving from the other side — it warned that generosity
buys rho without buying truth, and here generosity does not even buy rho. The
working hypothesis is that the demonstrations calibrate *strictness* rather than
teach taste, and that the accidental all-projects-are-0 is acting as a severity
prior the metric likes.

**Do not "fix" this without a measurement that can tell severity from taste.**
Pair accuracy over binary gold ticks cannot: it rewards hard separation by
construction. The metric that could is differentiator coverage in production's
33-item space, which costs a full 128-offer or 66-offer re-grade — the run not
yet taken. Until then the defect stays documented and unfixed, which is the
honest state, not an oversight.

Everything needed to retry is committed: `export-shots --graded` / `--expand`,
the widened guard, `SNIPE_JUDGE_SHOTS` to keep candidates away from production,
and both grade caches under `batch/bench/`.

## Superseded — the judge grades binary

Not caused by any of this work; found during it. See ledger §4.10.

```
one-field grade histogram, 128 offers × 33 atoms:  0: 3026   2: 30   3: 1135
```

`JUDGE_SYSTEM` demands the full range and gets 30 mid-scale gradings out of
4191. The exemplars are built as `want.has(text) ? 3 : 0` from binary human
ticks, so every demonstration is a 0 or a 3 and demonstrations beat
instructions. The retrieval ledger already priced binarisation at **0.03 pair
accuracy**, so the shipped judge has been paying that since it landed. The fix
is graded exemplars — a human rating a sample 0–3 rather than ticking keep/drop
— plus its own benchmark. Untouched.

## Closed, do not re-open without new information

| idea | result |
|---|---|
| bigger writer model (9B, 30B) | loses grounding in **every** offer it changes |
| pre-written variant bank | null on all label metrics; 22–8–2 against on blind preference |
| MMR redundancy penalty | +0.002 on top of spike — subsumed |
| reserved differentiator slots | +0.005, negative at 6 |
| judge distinctiveness rating | does not beat simply weighting `grade` higher (p=0.27) |
| deleting the judge | costs −0.021 held out; it stays |
| spike background from `jd-index.json` | −0.025, wrong scale |
| project gate scored by top-k mass (`gateK`) | +0.008 held out, 4 of 66 offers, p=0.375 |
| keeping all 5 projects on the same budget (`projKeep`) | −0.005 coverage, −0.009 yield |
| graded exemplars from the Opus label corpus | −0.148 pair accuracy held out; see below |
| expanding a ticked project into its bullets | −0.045 pair accuracy held out; the defect is load-bearing |

## Standing rules that cost time to learn

1. **A selection change must not reuse a select cache.** The key is over the CV
   and requirements, not the ranker — a stale cache serves pre-change selections
   and the run looks like a valid A/B while measuring nothing.
2. **`validate` before believing any sweep.** The simulator's deltas mean
   nothing until it reproduces a number a real run measured.
3. **Identical results across a weight sweep are plumbing, not a null.** Cost an
   hour when `--distinct` returned the same figure at 0.05 and 0.4, and again
   when zsh passed `"--spike 6 --lambda 0.3"` as a single argument.
4. **Control for the cheap explanation.** The distinctiveness rating looked like
   the best config measured anywhere until it was compared against simply
   raising `gradeW`, which it did not beat.
