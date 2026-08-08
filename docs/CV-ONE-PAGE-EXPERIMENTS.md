# One page — experiment plan, targets, and desired outcomes

Companion to `docs/CV-ONE-PAGE-PLAN.md` (which holds the measurements that
motivate this). This file is the falsifiable part: what gets run, what number
decides it, and what result kills it.

Baseline is **v0.1.0** (tagged, pre-release). Everything below lands as **v0.2.0**,
because it changes CV output shape.

---

## 0. The blocker nobody would have noticed

**`opus-metrics.mjs` cannot see the page.** It reads `cv-content.json`
(`opus-metrics.mjs:188`), and `local-pdf-offer.mjs:877` writes that file and then
`process.exit(0)`s in bench mode — *before* the density ladder runs:

```js
// Benchmark stop. […] cv-content.json is captured pre-ladder, so the ladder's
// project trimming cannot mask what the model actually returned.
if (args.benchDir) { out({ … }); process.exit(0); }
```

That stop is correct for what it was built for — it stops the ladder from
flattering a *generation* change. But it means **every metric in the suite scores
the content the selector produced, not the content that reached the page.** Run any
one-page experiment against it today and all arms score identically, and the whole
programme reads as a clean null.

This is CLAUDE.md benchmark rule 5 in a new costume: *what does this metric read if
the change lands?* Answer, today: nothing.

### E0 — unblock the harness. Do this first; nothing else is measurable without it

Two options, and the choice is load-bearing for the design:

| | approach | consequence |
|---|---|---|
| **a** | `--bench-post-ladder` flag: run the ladder in bench mode, re-serialise `cv-content.json` after it, measure that | keeps the ladder as the thing that enforces the page |
| **b** | make selection itself page-aware, so pre-ladder content already *is* the page | ladder becomes a safety net that rarely fires; pre/post become the same file |

**Take (b), and add (a) only as the check that (b) worked.** If the line budget
lives in `cv-select.mjs`, the content the selector emits already fits, the existing
bench stop stays honest, and the ladder stops being load-bearing. (a) alone would
mean optimising a truncation step, which is the wrong place — the ladder cuts by
count and cannot know which bullet carries the differentiator.

Also needed either way: a **`page_count`** metric. It is the hard gate, and it does
not exist yet.

**Desired outcome:** a metrics run where a deliberately over-long arm scores *worse*
than the shipped one. If it does not, the harness is still blind — stop and fix it.

---

## 1. Targets

Current shipped state (v0.1.0), 32 offers, judge on, paired, from
`PHASE3-NEXT.md`:

| metric | now (2 pages) |
|---|---|
| `differentiator_coverage` | 0.649 |
| `grade_yield` | 0.710 |
| `ats_coverage` | 0.696 |
| `grounding` | 1.000 |
| `metric_fab` / `product_fab` / `num_lost` | 0.000 |
| pages | **2** |

### Gate — binary, non-negotiable

| | target |
|---|---|
| `page_count == 1` | **32 of 32 offers** |

Not "most". An arm that fits 30 and overflows 2 has not solved the problem, and a
mean page count is a meaningless number.

### Primary

| | target | stretch |
|---|---|---|
| `differentiator_coverage` at one page | **≥ 0.55** | ≥ 0.60 |

Coverage **will fall**. One page holds ~28 bullet-lines against today's 40, so ~30%
of the content has to go. The honest goal is to lose less than the content does —
if 30% fewer lines costs less than 30% of the differentiators, the ranker is doing
its job. Below 0.55 the one-page constraint is costing more than it is worth and
the conversation changes from "how" to "whether".

### Guards — a breach kills the arm regardless of coverage

| metric | must stay | why |
|---|---|---|
| `grounding` | **1.000** | the verbatim writer's whole justification |
| `metric_fab`, `product_fab`, `num_lost` | **0.000** | fabrication, non-negotiable |
| `ats_coverage` | **≥ 0.66** | 0.696 today; the allocation already costs −0.015 and fewer bullets will cost more, but a keyword-starved CV fails the filter before a human sees it |
| `grade_yield` | ≥ 0.65 | 0.710 today |

### Noise floor — read this before claiming anything

Per CLAUDE.md, take the **wider** floor when measurements disagree:

| metric | floor |
|---|---|
| `grounding` | ±0.020 |
| `mean_bullets` | ±0.120 |
| `ats_coverage` | ±0.002 |
| `selection_regret` | ±0.001 |
| `summary_cv_fit` | ±0.004 |
| `summary_jd_fit` | ±0.016 |

`differentiator_coverage` had no published A/A floor. **It does now: 0.000.**

Ran 2026-08-08. Two arms, `aa1` and `aa2`, byte-identical config — `sample32.tsv`,
`--writer verbatim`, `--temperature 0`, `SNIPE_PROJECT_BULLETS=4`, and **no
`SNIPE_SELECT_CACHE`**, so selection ran fresh in both. 39.2 and 37.5 minutes.

| metric | aa1 | aa2 | delta | CI95 | w-l |
|---|---|---|---|---|---|
| `differentiator_coverage` | 0.643 | 0.643 | 0.000 | [0.000, 0.000] | 0-0 |
| every other metric | — | — | 0.000 | [0.000, 0.000] | 0-0 |

A zero-width CI is rule 6's signature, so it was checked rather than believed:

- **All 32 `cv-content.json` are byte-identical** between the arms.
- Neither `meta.json` records a select cache; only `SNIPE_PROJECT_BULLETS=4`.
- `judgeGrades` has no disk cache — it calls the 30B every time.
- The timing logs show **191 real model calls** across the two arms, 3 per offer
  (judge, summary, main JSON).

So the arms were independent and Phase 3 is genuinely deterministic. **Any nonzero
delta from here is signal.** The `≥ +0.05` bar below is now a judgement about
whether a change is worth its complexity, not about whether it can be detected.

**Why this differs from the floors in CLAUDE.md rule 2.** Those were measured
under `--writer model`. Deleting the 7B tailor call deleted the nondeterminism
with it: the calls that remain emit a short summary and a schema-constrained list
of small integers, which have far less room to diverge than a page of rewritten
bullets. The old floors still apply to `--writer model`, which survives only as
the benchmark control.

**The caveat.** Measured back-to-back on an idle machine with the models already
warm, so GPU batch and split conditions were identical. This is not a promise that
a run competing for the GPU decodes the same. Re-check the floor if a result ever
rests on a delta under 0.01 measured under different load.

**One loose end.** `alloc32` scored 0.649 with a frozen select cache; `aa1` scored
0.643 with fresh selection. Against a 0.000 floor that 0.006 is real, but the two
runs differ in more than the cache, so it is an observation rather than a finding.
Worth a paired check before any E2 arm reuses a cache.

---

## 1b. Results — E0 to E3 are done

Ran 2026-08-08. Everything below is measured, 32 offers, paired.

### The gate is met

| | pages (mean) | one_page_rate |
|---|---|---|
| before any of this | 1.72 | 0/32 |
| after Phase A layout | 1.286 | 0/32 |
| E2 knapsack, at render | 0.960 | 31/32 |
| **+ E3 ladder** | — | **32/32** |

31 offers fit at ladder step 0, so the ladder never fires on them. One (`id=5`)
renders at 1.013 pages and is fixed at step 2. Nothing fails to reach one page.
E3's target was "fires on ≤ 3 of 32, no offer past step 2" — it fires on 1, at
step 2.

### E2 — the knapsack beat naive count-cutting

Both arms fit one page; the question was which keeps more in the same space.

| metric | e2ctl | e2knap | delta | CI95 | w-l | p |
|---|---|---|---|---|---|---|
| `differentiator_coverage` | 0.289 | **0.409** | **+0.120** | [0.073, 0.168] | 18-1 | <0.001 |
| `grade_yield` | 0.637 | 0.691 | +0.054 | [0.022, 0.083] | 23-6 | 0.002 |
| `mean_grade` | 1.720 | 1.820 | +0.101 | [0.018, 0.178] | 21-7 | 0.013 |
| `ats_coverage` | 0.582 | 0.581 | −0.001 | [−0.013, 0.011] | 12-13 | 1.000 |
| `grounding`, all fab | 1.000 / 0.000 | 1.000 / 0.000 | 0.000 | — | 0-0 | — |

+0.120 against a +0.05 bar and a 0.000 floor. **Ship the knapsack.**

### What one page costs, which is the number that matters

| metric | 2 pages (`aa1`) | 1 page (`e2knap`) | delta | w-l | p |
|---|---|---|---|---|---|
| `differentiator_coverage` | 0.643 | 0.409 | **−0.234** | 1-24 | <0.001 |
| `ats_coverage` | 0.691 | 0.581 | **−0.110** | **0-32** | <0.001 |
| `grade_yield` | 0.739 | 0.691 | −0.048 | 6-24 | 0.001 |
| `grounding`, all fab | 1.000 / 0.000 | 1.000 / 0.000 | 0.000 | 0-0 | — |

One page costs a third of the differentiator coverage and 11 points of ATS
keyword coverage, the latter on **every single offer**. The knapsack recovers
+0.120 of the −0.234 — about half — and no ranker recovers the rest: 40 lines of
evidence do not fit in 21.

### Two targets in this document were not met

- **Primary was `differentiator_coverage` ≥ 0.55. Landed 0.409.** The decision
  table says below 0.45 means reopening whether one page is the right constraint.
  It is a real result, not a tuning failure — the page holds 21 lines and the
  evidence is 40.
- **The `ats_coverage` ≥ 0.66 guard is unreachable at one page.** Both arms sit
  at 0.58, so the guard as written kills every one-page variant including the
  control. It was calibrated against a 2-page CV; that was a mistake in this
  document, not a regression in the code. What it is really measuring is the
  keyword cost of the format decision.

  **Retired in E4, because the guard was measuring the wrong thing.**
  `ats_coverage` counts every ≥3-char token a JD and `cv.md` share. Over these 32
  offers that yields 202 distinct missed terms whose leaders are `complex`,
  `location`, `fast`, `where` and `never` — 185 of the 202 are generic English and
  17 are technologies. Its ceiling with *every* bullet on the page is 0.913 and
  with the full CV 0.953, so 0.66 was never a keyword threshold; it was a proxy
  for document length. Replaced by `skill_coverage` — of the skills a posting
  names and `cv.md` claims, the fraction reaching the page — which sits at
  **1.000** on one page. **Gate: `skill_coverage` = 1.000, and `ats_coverage`
  reported but not targeted.**

### Bugs the experiments found

1. **`ceil(len/95)` was wrong** — calibrated for the 0.6in margins, over-estimated
   20 of 32 bullets once Phase A widened the column. 124 chars/line is exact on
   30 of 32 and never under-predicts.
2. **The cost model ignored `margin-bottom: 2px` per bullet** — about ⅛ line each,
   over a line on a 9-bullet page. The first knapsack run overran on 6 of 32 by
   1-2 lines apiece while the model insisted all 32 fitted.
3. **The `--role` line costs 21px** and was the entire reason `id=5` overran. An
   E3 probe that forgot to pass `--role` measured 32/32 fitting — a CV that
   production never renders.
4. **A benchmark survived its own source being swapped.** A branch switch 34
   minutes into an E2 arm split it silently; `runVariant` now records `commit`
   and `commit_end`, and `allMetrics` refuses to score a split run.
5. **`parseSkillCategories` dropped every parenthesis**, so `Message Queues
   (RabbitMQ, Kafka)` shipped as "Message Queues". Kafka, AES-256-GCM,
   EC2/Lambda/S3/IAM, Jest, Jenkins, Ollama and MCP had been deleted at parse
   time from every tailored PDF ever generated.
6. **`tokenize` has a 3-character floor**, so `C#` and `CI/CD` scored 0 however
   loudly a posting asked for them. Ranking hid it — they shipped last — and
   filtering exposed it by dropping them outright.
7. **A `.` survives phrase normalisation** for `Next.js`, welding the period in
   "…experience with Kafka." onto the term, so a skill named at the end of a
   sentence never matched.
8. **The bench rendered a document production does not produce.**
   `withPageMetrics` passed `--max-skills 6` after LADDER step 0 stopped capping,
   so it measured a 6-row page against an 8-row PDF. Two more in the same string:
   `outputText` still scored Core Competencies months after the template deleted
   it (+0.009) and never read project bullets at all (−0.008). Those two nearly
   cancelled, which is why neither was visible.

---

## E4 — skills are selected, not dumped

The block shipped 52 items, near enough the whole taxonomy reordered, of which
12.9 shared a term with the posting. Three tiers now: named by the posting,
related to a named item by a `cv.md` line writing them together, then a floor of
3 in CV order. Held selection byte-identical, so this is the skills change alone.

| metric | control | E4 | delta | CI95 | w-l | p |
|---|---|---|---|---|---|---|
| `skill_coverage` | 0.932 | **1.000** | **+0.068** | [0.032, 0.106] | 9-0 | 0.004 |
| `ats_coverage` | 0.579 | 0.594 | +0.014 | [0.008, 0.021] | 13-0 | <0.001 |
| `differentiator_coverage` | 0.409 | 0.409 | 0.000 | [0.000, 0.000] | 0-0 | 1.000 |
| `grounding` / `product_fab` | 1.000 / 0 | 1.000 / 0 | 0.000 | — | 0-0 | 1.000 |

Skill items 52.0 → 29.8, page 979 → 958 px, one-page rate 32/32 throughout.
Against the `--writer verbatim` A/A floor of 0.000, both moves are signal.

**`skill_coverage` is perfect on 30 of 30 offers that name a skill.** The 21px it
frees is not yet spent; raising `SNIPE_LINE_BUDGET` to claim it changes selection
and so needs a fresh judge run rather than a re-derivation.

## 2. Experiments

Run in order. Each is gated on the previous.

### E1 — layout only. Chrome 728px → 594px

**Hypothesis.** The page spends 29% of itself on section chrome before a word of
content. Reclaiming it buys bullet-lines at zero content cost, so coverage cannot
fall and page count must.

**Change.** Phase A of `CV-ONE-PAGE-PLAN.md` — A4 regex fix, 0.45in margins, delete
Core Competencies, fold Certifications into Education, one-line job header, tighter
spacing, Skills after Summary, target role in header, project GitHub links.

**Measured already** (headless Chromium, NatWest CV, not a projection):

```
chrome floor   728px -> 594px
bullet room    279px -> 442px   (18 -> 28 lines)
```

**Desired outcome.** `differentiator_coverage` **unchanged** (±A/A floor) and
`ats_coverage` unchanged. This arm must not move content metrics at all — if it
does, something in the template is dropping content rather than tightening it.

**Kill criterion.** Any content metric moves outside its A/A floor. That is a bug,
not a result.

**Expected page count after E1 alone: still 2** (measured 1.13 pages). E1 is
necessary and not sufficient. Do not read that as failure.

---

### E2 — the main event. Budget lines, not bullets

**Hypothesis.** The page is rationed in rendered lines; `cv-select.mjs` rations
bullet counts (`allocate()`, `cv-select.mjs:445`), and Experience gets no budget at
all — a flat `maxBulletsPerRole = 4`. A 4-line bullet and a 1-line bullet cost the
same against that budget and 4× the page. Charging each bullet its real cost and
ranking on **score per line** should keep more differentiators per page than
cutting counts until it fits.

**Control (E2-ctl).** The naive answer: today's selector, ladder retargeted to 1
page, cutting counts until it fits. This is the arm to beat, and it is what a
reasonable person would ship without this analysis.

**Variant (E2-knap).** Shared line budget across Experience *and* Projects, greedy
on score-per-line, one bullet floor per entry preserved.

```js
const cost = t => Math.max(1, Math.ceil(t.length / 95));  // ~95 chars/line at A4 width
```

**Desired outcome.**

| | target |
|---|---|
| `E2-knap − E2-ctl` on `differentiator_coverage` | **≥ +0.05**, sign test p < 0.05 |
| `page_count == 1` | 32/32 on both arms |

**Kill criterion.** Delta ≤ +0.02 (once the A/A floor exists, ≤ 2× that floor).
If the knapsack does not beat naive count-cutting, ship E2-ctl — it is less code,
and the honest conclusion is that bullet length does not predict what is worth
keeping.

**Watch item.** `trim()`'s metric-bullet guarantee (`cv-select.mjs:428`) swaps in a
digit-carrying bullet regardless of length. Under a line budget it can silently blow
the page by trading a 1-line bullet for a 4-line one. CLAUDE.md already names it the
first suspect for the ATS dip. It must charge for the swap, and that sub-change gets
its own measurement — it fires on 42% of single-slot project bullets, so it is not
a corner case.

**Protocol.** Selection change: sweep offline with `select-sweep.mjs` first
(`ablate --split train`, then `check --split test`), and **do not reuse an existing
`SNIPE_SELECT_CACHE`** — the key is over CV and requirements, not the ranker. A
half-populated grade cache ranks ungraded offers as all-zero, which is a different
ranker rather than a missing term; the tools drop the judge term unless every offer
in the split has grades.

---

### E3 — retarget the ladder to 1 page

**Hypothesis.** After E2 the ladder should almost never fire. It is the safety net
for cases selection cannot predict — an unusually verbose JD, a role with five
entries, a project title that wraps.

**Change.** `LADDER` (`local-pdf-offer.mjs:939`) targets 1 page with 2 as the hard
failure ceiling; steps trim lines rather than counts; `--max-pages` follows.

**Desired outcome.** Ladder fires on **≤ 3 of 32** offers, and no offer needs more
than step 2. Coverage unchanged from E2 (±floor).

**Kill criterion.** Ladder fires on more than a third of offers — that means E2's
budget is set wrong, and the fix belongs in the budget, not in more ladder steps.

---

### E4 — conditional, and it is yours, not the pipeline's

**Trigger.** Only if E1–E3 land and `differentiator_coverage` is still below 0.55.

**The ceiling.** At 28 lines and a median 2.5-line source bullet, one page holds
~11 bullets. `cv.md` has 38 bullets: median 223 chars, 8 of them four or five lines,
only 5 of them one line. Those long bullets are master-CV bullets and no ranker can
shorten them.

**Not an option:** a model rewrite. `PHASE3-RETENTION-LEDGER.md` settled it — a 9B
and the 30B both lost to *no writer at all*, and every offer they changed lost
grounding (0-32, 0-11).

**The only remaining lever** is editing `cv.md` so the differentiator-carrying
bullets have short forms. That is the user layer, so it is a human decision.

**Price it before starting:** the 128 label files in `batch/bench/opus/labels/` are
**positional against `cv.md`**, so editing it invalidates all of them, and every
number in this document becomes unreproducible until they are re-labelled.

---

## 3. Decision table

| outcome | action |
|---|---|
| E0 shows the harness still blind | stop. Nothing downstream is measurable. |
| E1 moves a content metric | bug in the template. Fix before E2. |
| E2-knap ≥ +0.05 over ctl, gate held | ship both, tag v0.2.0 |
| E2-knap within noise of ctl | ship ctl, delete the knapsack, record the null |
| E2 breaches a guard | revert, record which guard and by how much |
| coverage < 0.55 after E1–E3 | E4 is a `cv.md` conversation, with the relabelling cost stated up front |
| coverage < 0.45 after E1–E3 | reopen whether one page is the right constraint at all |

---

## 4. Recording

Results append to this file in the ledger style the repo already uses — before /
after / delta / win-loss / p, and **nulls recorded as prominently as wins**. Two of
the last three Phase 3 experiments lost; that record is why the numbers in this repo
are trustworthy.

Nothing here is measured yet. Every number above is a target or a prediction.
