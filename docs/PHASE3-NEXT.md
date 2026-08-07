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

## Experiment A — adaptive per-project bullet allocation (do this first)

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

## Experiment B — targeted `cv.md` rephrasing (second)

Attacks the 49% bucket. Lower value than A and more fragile, so do it after.

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

## Open defect — the judge grades binary

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
