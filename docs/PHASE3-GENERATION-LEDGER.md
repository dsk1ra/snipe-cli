# Phase 3 generation ledger

Companion to `PHASE3-RETRIEVAL-LEDGER.md`. That one covers *which* CV bullets get
picked; this one covers what the model then *writes*. Append-only. A variant that
loses is as valuable a record as one that wins — §1 of the retrieval ledger exists
because the losses were written down.

Every number here was measured on `batch/bench/tailor/sample.tsv` (24 offers,
stratified to eval score ≥ 3.5) at `--temperature 0`, where this stack is
byte-identical greedy and a single run is a valid A/B.

---

## 0. The instrument

Eight label-free metrics, `node batch/tailor-harness.mjs metrics <label>`.

| Metric | Want | What it catches |
|---|---|---|
| `role_retention` | 1.0 | dropping or inventing an employer |
| `metric_fab` | 0 | a figure `cv.md` does not state |
| `example_copy_pct` | 0 | plagiarising the prompt's own worked example |
| `grounding` | higher | output bullets tracking their source bullet |
| `product_fab` | 0 | a named product absent from `cv.md` |
| `ats_coverage` | higher | JD terms the CV *can* support reaching the output |
| `summary_jd_fit` | higher | summary addressing the posting |
| `summary_cv_fit` | higher | summary describing what the CV actually shows |
| `selection_regret` | 0 | shipped bullets vs the best pick of the same size |

Two of these are load-bearing traps, and both were closed before any change
landed:

- **`example_copy_pct` was self-defeating.** It was defined against whatever
  worked example the prompt currently held, so *deleting* the example — the
  intended fix — would have driven it to 0 by construction. A committed snapshot
  (`batch/bench/example-bullets.json`) is now unioned in, so the number keeps
  answering the real question. Scoped to the *active* prompt only: seeding it
  from both the shipped and the personal prompt read 0.458 instead of 0.292,
  because the two hold near-paraphrases and the model only ever sees one.
- **`ats_coverage` is scored against the supportable subset**, not the whole JD,
  so it cannot be gained by inventing. Capped at the CV's honest ceiling by
  construction.

## 1. Baseline

`batch/bench/tailor/baseline-t0`, current code, temperature 0.

The previously-recorded `v6-prompt-real` run is **not** a valid control: it was
made against an older sample, and only 6 of its 24 offer directories overlap the
current sample's ids. This is benchmark rule 1 from `batch/CLAUDE.md` biting in
practice — a historical artifact is not a control.

## 2. The hardware picture, measured

`SNIPE_TIMING` records `load_duration` / `prompt_eval_*` / `eval_*` for every
Ollama call (`batch/timing.mjs`). First real numbers, Phase 3, per offer:

```
p3-judge   66 s/call   742 out tok @ 21.1 tok/s   ~9,000 prompt tok
p3-tailor  24 s/call   740 out tok @ 47.3 tok/s
embed       3 s/call
wall      ~100 s/offer
```

Three facts the plan did not have:

1. **The Phase 3 reranker is 80 % of Phase 3.** The plan estimated the judge at
   44 s; it is 66 s, against 24 s for the actual writing. Four-fifths of the
   tailoring budget is spent deciding which bullets to use, not writing them.
2. **Almost every call pays a reload.** 12 reloads across 16 calls, 66 s — the
   embedder, the 30B and the 7B evict each other in a fixed cycle, once per
   offer. ~14 s/offer is pure model loading.
3. **The judge grades 39 atoms, not 14**, and spends 742 output tokens doing it
   — ~19 tokens per `{"id":n,"grade":g}`. At 21 tok/s that is 35 s of generation
   for what its own exemplars teach as a binary decision.

## 3. Label noise

Two of gold sheet 1's offers were re-labelled blind, with the original ticks
withheld from the labeller (M2):

```
per-atom agreement  0.857  (24/28)
jaccard             0.750
pair accuracy       0.875   identical on both offers, both directions
```

**The shipped ranker scores 0.851 on sheet 1. Two labellers agree with each
other at 0.875.** On that sheet the remaining headroom is 0.024, against a
resolution limit that needs 50 labelled offers to see 0.05.

**Correction — this was initially over-read as "retrieval is finished".** It is
not. 0.875 is the agreement between *two particular labellers on two offers*,
not a universal ceiling, and one of those labellers was an agent. §4 below
measures the judge on 12 held-out offers and finds it decisively valuable. What
survives from M2 is narrower and still worth knowing: label noise here is
substantial, so any *further* retrieval tuning measured against sheet 1 has
almost no room to show itself. That is a statement about the instrument, not
about the ranker.

Caveat that bounds the claim: n = 2 offers. Both landed on exactly 0.875, which
is reassuring but is not a tight interval.

### 3.1 Where the ranker actually sits

All three measured the same day with the same code, `goldset.mjs score`
(cosine-only — the production ranking function, judge excluded):

```
gold sheet 1 (user-labelled, 12 offers)        cosine  0.764
gold sheet 2 (held out, 12 fresh offers)       cosine  0.814
inter-labeller ceiling (M2, n=2)                       0.875
shipped cos + 0.10 x judge, sheet 1 (recorded)         0.851
```

Sheet 2 is the first genuinely held-out measurement of this ranker — offers
that played no part in selecting `cos + 0.10 × judge`.

**Confound, stated rather than buried:** sheet 2 was labelled by an agent and
sheet 1 by the repository's owner. Sheet 2 scoring higher may mean it is an
easier set, or it may mean an agent labeller reasons more like an embedding
model than a human does. The two numbers are therefore not strictly
comparable, and sheet 2 should not be treated as evidence that the ranker
improved. It is used below only to ask whether the *judge* still earns its
66 s, which is a within-sheet comparison and immune to this confound.

## 4. Does the 66-second judge earn it? — yes, decisively

The reranker is 80 % of Phase 3's wall clock, so before spending any budget on
generation it was worth asking whether it still pays. Gold sheet 2 is the first
set that can answer it honestly: 12 offers that played no part in choosing
`cos + 0.10 × judge`.

```
variant           pair    Δpair    CI95              w/l     sig
cos+judge-0.10    0.930   +0.115   [0.074, 0.159]    11/0    YES
cos+judge-0.25    0.920   +0.105   [0.058, 0.154]    10/1    YES
base-cos          0.815    0.000   —                  —      —
judge alone       0.801   -0.014   [-0.092, 0.065]   6/6     no
```

The judge stays. Held out, it is worth **more** than the +0.095 recorded on
sheet 1, it wins on 11 of 12 offers and loses on none, and the CI is nowhere
near 0. The 0.10 weight remains the right one.

Two things this also settles:

- **The judge is a feature, not a ranker.** Alone it scores 0.801, statistically
  indistinguishable from cosine's 0.815 and split 6/6. It is only valuable
  *blended*. That matches the sheet-1 finding and is why the weight sweep
  plateaus rather than peaking.
- **Deleting the judge to buy back 66 s/offer is off the table.** The remaining
  latency route is L1 — keeping the grades but computing them where the 30B is
  already resident.

A bug found while doing this, worth recording because it produced a confident
wrong answer first: `retrieval-bench.mjs run` had no `--sheet` flag and always
loaded sheet 1. Passing sheet 2's grades scored them against sheet 1's offers,
every id missed, and the run reported the judge at exactly chance (0.500) with
`cos+judge` delta 0.000 and a CI of [0.000, 0.000]. A CI of exactly zero width
is not a null result, it is a plumbing failure — worth treating as a smoke alarm
rather than a finding.

## 5. The metrics would have passed a regression

Worth recording on its own, because it is the clearest argument in this whole
campaign for looking at output rather than dashboards.

The first version of the summary stage was handed the Block B requirement list
as "emphasis context", with a prompt rule saying to use it only for emphasis.
On the first offer it wrote, for a C++/HFT posting:

> "Highly skilled software engineer with **over a decade of experience** ...
> Expertise in networking, **HFT, and financial markets**."

against a CV that shows none of that. This is a straight regression, and **every
metric would have reported success**:

- `summary_jd_fit` would have gone **up** — it parrots the posting.
- `product_fab` does not fire: "HFT" and "financial markets" are domains, not
  named products.
- `metric_fab` does not fire: "a decade" contains no digit.
- `role_retention`, `grounding`, `example_copy_pct` are all untouched by the
  summary field.

The plan named this exact risk ("the pairing needs eyes on actual output, not
just numbers") and it fired on the first offer of the first run.

Three fixes, in order of how much they matter:

1. **The requirement list is gone from the prompt.** Handing a 7B the posting's
   vocabulary and asking it not to use that vocabulary does not work. It is also
   redundant: `cv-select` has already ranked and trimmed these bullets against
   Block B, so the JD signal is encoded in *which* evidence is present.
2. **`evidenceOverlap`** — the fraction of the summary's content words traceable
   to the evidence. This is the direct measure of parroting, because the
   posting's vocabulary is not the CV's. On the failing pair it separates the
   drafts cleanly: 0.139 parroted vs 0.564 grounded.
3. **The tenure guard grew a word-form branch.** "over a decade of experience"
   carries no digit, so every numeric branch missed it.

## 6. A second exploit, found the same way

The summary selector scores candidates on `evidenceOverlap`. That created an
obvious exploit and the model took it on the first run:

> "Senior Software Engineer (Graduate) / Production-Grade Backend Systems /
> SRE-adjacent Operations / 99.9% Uptime on Live Subscription Platform (170+
> users) / Kubernetes & Observability (Prometheus/Grafana) / ..."

A dense keyword dump maximises overlap with the evidence while being unreadable,
and it **scored well**. The lesson generalises past this metric: any score term
that rewards resemblance to a source can be maximised by copying the source
without writing anything.

Shape is therefore a gate, not a score term — `looksLikeProse` rejects a
candidate outright on function-word ratio (prose runs 0.20–0.40 of tokens,
a tag list near zero), slash count and sentence punctuation. Overlap's weight
dropped 20 → 8 once it could no longer be gamed this way.

The incumbent also now holds ties. On offer 182 the challenger was blander than
the JSON field it displaced, and a bare `>` gave it the slot.

## 7. Changes

Recorded per change, with the metric it targeted and what actually moved.

### `g-bundle` — G2 + G3/G4 + T2, vs `baseline-t0`

24 offers, temperature 0, paired. 39.9 min.

```
metric            baseline-t0   g-bundle    delta
example_copy_pct  0.583         0.042       -0.541   G2  ship
product_fab       0.083         0.042       -0.041   T2  ship
role_retention    1.000         1.000        0.000   invariant held
metric_fab        0             0            0.000   invariant held
grounding         0.869         0.848       -0.021
ats_coverage      0.491         0.488       -0.003
summary_cv_fit    0.585         0.555       -0.030   G4  FAILS GATE
summary_jd_fit    0.561         0.549       -0.012   G4  FAILS GATE
selection_regret  0.064         0.067       +0.003
mean_bullets      4.54          4.92        +0.38
```

**G2 — drop the worked example. Shipped, and it was not "pure downside".**
14 of 24 offers were lifting an 8-gram straight out of the prompt's own example;
now 1. What this entry originally claimed — that `TAILOR_SCHEMA`'s constrained
decoding already guaranteed everything the example demonstrated, so removing it
cost nothing — was wrong, and section 9 has the measurement. The example was
also teaching bullet *shape*: keep the figure. Deleting it cost 0.258 of
`num_retention`. The metric that would have caught this did not exist yet, which
is the whole lesson.

**T2 — product gate. Shipped, then extended.** Halved on the first pass. The
survivor was in `skills`, the one model-written surface the first pass did not
cover, since `competencies` and `education_modules` are derived from `cv.md` in
code and cannot fabricate.

**G4 — summary as its own stage. Failed its gate.** The gate was *both*
alignment numbers up; both fell. The cause is visible in the output rather than
the numbers: filler appears in 4 of 9 sampled summaries against the baseline's
0 of 24, and 67 % needed the deterministic length padding against 37 %.

The mechanism is worth recording because it is a genuine cost of G2: **the
worked example was also demonstrating what a specific summary looks like.**
Removing it fixed copying and cost concreteness at the same time. The two
defects had one shared cause, and fixing one exposed the other.

`grounding` −0.021 rides on `mean_bullets` +0.38 — more bullets kept, and
`grounding` is a mean over bullets, so the marginal ones dilute it.

### `g2` — specificity + the skills surface, vs `baseline-t0`

24 offers, temperature 0, paired. 39.8 min. **This is what ships.**

```
metric            baseline-t0   g2          delta
example_copy_pct  0.583         0.042       -0.541
product_fab       0.083         0.000       -0.083   T2 gate MET
summary_cv_fit    0.585         0.623       +0.038   G3 gate MET
summary_jd_fit    0.561         0.527       -0.034
role_retention    1.000         1.000        0.000   invariant held
metric_fab        0             0            0.000   invariant held
grounding         0.869         0.848       -0.021
ats_coverage      0.491         0.473       -0.018
selection_regret  0.064         0.067       +0.003
```

Against the failed `g-bundle`, the specificity fix did exactly one thing:

```
filler summaries  11/24  ->  1/24
summary_cv_fit    0.555  ->  0.623   +0.068
product_fab       0.042  ->  0.000
grounding, example_copy_pct, selection_regret, mean_bullets:
    unchanged to three decimals
```

Four metrics moving by exactly 0.000 while two move sharply is the cleanest
demonstration of the temperature-0 discipline in this ledger: the change was
surgical, and the benchmark can prove it rather than assert it.

**On the two summary numbers, stated plainly.** `summary_cv_fit` is up and
`summary_jd_fit` is down. That is the intended direction, and it is exactly why
the strategy doc insists on measuring both: a summary that parrots the posting
scores high on `jd_fit` and low on `cv_fit`, so moving away from parroting must
trade one against the other. G3's gate (*cv_fit up, jd_fit not collapsing*) is
met. **G4's stricter gate (*both up*) is not**, and a 6 % relative fall in
`jd_fit` is not a collapse but it is not an improvement either.

`ats_coverage` −0.018 and `grounding` −0.021 are the same trade: a summary
describing the CV rather than the posting shares fewer tokens with the posting.
Neither is near a collapse, and both truth invariants held.

### L3 — compact judge output. Rejected.

The judge emits `{"id":n,"grade":g}` per item: 660–742 output tokens for 39
items at 21 tok/s, about 35 s of its 61 s. The order is fixed by the prompt, so
the id looks like pure redundancy and a positional array carries the same
information in ~80 tokens.

Measured on the held-out sheet:

```
cos + 0.10 x judge, verbose ids   0.930   +0.115 over cosine   11/0
cos + 0.10 x judge, positional    0.878   +0.063 over cosine    9/2
base-cos                          0.815
```

Compaction costs **0.052 pair accuracy — roughly half of what the judge buys —
to save 35 s.** Rejected.

Why it fails is the useful part: the id is not redundant *to the model*.
Writing it per item is what keeps the grading aligned and deliberate; removing
the scaffolding does not merely shorten the answer, it changes it. Same family
of result as 0-shot grading scoring worse than no grading at all.

Two earlier cheap variants died the same way, both free to test because the
grades were already cached: binarising at ≥2 scores 0.894, at 3 scores 0.899,
against 0.930 graded. **The 0–3 granularity is load-bearing.** The model does
use the range — distribution over the held-out sheet is 0:60, 1:7, 2:46, 3:55.

This closes every cheap latency route. Deleting the judge is off the table
(+0.115), compacting it is off the table (−0.052), binarising it is off the
table (−0.03). Only L1 remains: computing the same grades where the 30B is
already resident instead of paying a second load.

### G1 — 30B writer instead of the 7B coder. Not shipped; the trade is real.

12 offers (a fixed prefix of the sample), paired against `g2`. The premise —
"`snipe-cv` is a code-tuned model writing English prose, and that is a tool
mismatch" — is partly vindicated.

```
metric            g2 (7B)   g1-30b    delta
ats_coverage      0.457     0.586     +0.129
summary_jd_fit    0.493     0.559     +0.066
example_copy_pct  0.083     0.000     -0.083
selection_regret  0.077     0.070     -0.007
grounding         0.809     0.796     -0.013
summary_cv_fit    0.610     0.584     -0.026
mean_bullets      4.92      6.00      +1.08
role_retention / metric_fab / product_fab: all held
```

`ats_coverage` +0.129 is the largest single-metric move any variant produced,
and it survives rendering — no role exceeds the density ladder's 4-bullet cap in
either run, so the extra depth reaches the PDF rather than being trimmed.

**It costs 70 s per offer: 99 s → 169 s of Phase 3.** Against the pre-registered
5-minute ceiling that is decisive, see §8. Recorded as an available trade rather
than a rejected idea — `--phase3-model snipe-eval` switches it on, and for a
small number of high-value applications the coverage is probably worth the wait.

Worth noting against the plan's framing of G1 as "completely unverified": the
`Modelfile.snipe-cv` header already records a 5×2 benchmark in which the coder
base beat *qwen2.5-7b-instruct* on exactly these structured-output constraints.
The premise was tested before, against a weaker comparator. What is new here is
that a much larger general model does win on coverage — the original finding was
about instruction-following at 7B, not about prose at any size.

## 8. End-to-end latency, measured for the first time

The plan flagged that no phase records its own wall clock and that the "~5 min
per JD" figure was an estimate. It is now a number, per offer, at temperature 0:

```
Phase 1  (snipe-screen 4B)        12 s
Phase 2  (snipe-eval 30B x3)     246 s   <- 69% of the pipeline
Phase 3  (snipe-cv 7B, shipped)   99 s
                                 ----
                                 358 s = 5.96 min
Phase 3 with the 30B writer      169 s   -> 427 s = 7.12 min
```

**The pipeline already exceeds its own 5-minute budget, before any change made
here.** G2/G3/T2 did not cause it — Phase 3 got slightly *faster*, since
deleting the worked example shortened every tailor prompt.

The real target is not the Phase 3 reranker the plan focused on. It is
**`p2-judgment`: 114–153 s per offer, ~2,765 output tokens**, the single largest
cost in the pipeline and roughly a third of it. L3 was aimed at Phase 3's judge
and found nothing; aimed at Phase 2's judgment call, where the plan originally
pointed it (`num_predict: 5120`), there is a third of the pipeline to argue with.

### An attempted latency fix that reverted, and why it matters

Ollama keys a loaded model on its context size, so stages at 8192 sitting
between stages at 12288 force a full reload of an 18.5 GB model. Aligning all
three Phase 2 stages to 12288 worked exactly as predicted:

```
p2-judgment load   15.84 s -> 0.33 s     reloads 2 -> 0
```

**And it changed the answers.** On the same two offers at temperature 0, the
score moved 1.6 → 1.1, requirement coverage 24 % → 12 %, and stage 1 parsed a
different role title ("Engineer" vs "Software Engineer (C++)"). `num_ctx` is not
a free knob on this stack: it changes the runtime configuration, and greedy
determinism only holds *within* a fixed configuration.

Reverted. It is an unvalidated quality change bought for ~8 s, and validating it
properly needs a full Phase 2 benchmark (`eval-harness`, 18 offers, rho and pair
accuracy) that did not fit the remaining budget. The mechanism is real and the
saving is real; the neutrality claim was false and testing it is what caught it.

---

## 9. The figures the model deleted — found by reading, not by measuring

Reading the 12 CVs from a real production run surfaced a defect none of the
eight metrics could see: **44 % of the figures stated in a source bullet
survived into its rewrite.** The 7B truncates to the first clause, so

> Authored troubleshooting documentation and standardised environment setup
> guides, cutting configuration time from 2+ hours to 30 minutes per student and
> reducing staff escalations by 90 %

shipped as *"Authored troubleshooting documentation for students"*, and

> Won MongoDB as client through a competitive pitch; served as Project Manager
> and Lead Engineer for a 6-person cross-functional team, achieving Distinction

shipped as *"Led a project partnership with MongoDB"* — a weakening rewrite, not
even a truncation. Every PDF was 2 pages, so nothing forced the cut.

### Why every metric passed it

`metric_fab` asks whether a number was invented. Nothing asked whether the
numbers already there were kept. Under a metric suite that only punishes
falsity, **the optimum is an empty CV** — a bullet asserting nothing scores
perfectly on all eight. Each guard added over the campaign moved toward that
optimum and scored as a win doing it.

The suite has the same blind spot as the code because the same process built
both: every metric was written after a failure was observed, so the suite covers
the failures already found and is structurally incapable of catching a class
nobody has looked at. Twenty minutes of reading output found what eight hours of
benchmarking could not.

### `num_retention`, and what it says about G2

Pairs each output bullet with the `cv.md` bullet it was rewritten from (argmax
of the same overlap `grounding` uses) and counts which of that source's figures
survived. `num_lost` is the absolute counterpart.

Run against the campaign's own archived runs, it reverses section 7's headline:

```
baseline-t0 → g2 (drop the worked example), paired on 24 offers
num_retention  0.726 → 0.469   delta -0.258, CI [-0.390, -0.128]
                               worse on 14, better on 5, tied 5
```

G2 was reported as the campaign's biggest win on `example_copy_pct`
0.583 → 0.042. It also cost a quarter of the CV's quantified evidence. Both
statements are true; only one was measured at the time.

`g1-30b` — the 30B writer rejected in section 7 for +70 s/offer — scores 0.866,
the best of any run. That trade is worth revisiting now that there is a metric
for the thing it was better at.

### The repair: `verifyBulletFigures`

The mirror of `verifyBulletNumbers`. That guard reverts a bullet asserting a
figure the CV does not state; this one reverts a bullet that dropped a figure
the CV *did* state. `revertUnsupportedBullets` now hands the predicate the
source bullet as well as the rewrite, since "this dropped something" is only
answerable against the line it came from.

Gated behind `SNIPE_REVERT_FIGURES` for the benchmark, because reverting was
expected to cost the JD keywords the rewrite added. Two fresh arms, 24 offers,
temperature 0, paired:

```
metric            ctl-figures   rev-figures   delta    CI                 W/L
num_retention     0.469         1.000         +0.531   (by construction)
grounding         0.848         0.953         +0.105   [0.060, 0.160]     21/0
ats_coverage      0.473         0.498         +0.025   [0.014, 0.039]     16/1
mean_bullets      4.92          4.83          -0.083   [-0.208, 0.000]     0/2
summary_jd_fit    0.527         0.531         +0.004
summary_cv_fit    0.623         0.617         -0.006
selection_regret  0.067         0.066         -0.001
```

**Only `ats_coverage` is evidence.** `num_retention` reaching exactly 1.000 and
most of `grounding` are pinned by the guard firing — a reverted bullet *is* a CV
bullet. The independent result is that the metric this was expected to cost went
**up**: the full CV bullet already carries more of the posting's vocabulary than
the 7B's truncation of it. The rewriting was net-negative on the one axis it
existed to improve.

Cost: 2 offers of 24 lost one bullet to a revert collision (two rewrites
reverting onto the same source line, deduped). Shipped on by default.

### Still open

`g-bundle` was not a valid control — a 3-offer probe found 2 of 3 summaries
differed, because the filler penalty landed after that run. Experience bullets
were byte-identical, which is what confirmed the `revertUnsupportedBullets`
refactor was a no-op. **Benchmark rule 1 held: two runs made now.**

## 10. Cross-entry figure theft

Found by the same reading pass, on the Java/Kafka CV. Three project blurbs, two
defects, two different mechanisms:

| Blurb | Claim | Provenance |
|---|---|---|
| Re:Link | "serving 970%+ revenue growth" | `970` is nowhere in `cv.md` — invented |
| DE-Store | "achieving sub-500ms load times" | real, `cv.md:60` — the Zero Trust dashboard's |
| Zero Trust | "winning MongoDB as a client" | correctly attributed |

Both shipped, for two different reasons, and neither is a retrieval failure:

- `verifyBulletNumbers` allowed any figure appearing **anywhere** in `cv.md`. It
  answers "does this number exist in the document", when the question is "does
  it belong to the entry claiming it". `sub-500ms` passes a CV-global check by
  construction.
- Projects had no figure guard at all — only `stripFabricatedProducts`, which
  knows product names and not numbers. So `970%` was never tested.

**Selection was never the problem.** `cv-select.mjs:228` carries `entry` on every
atom and `:248`/`:260` write the ranked bullets back into it, so the CV handed to
the 7B is already partitioned by employer and project. The model receives a
correctly-scoped document and blends across it while generating. Scoping the
*pool* would have fixed nothing; scoping the *verification* is the fix.

### The fix

One entry-scoped rule subsumes both defects, because neither figure appears in
the entry doing the claiming. `verifyBulletNumbers` now takes its allow-set from
the employer's own entry (head included — the dates and tech-stack line carry
figures a bullet may legitimately cite), and `verifyProjectFigures` applies the
same test to project blurbs.

Projects are repaired by **clause surgery, not revert**: a blurb is synthesised
from several source bullets, so reverting joins them into a run-on — precisely
the 55-word PQC paragraph that shipped next to 12-word siblings on 3 of 12 CVs.
`stripUnsupportedClauses` is that surgery, lifted out of
`stripFabricatedProducts` so both guards share it.

Replayed against the CV that exposed it, prose intact and the correct blurb
untouched:

```
FIXED  Re:Link      … end-to-end encryption (Rust / Flutter), serving 970%+ revenue growth for a client.
            →       … end-to-end encryption (Rust / Flutter).
FIXED  DE-Store     … using Spring Boot and Kafka, achieving sub-500ms load times.
            →       … using Spring Boot and Kafka.
KEPT   Zero Trust
```

### The experience section was clean by luck, and not entirely

Two employers in clearly-labelled blocks is what was keeping it honest, not the
guard — the CV-global allow-set applied there too. Re-running the 24 bench
offers under the entry-scoped rule changed **1 of 118 bullets**, and that one is
the same bug:

```
56_unknown [UBWIS]
  old: Led a team on a Zero Trust SIEM dashboard serving 3M+ auth events with sub-500ms load times.
  new: Led a two-developer team building a membership platform … grew paying subscribers from 80 at launch to 170
```

A UBWIS bullet wearing the Zero Trust dashboard's numbers, passed by the old
rule because both figures are real.

**Not re-benchmarked, deliberately.** Projects were previously unguarded, so the
change can only remove figures that no CV entry supports — there is no quality
axis to trade against, and the repair is verified directly on the defect above.
On the experience side the blast radius is 0.8 % of bullets and the single hit
is a true positive that reverts to a *longer* source bullet, which moves
`num_retention` and `ats_coverage` the same direction §9 already measured. A
40-minute arm to resolve one bullet would not be measuring anything.

---

## 11. The summary reads the posting — and what that cost before it paid

Measured on `sample32.tsv` (32 offers, not the 24 of §0–§10), `--temperature 0`,
`--writer verbatim`, `SNIPE_LINE_BUDGET=24 SNIPE_MAX_PROJECTS=3`, selection
frozen across every arm with `SNIPE_SELECT_CACHE` — legitimate here and only
here, because nothing in this section touches `cv-select`, and every selection
metric reading identically 0.000 in all four arms is the check that it held.

### The defect

The shipped summary was three achievements with the bullet points removed. On
offer 305 (Trustpilot, asking for TypeScript/Node/AWS) it spent two of its three
sentences on a Java coursework project, and the evidence set *contained* the
Stripe/Node/Next.js bullet it ignored. Two further runs of the same offer
produced a 75-word single-sentence run-on and a "proven track record" opener, so
there was no stable format to critique — which is itself the finding.

**No metric could see any of it.** All eight punish falsity; the three shapes
scored identically. `summaryShape` is the answer to that (rule 9 again) and is
deliberately *only* a shape metric — a two-word summary clears `no_positioning`.

### The arms

| arm | what it is |
|---|---|
| `sum-ctl` | pre-`5ec5a44`: 7B writer, no requirements in the prompt |
| `sum-new` | `5ec5a44`: 30B, Block B in the prompt, industry template |
| `sum-v2` | `bfc51aa`: + domain/cased/figure rejection with a grounded retry |
| `sum-v3` | shipped: + reject what `stripJdProperNouns` would gut |

| arm | fab raw | fab shipped | shape defects | clean | generic closer |
|---|---|---|---|---|---|
| `sum-ctl` | 0.031 (1) | 0.000 | 0.844 | 19% | 5/32 |
| `sum-new` | **0.250 (8)** | **0.063 (2)** | 0.125 | 88% | 14/32 |
| `sum-v2` | 0.031 (1) | 0.000 | 0.125 | 88% | 13/32 |
| `sum-v3` | 0.031 (1) | 0.000 | **0.063** | **94%** | 9/32 |

`sum-ctl` → `sum-v3`, paired, n=32: **`ats_coverage` +0.029, CI [0.018, 0.041],
21-3, p<0.001** — 15× its ±0.002 A/A floor. `summary_jd_fit` +0.138 (30-2).
Every selection metric, `grounding`, `num_retention`, `metric_fab` and
`product_fab` at 0.000 delta.

### Rule 7 caught this, and the table would not have

`sum-new` reads as a clean win — shape 0.844 → 0.125, `ats_coverage` +0.028,
`product_fab` a flat 0. It was fabricating on **8 of 32 offers**. The tell was
`summary_cv_fit` −0.087 next to `summary_jd_fit` +0.151, which is rule 7's
signature exactly, and the only way to confirm it was reading the output:

- *"deep expertise in **financial services** IT systems"* — JPMorganChase posting,
  CV contains "financial" zero times
- *"production systems in **Go** and Rust"* — CV contains standalone "Go" zero times
- *"reduced configuration time by **87.5%**"* — `cv.md` says 90%
- *"improved accuracy by **14.1%**"* — computed from 0.815 → 0.930, never stated
- *"**C++17/20**"* — `cv.md` says "C/C++"

Three classes, none visible to any guard. Domains because `productFab` matches
products; `Go` because `stripJdProperNouns` skips words under three characters —
**the same 3-char floor this repo already recorded zeroing `C#` and `CI/CD` in
`tokenize`**, found twice now in unrelated code; derived figures because nothing
asked whether a *correct* calculation was a *stated* one.

`summary_cv_fit` −0.082 survives into `sum-v3` and is no longer evidence of
falsity: fabrication is measured directly and sits at the control's level. It is
the summary using the posting's vocabulary for things `cv.md` genuinely contains,
which is the entire point of showing it the posting.

### What actually fixed it

**Rejection, not repair.** Clause surgery had no sibling to fall back on when it
was written; it does now. One repair had shipped *"Linux systems programming, and
advanced concurrency patterns."* as a sentence. When a draft is unusable the
stage asks for a second one with the requirements withheld — clean *by
construction*, since it never sees the posting, which is the only kind of clean a
closed-vocabulary detector can be trusted to deliver. The second call fires on 3
of 32.

**One function for the gate and the metric.** `summaryUnsupported` is now both.
They were separate and drifted: the harness copy grew `tenure` and `figure` while
the gate still checked only products, so the stage believed it had rejected
everything while 8 offers shipped a fabrication. Same failure `normPhrase` was
shared to prevent.

**The gate must know about guards that run after it.** `stripJdProperNouns` runs
downstream in `local-pdf-offer.mjs`; a candidate the gate passed and that guard
then gutted fell under the 50-word floor and picked up the generic closer — 13 of
32. Asking it up front moved that to 9 and shape defects 0.125 → 0.063.

### Two things not claimed

`sum-ctl` ran 36.7 min against `sum-new`'s 3.5, but the control built the select
cache cold — **confounded, not a speedup.** And the summary call dropping to
temperature 0 is a determinism choice for production, invisible to a bench where
every arm was already greedy.

---

## 12. Figure attribution — the defect §10 fixed everywhere except here

Same setup as §11 (`sample32.tsv`, temp 0, `--writer verbatim`, frozen selection).

§10 replaced the CV-global figure allow-set with an entry-scoped one for
experience bullets and project blurbs: *"does this number exist in the document"
is not the question, "does it belong to the entry claiming it" is.* The summary
never got that fix, and it is the surface most able to blend entries, because it
is the only one that draws on all of them at once.

Shipped example, Sophos: *"Delivered a privacy-preserving peer-to-peer system
with 85%+ test coverage and maintained 99.9% uptime"*. Both figures are real,
both are UBWIS's, the sentence names Re:Link. `summaryUnsupported` reports clean
by construction, because each number genuinely appears in `cv.md`.

| arm | misattributed | fab raw | fab shipped | shape defects | clean |
|---|---|---|---|---|---|
| `sum-ctl` | 0/32 | 0.031 | 0.000 | 0.844 | 19% |
| `sum-new` | 2/32 | 0.313 | 0.125 | 0.125 | 88% |
| `sum-v3` | 3/32 | 0.063 | 0.031 | 0.063 | 94% |
| `sum-v4` prompt only | 3/32 | 0.031 | 0.031 | 0.031 | 97% |
| `sum-v5` shipped | **0/32** | **0.000** | **0.000** | **0.031** | **97%** |

`sum-ctl` → `sum-v5`, paired, n=32: `ats_coverage` **+0.023**, CI [0.013, 0.033],
19-4, p=0.003. Every selection metric, `grounding`, `num_retention`,
`metric_fab` and `product_fab` at 0.000 delta.

**The `sum-new`/`sum-v3`/`sum-v4` fabrication figures here are higher than §11
reports them.** The detector grew bare domain forms while measuring `sum-v4`, and
every arm is rescored by current code. §11's numbers were the truth the detector
could see at the time; these are the truth it can see now. `sum-v4` in particular
was reported as fabrication-free and is not — it claims "clinical AI agents" on a
clinical-AI posting, which `clinical trials` did not match. The qualifier the list
happened to carry was doing the work rather than the domain word.

### Asking the model to attribute does not make it attribute

`sum-v4` is the prompt-only arm: the proof sentence must name the entry it
credits, with the wrong/right pair from offer 70 in the rules. **Attribution did
not move — 3 of 32 before, 3 of 32 after.** Offer 50 is why:

> "Achieved **85%+ test coverage** and reduced fabricated job requirements by 9x
> **in the Snipe — Fully Local LLM Job-Application Pipeline**."

The entry is named, exactly as instructed, and UBWIS's figure is welded into the
same clause anyway. All three survivors were the same figure, `85%+`, attached to
three different entries. Naming makes the error explicit and detectable; it does
not prevent it.

It was worth keeping regardless — shape 0.063 → 0.031 and 94% → 97% clean — and
it is what makes the guard's job easy, because the entry is now stated rather
than inferred. But as a fix for the thing it targeted it is a null, and the
entry-scoped guard is what actually closed it: attribution joins the rejection
gate, so a draft crediting the wrong entry is re-requested with the requirements
withheld, and `stripMisattributedFigures` is §10's clause surgery on the repair
path and in the production chain.

`sum-v4` → `sum-v5` costs `summary_jd_fit` −0.033 (CI [−0.056, −0.012], 9-23,
p=0.020) with `ats_coverage` flat at −0.003 (CI crosses 0). Rule 7 is explicit
that `jd_fit` is the gameable one and `ats_coverage` is the breadth signal that
matters, so a fabrication-free, attribution-clean arm at the same ATS breadth is
the trade taken.

### Two false starts, both pinned in tests

**Naming read off bullets names everything.** The first index was built over
entry *text*, so any summary reusing a bullet's wording named its entry — which
is most summaries, since the evidence is where their vocabulary comes from. The
Sophos error then reads as correctly attributed: it echoes UBWIS's "test
coverage" while naming Re:Link by title, and any-match declares the figure fine.
Titles identify; bullets repeat. Index the head, check ownership against
everything.

**Sentence granularity conflates two true clauses.** *"Achieved sub-500ms
dashboard load times through cache warming, and improved job-application pipeline
accuracy from 0.815 to 0.930"* names Snipe and carries Zero Trust's latency, and
both halves are true. Judged whole it reads as Snipe claiming 500ms. Clause
granularity is the same level `stripUnsupportedClauses` repairs at, and it errs
toward silence — a name in one clause and its figure in the next is missed rather
than invented.

---

## 13. The experience floor, and a simulator that was validating against 2026-08-07

The 24-line budget lets experience and projects compete on score per line with no
floor beyond the one bullet every entry is guaranteed (§E2). Across the 32-offer
bench that left **26 offers with an employer at a single bullet and 7 with every
employer at one** — an Experience section of two one-line entries, which reads as
no experience whatever the projects say.

### The tool had to be repaired before it could answer

`select-sweep.mjs validate` reported "within 0.011 of the real run" against
`vbp2`'s 0.468 — an arm from 2026-08-07 with `SNIPE_PROJECT_BULLETS=2` and no
line budget. Production moved to the 24-line budget the next day and measures
0.564. **The tool built to enforce benchmark rule 1 was itself comparing against
a control the pipeline had abandoned**, and would have gone on doing so silently.

Worse for this question, the simulator models the *pre*-line-budget funnel:
experience gets its own `EXP_KEEP` quota and never competes with projects, so the
effect under investigation could not exist in it. `allocateLines` is now ported —
costs from `cv.md` via production's own `bulletCost`, with the atom↔bullet
positions asserted rather than assumed.

**And the spike parameterisation was wrong for every sweep ever run at
`spikeW > 0`.** Production scores `cos + 0.10·grade` then subtracts `α·mean` with
`α = w/(1+w)`. The simulator scores `cos + gradeW·grade + w·(cos − mean)`, which
is the same ranking *divided by (1+w)* — so `--spike 6 --grade 0.10` is the
shipped ranker with the judge at `0.10/7`, and the judge is worth +0.115 pair
accuracy. Scaling `gradeW` by `(1+w)` closes the gap from 0.046 to 0.011.

`grade_yield` has never reproduced on this simulator (0.518 vs 0.689 legacy,
0.424 vs 0.777 now) and is printed as untrusted rather than quietly ungated.

### The sweep

Tuned on train, scored once on held-out, 128-offer label corpus:

| floor | train Δcov | test Δcov |
|---|---|---|
| 2, every experience entry | −0.046 | −0.055 |
| 3, every experience entry | −0.119 | −0.157 |
| 2, **top-scoring entry only** | −0.008 (CI crosses 0) | **−0.018** |
| 3, top-scoring entry only | −0.027 | −0.046 |

The blanket floor costs three times as much, and the reason is the whole result:
**the entry the budget starves is the teaching assistantship** — 1.39 bullets,
at the floor in 71% of 128 offers — not the commercial role (2.41, 25%). A floor
on everything spends the page on the least differentiating evidence on the CV.

### The real arm

`sum-v5` → `floor2`, paired, n=32, fresh selection in `floor2` (a selection
change may not reuse a select cache; the control's cache predates every
`cv-select` change, so its selection is the pre-floor one):

| metric | sum-v5 | floor2 | delta | CI95 | p |
|---|---|---|---|---|---|
| `differentiator_coverage` | 0.564 | 0.552 | −0.012 | [−0.036, 0.011] | 0.625 |
| `mean_bullets` | 3.531 | 3.844 | **+0.313** | [0.156, 0.469] | 0.002 |
| `noise_rate` | 0.163 | 0.178 | +0.015 | [0.003, 0.027] | 0.063 |
| `ats_coverage` | 0.653 | 0.659 | +0.006 | [−0.004, 0.016] | 0.648 |

**Offers where every employer renders as one bullet: 7/32 → 0/32.**
`skill_coverage` 1.000, `grounding` 1.000, every fabrication metric 0.

The predicted cost did not materialise as a significant loss — the simulator said
−0.018 and the arm measured −0.012 with a CI containing both. That agreement is
the corroboration the port needed; a simulator agreeing with itself is not
evidence. The one real cost is `noise_rate` +0.015, which is what a bullet
promoted by floor rather than by score is expected to look like.

**"At least one employer at 1 bullet" barely moved: 26/32 → 24/32.** That is by
design and worth stating plainly, because it looks like a failure — the floor
deliberately does not lift the teaching assistantship, which is the entry starved
in most of those offers and the one the sweep says is not worth the page.

---

## 14. `section_balance` — and what it says about how the starvation got there

§13 confirmed the experience floor by counting bullets by hand in rendered PDFs,
because no metric in the suite could see the thing it fixed. All eight punish
falsity, so a page carrying nine project bullets over two one-line employers is
perfect on every one of them — standing rule 9, asked of the sections rather than
of an empty output.

Four fields now, scored off `cv-content.json` like everything else. That file *is*
the rendered document for a bench arm: `local-pdf-offer.mjs` exits before the
density ladder under `--bench-dir`, so there is no post-ladder page to disagree
with (benchmark rule 11 asked and answered, not assumed).

| field | what it is |
|---|---|
| `exp_bullets` / `proj_bullets` | the page census, per section |
| `section_balance` | experience ÷ total. A share, not a score — there is no correct value |
| `exp_starved` | experience entries at ≤1 bullet, per offer |
| `all_exp_starved_pct` | offers where *every* employer is at one bullet — the gate |

**The share alone would not have worked.** Across the floor arm it moves 0.355 →
0.387, which reads as noise next to any A/A floor. The starvation count is what
carries the result, and it reproduces §13's hand count to the offer: 7/32 → 0/32.
That agreement is the only reason to trust the field — a metric with no
independently-known answer to hit is just another number.

### The whole series, rescored

Free, because every arm's `cv-content.json` is committed:

| arm | funnel | exp | proj | starved | every employer at 1 | cov |
|---|---|---|---|---|---|---|
| `e2ctl` | counts (`LINE_BUDGET=0`) | 4.00 | 3.00 | 0.00 | **0/32** | — |
| `vbp2` | `PROJECT_BULLETS=2` | 8.00 | 8.00 | 0.00 | **0/32** | 0.468 |
| `spike32` | " | 8.00 | 8.00 | 0.00 | **0/32** | 0.548 |
| `alloc32` | " | 8.00 | 8.00 | 0.00 | **0/32** | 0.649 |
| `e2knap` | `LINE_BUDGET=21` | 2.69 | 5.22 | 1.50 | **16/32** | — |
| `e5` | `LINE_BUDGET=24` | 3.44 | 5.81 | 1.25 | **9/32** | — |
| `sum-ctl` / `sum-v3` / `sum-v5` | `LINE_BUDGET=24` | 3.53 | 6.44 | 1.03 | **7/32** | 0.564 |
| `floor2` | + top-entry floor | 3.84 | 6.09 | 0.75 | **0/32** | 0.552 |

**The starvation is not old. It arrived with the line budget on 2026-08-08.**
Every count-based funnel starves nobody, because experience held its own
`EXP_KEEP` quota and never competed with projects. Sharing one budget is what
created the failure, and the dose-response is monotone: 21 lines starved every
employer on half the corpus, 24 on 7 of 32, and the floor closes it.

So the +0.116 differentiator coverage the 24-line budget bought was paid for
partly in Experience section, and the invoice was not readable at the time. The
trade may well still be right — `floor2` gives back 0.012 coverage to buy the
section back, which is cheap. But it was made blind, and for four days the
strongest-scoring configuration on record was also the one that rendered two
one-line employers.

The three summary arms reading identically (3.53 / 6.44 / 1.03 / 0.219) is the
control that says the field is not noise: §11 and §12 froze selection with
`SNIPE_SELECT_CACHE` and changed only the summary, so a balance metric that moved
across them would have been measuring itself.

### Two things it cannot do

**It does not survive the writer change.** `ctl32`, `vb32`, `floors*` and
`baseline-floors` render projects as a prose blurb, so `proj_bullets` is 0 by
construction and `section_balance` reads a flattering 1.000. That is an absent
field, not a balanced page. Do not compare balance across `--writer model`.

**`mean_bullets` was never the page.** It is `grounding`'s denominator —
matched *experience* bullets only — and the name has been read as the whole
document. Experiment A cites `mean_bullets` 8.000 → 8.000 as the load-bearing
proof that reallocating *project* bullets did not grow the page, and that number
could not see a project bullet. The conclusion holds (`proj_bullets` is 8.00 in
both arms, measured now), but the evidence offered for it did not support it.
Use `exp_bullets` + `proj_bullets` for anything about page size.

---

## 15. `skill_coverage` was scoring 3.5 skills a posting

Found while producing the item 9 taxonomy report, which is the only reason it was
found at all: the metric reads 1.000 and has read 1.000 since the skills work
landed, so nothing about it looked worth opening.

`cv.md` writes some taxonomy items as alternatives — `TypeScript / JavaScript`,
`Agile / Scrum`, `MongoDB / Atlas`, `Unit / Integration / E2E Testing` — and some
compound names with a slash inside them: `CI/CD`, `C/C++`, `STUN/TURN`. The
spaced slash means "either of these" and the tight one does not, consistently
across all nine such items. That is the file's own notation.

`skillCoverage` matched an item as one whole phrase, so a posting asking for
TypeScript did not match `TypeScript / JavaScript`. **Those postings were not
counted as misses. They left the denominator.** 31 of 128 offers name TypeScript;
31 name Agile; 14 name a form of `Unit / Integration / E2E Testing`.

| | before | after |
|---|---|---|
| `skills_asked` (mean per offer) | 3.5 | **4.2** |
| `skill_coverage`, `sum-v5` / `floor2` | 1.000 | **1.000** |
| `skill_coverage`, `spike32` / `alloc32` | 1.000 | 0.929 |

So the pipeline passes the harder test — every skill a posting names under the
wider match still reaches the page. The 0.929 on the two pre-skills-work arms is
the metric recovering a real historical miss, and it agrees with the 0.932 the
skills work recorded as its own starting point.

**The selector was never affected.** `selectSkills` scores `hits()` as token
overlap plus phrase, so `TypeScript / JavaScript` already ranked first and
shipped on every TypeScript posting — verified by reading the rendered skills
block on three offers rather than by reasoning about it. Only the metric matched
on the phrase alone.

That is the third time a gate and its metric have drifted apart in this
codebase: `normPhrase` was exported to stop the selector and the harness
disagreeing, `summaryUnsupported` was made one function after the harness copy
grew `tenure` and `figure` while the gate checked only products, and this. The
pattern is worth naming — when a metric scores a decision some other code makes,
it must call that code, not re-implement its predicate.

### Rejected as marginal, measured first

`Express.js` and `Node.js` against postings writing bare `Express` / `Node`: real
(all three occurrences are genuine framework references, read individually) but 3
offers of 128. The same rule applied to `Next.js` and `.NET` would match ordinary
English — 45 offers contain the word "next", 40 contain "net" — so an alias rule
loses more than it wins. Left alone deliberately.

### What the report says to do

`node batch/bench-tools/skills-gap.mjs --min 4 --shaped`. The two largest gaps
are notation rather than capability, and both are now one-character edits because
`skillForms` reads the spaced slash: `AI / LLM application development` (84
offers, 66% of the corpus) and `REST / RESTful APIs` (21, 16%). `cv.md` prose
already proves both. Full list in `CV-GENERATION-BACKLOG.md` §9.

---

## 16. The judge grade as a cut — closed, −0.062 held out, and why it cannot work

Backlog item 4. Offline, 0 model calls: the 30B's grades are cached for 127 of
128 offers, so the whole question costs seconds.

### The tool had to be repaired again first

§13 fixed `validate` to check against the funnel production runs. It did not fix
`sweep`, `ablate` or `check`, which never passed `lineBudget` at all — so every
selection sweep since 2026-08-08 has described the funnel the pipeline
abandoned. `--shipped` puts both arms on the real one; the legacy funnel stays
the default, because the ledgers' numbers were taken there and re-pointing them
silently is the same error in the other direction.

`shippedCfg` was itself one commit stale: it carried the line budget and not the
experience floor. With `minExp=2, floorMode=top` the simulator now reproduces
`floor2` at **0.000** — exact, against 0.011 before.

Two more traps, both this file's recurring kind. `check` built its variant config
from `arg(flag, default)`, so every field had a value whether or not it was
typed: `--shipped` set `spikeW=6` and the argv default set it straight back to 0.
**Both arms ran the shipped line budget with the spike term switched off, and it
printed a clean paired result.** An absent flag now means absent. And the summary
line printed the *requested* config rather than the merged one, which is how it
stayed invisible — the same shape as the two bugs before it.

### The result

Applied to the surplus only: the top-bullet pass and the floor run first, so a
cut can never reduce an entry to a bare heading. Held out, 66 offers, on the
shipped funnel:

| | baseline | gradeCut 1 | delta | CI95 | w-l | p |
|---|---|---|---|---|---|---|
| `differentiator_coverage` | 0.544 | 0.482 | **−0.062** | [−0.090, −0.037] | 0-18 | <0.0001 |
| `grade_yield` | 0.436 | 0.379 | −0.057 | [−0.075, −0.040] | 0-38 | <0.0001 |

Thresholds 1, 2 and 3 are the same cut — the grades are binary (3026 zeros, 30
mid, 1135 threes across 127 offers × 33 atoms), so any threshold in (0, 3] means
"drop the zeros". Train agrees at −0.070 for all three.

### Why, which is the part worth keeping

**Raising `gradeW` is not an alternative lever either — it is already saturated.**
Sweeping it produced byte-identical output from 0.10 upward, which is benchmark
rule 3's signature; it is not plumbing this time. With grades in {0, 3}, once
`gradeW·3` clears the cosine spread the ordering is "every graded atom, then
every ungraded one", and multiplying further cannot reorder anything. Production
ships 0.10 and is at that ceiling. Rule 4 asked for the cheap explanation to be
controlled for, and the control turns out to be unavailable in principle.

**The page needs more atoms than the judge is willing to grade.** A cut ships
8.38 atoms where the baseline ships 9.94: 40% of what reaches the page is
judge-graded 0, not because the ranker likes it but because the budget still has
room after every graded atom is placed. There is nothing better to promote, so
the cut spends the page on nothing.

**And the judge's zeros are wrong three times in four.** Against the Opus label
corpus, over the same atoms:

```
atoms the 30B graded 0        3026
  Opus labeller also 0         689     precision 0.228
  Opus labeller > 0           2337
    …flagged a differentiator   368
atoms the 30B graded > 0      1165
  Opus labeller > 0           1106     precision 0.950
```

**The judge is a precise positive signal and a near-worthless negative one.**
Weighting reads only the positives, which is why `+0.10 × grade` is worth
+0.115 pair accuracy. Cutting reads only the negatives, and a cut on grade 0
deletes 368 flagged differentiators from the corpus. That asymmetry closes the
item, and it is not a tuning result — no threshold, weight or ordering recovers
information the grade does not carry.

It also puts a number on the "judge grades binary" defect recorded in
`PHASE3-NEXT.md`. The problem is not only that the middle of the scale is unused;
it is that the 0 bucket is 77% false. The judge's *ordering* survives that
(it still earns its 66 s), and any use of its absolute zeros does not.

---

## 17. The section cap — bounding the contest instead of flooring an entry

Backlog item 3, and the direct consequence of §14. If the starvation arrived the
day experience and projects started sharing one pool, the obvious fix is not to
protect an entry inside that contest but to bound it: cap the lines Projects may
take of the 24.

It needs no rule about *which* entry deserves the floor, which is the awkward
question §13 had to answer with a sweep and still only half-answered — the floor
deliberately lifts the teaching assistantship and leaves the commercial role at
one line on most offers.

### The sweep had to learn to see the trade first

`evalCfg` scored coverage and yield. §14 is the case that coverage cannot see a
starved Experience section, so a tool measuring only coverage reports every
balance change as a pure loss — which is exactly what the first run of this
sweep did. `exp_starved` and `all_exp_starved` now come back with every config.

### Held out, 66 offers, shipped funnel, floor already on

| cap | Δ coverage | CI95 | Δ exp_starved | CI95 | w-l |
|---|---|---|---|---|---|
| 16 | −0.001 | [−0.009, 0.007] | −0.061 | [−0.121, −0.015] | 0-4 |
| **14** | **−0.011** | **[−0.029, 0.008]** | **−0.242** | **[−0.348, −0.152]** | **0-16** |
| 12 | −0.052 | [−0.082, −0.023] * | −0.424 | [−0.545, −0.303] | 0-28 |
| 10 | −0.088 (train) | — | −0.70 (train) | — | — |

14 is where the trade turns: starvation falls 0.242 per offer with a CI clear of
zero and 16 losses to 0 wins, while the coverage CI still contains zero. At 12
coverage starts costing for real. Caps of 18 and above never bind — projects do
not take more than 18 lines even unconstrained, which is the null-safety check
that says the knob is a generalisation rather than a second allocator.

**It is not a smaller page.** Total lines move 23.17 → 23.29 of 24 and shipped
atoms 9.94 → 10.01, because experience bullets are cheaper per line than project
bullets, so the same budget buys slightly more of them.

Compare what the floor bought in §13: 0.28 starvation for a real −0.018. The cap
buys 0.242 for a cost indistinguishable from zero, on top of the floor rather
than instead of it — capped-without-floor measured worse on train (0.43 starved
against 0.33 with both).

### The smoke test, read rather than tabulated

Three offers before spending the arm. Offer 111 is the case §13 recorded as
*not* fixed:

```
floor2     exp 2/1   proj 4/2/1
cap14      exp 2/2   proj 3/2/1
```

The bullet the cap bought back is the strongest commercial evidence on the CV —
*"Led a two-developer team building a membership platform … MVP delivered in 4
weeks"* — against a project bullet the posting scored marginally higher. Offer 4
lifted Napier 1 → 2 the same way; offer 56 was unchanged because the cap did not
bind. That is the shape the sweep predicted, on the offers the sweep never saw.

### The arm

`floor2` → `cap14`, paired, n=32, fresh selection in both (a selection change may
not reuse a select cache), 46.7 min:

| metric | floor2 | cap14 | delta | CI95 | w-l | p |
|---|---|---|---|---|---|---|
| `differentiator_coverage` | 0.552 | 0.530 | **−0.022** | [−0.044, −0.006] | 0-4 | 0.125 |
| `exp_starved` | 0.750 | 0.469 | **−0.281** | [−0.438, −0.125] | 0-9 | 0.004 |
| `section_balance` | 0.387 | 0.444 | +0.056 | [0.038, 0.075] | 17-0 | <0.001 |
| `mean_bullets` (experience) | 3.844 | 4.406 | +0.563 | [0.375, 0.750] | 17-0 | <0.001 |
| `noise_rate` | 0.178 | 0.180 | +0.002 | [−0.012, 0.016] | 4-5 | ns |
| `ats_coverage` | 0.659 | 0.655 | −0.004 | [−0.012, 0.003] | 5-6 | ns |

`skill_coverage` 1.000, `grounding` 1.000, `num_retention` 1.000, `metric_fab`,
`product_fab` and `num_lost` all 0.000 delta. Page geometry unmoved: 0.977 →
0.976 pages, 1013 → 1011 px, and the 2-page cap is nowhere near binding.

**And the thing the work was actually for.** Per employer, at one bullet:

| | Teaching Assistant | **commercial role (UBWIS)** |
|---|---|---|
| `sum-v5` (pre-floor) | 81% | 22% |
| `floor2` (floor only) | 56% | 19% |
| **`cap14`** | 44% | **3%** |

Six offers rendering the commercial role as a single line becomes one. That is
the complaint that started §13, and the floor did not fix it — §13 said so
explicitly and could not do better without a blanket floor costing −0.035.

### The simulator was right about the benefit and half-right about the cost

Predicted −0.011 coverage and −0.242 starvation; measured −0.022 and −0.281. The
starvation prediction is good and the **cost prediction is out by 2×, in the
direction that matters** — the sim's coverage CI contained zero and the arm's
does not. Its per-entity prediction was exact (3% for the commercial role, 3%
measured).

So the sweep is trustworthy for ranking configurations and optimistic about what
they cost. Prior calibration points were +0.096 vs +0.101 (Experiment A) and
−0.018 vs −0.012 (§13); this is the first where the sign of the conclusion
depends on the gap, and it did not change it.

### What the trade actually looks like

Offer 151, a full-stack posting: gained *"Automated manual billing and onboarding
with Stripe subscription management and Google OAuth 2.0 (Next.js, Node.js/
Express, MongoDB)"*, lost a bullet about auditing a benchmark and retiring two
metrics. Clearly right.

Offer 111: gained the membership-platform bullet, lost *"Designed a blind
rendezvous protocol with full client-side end-to-end encryption (AES-256-GCM)"* —
a real differentiator, and where the −0.022 comes from.

The cap trades project differentiators for commercial-role evidence. Whether
that is the right trade is a judgement about what a CV reader wants rather than
something the corpus decides — but it is now priced: 0.022 coverage for five
offers' worth of an employer that reads as a single line.

---

## 18. Grade-ordered summary evidence — measured, rejected

Backlog item 7. The summary prompt asks for one quantified achievement and the
model was taking the first plausible evidence line rather than the strongest. The
judge had already graded every one of those bullets and the summary never saw the
grades — and §16 had just established that the judge's *positives* are 0.950
precise, so this used the trustworthy half of its signal.

It does not work, and the two failed shapes on the way are the more useful part.

### Three shapes, two caught by reading before they cost an arm

**Sort everything by grade.** Projects hold 24 of the CV's 33 atoms and most of
the high grades, so this buried Experience and the summary stopped opening with a
positioning line. On the JPMorgan offer, *"Python Software Engineer with advanced
proficiency in system design…"* became a bare skills list that also leaked the
phrase *"in the Re:Link — … Remote Access System entry"* and never mentioned
Python — on a Python role.

Every metric read clean through that: fabrication 0, grounding 1.000, shipped
shape clean, `ats_coverage` up. **`summaryShape`'s `no_positioning` check does not
see a missing opener**, which is worth knowing independently of this result.

**Sort within each section.** Same failure, quieter. The first evidence line
anchors the opener, so reordering Experience rewrites the positioning sentence
rather than the achievement sentence.

**Sort Projects only** — the version that reached an arm, since the achievement
sentence is what the change was aimed at.

### The arm

`cap14` → `gradeord`, paired, n=32, 47.4 min. Every selection metric identically
0.000, which is the check that the change touched only the summary.

| | cap14 | gradeord |
|---|---|---|
| `ats_coverage` | 0.655 | 0.659 (+0.004, CI [0.001, 0.008], 4-0) |
| **generic closer** | **4/32** | **8/32** |
| summaries changed | — | 15/32 |
| mean words | 57.9 | 58.8 |

The generic closer is what ships when a draft falls under the 50-word floor, so
doubling it means the model produced thinner summaries and the stage padded them.
The one gain is `ats_coverage` +0.004 against a ±0.002 A/A floor — twice noise.

Reading all 15 changed summaries: roughly six better, six worse, three neutral.
Offer 210 lost a real misattribution (*"85%+ test coverage in the PM / Software
Engineer role"*, a Re:Link figure hung on UBWIS) and offer 4 gained a cleanly
attributed achievement. Against that, offers 54 and 93 lost their only concrete
achievement to the generic closer, and offer 119 swapped TypeScript/React/Next.js
evidence for Java on a full-stack posting.

A wash on the reads and a real regression on the one shape symptom that is
counted. Reverted.

### What this says about the idea rather than the implementation

The evidence order controls the **opener**, not the achievement sentence. That is
why all three shapes moved the wrong sentence: the model leads with what it is
handed first and fills the rest opportunistically. Pointing it at the strongest
claim needs the claim *marked* rather than *moved* — and §12 already measured
what prompt-level instruction achieves on this stage, which was nothing
(attribution stayed at 3 of 32 before and after being told to attribute).

So the remaining route is a marked-evidence prompt whose prior is poor, and the
plumbing to do it is recoverable from the reverted commits.

---

## 19. Best-of-two summary — SHIPPED. Five offers had no quantified achievement

Backlog item 6, in a shape the backlog did not propose.

`generateSummary` returned the first draft that passed the guards. `scoreSummary`
existed, was tested, and fired only on the repair path — so **clean was acting as
a ranking when it is only a floor.** The guards prove a draft says nothing false;
they cannot tell a summary that answers the posting from one that technically
says nothing at all.

### Two prompts, not two samples

The backlog proposed three drafts at temperature 0.3 with the score choosing, and
noted the cost: sampling gives up the determinism choice, so a single run stops
being a valid A/B and the arm needs repeats where every summary arm so far has
needed one.

The sibling draft already existed. `generateSummary` asks for a second draft
**with the requirements withheld** whenever the first is unusable — a pair that
differs *structurally* rather than by decoding noise. Drafting it unconditionally
and letting `scoreSummary` choose keeps temperature 0, needs no repeats, and adds
one call rather than two.

Tailored wins ties by `margin`. That is load-bearing: the score's evidence-overlap
term reads literal bullet text and a tailored draft spends some of its budget
paraphrasing into the posting's vocabulary, so a bare argmax would hand the page
back to the JD-blind sibling and undo the `ats_coverage` +0.029 that showing the
posting bought (§11).

### The arm

`cap14` → `bestof2`, paired, n=32, 51.3 min (+4.6 over `cap14`, the extra call).

| | cap14 | bestof2 |
|---|---|---|
| **summaries with no figure at all** | **5/32** | **1/32** |
| **generic closer** | **4/32** | **2/32** |
| mean words | 57.9 | 58.0 |
| `ats_coverage` | 0.655 | 0.652 (−0.003, CI [−0.011, 0.002], ns) |
| summaries changed | — | 9/32 |

Every selection metric identically 0.000 — the check that a summary change
touched only the summary. `grounding` 1.000, `metric_fab`, `product_fab`,
`summary_fab_pct`, `summary_fab_raw_pct` and both attribution metrics all flat at
zero. Pages 0.976 → 0.975.

**Mean words is the load-bearing row.** The template requires one quantified
achievement and five summaries were shipping without a single digit; that is now
one, at the same length. This is selection between drafts, not padding.

### Reading all nine

Five clear wins, and all five are the same failure. Offer 219 shipped
*"proven experience in full software development life cycle execution, including
coding standards, code reviews, source control, testing, and operations"* — the
posting's own words, no evidence, no number. It now names Spring Boot,
RabbitMQ, Resilience4j and a 6-person team at Distinction. Offers 165, 205, 58
and 54 are the same story.

Three neutral (156, 231, 79 — both drafts strong, cap14 marginally more on-topic
on 79).

**One real regression, and it is the predicted one.** Offer 175 is a full-stack
posting where the tailored draft named TypeScript, Node.js, React and Kubernetes;
the JD-blind sibling won on score, dropped all four, and picked up a generic
closer. That is exactly the risk `margin` exists to bound, and it bounds it to 1
of 32 rather than to zero. The closer count still falls 4 → 2, so the change
removes three and adds one.

Shipped: five offers rescued from having no quantified achievement, against one
that lost its targeting, with every falsity metric unmoved.

## 20. The two user-layer edits — SHIPPED, +0.050 differentiator coverage, 7-0

Both changes live in the user layer, so neither is a code diff. They landed
together because each invalidates `bestof2` as a control on its own.

1. **`config/profile.yml`** — `cv.pinned_projects` read `"Zero Trust SIEM"`
   against a `cv.md` whose title is `Zero Trust Security Analytics Dashboard`.
   The pin had matched nothing for 16 runs. Now `"Zero Trust Security
   Analytics"`.
2. **`cv.md` `## Skills`** — `RESTful APIs` → `REST / RESTful APIs`,
   `LLM application development` → `AI / LLM application development`, and `Git`
   added beside CI/CD. The first two are notation, not a new claim: `skillForms`
   reads a spaced slash as alternatives, so the taxonomy now answers a posting
   that writes "REST" or "AI". Of the 128-offer corpus, 80 name AI, 14 name REST
   and 8 name Git.

Arm `userlayer`: 32 offers, 51 minutes, temperature 0, `--writer verbatim`,
fresh selection — a selection change may not reuse a select cache. `ok: 32`,
`split_run: null`. Control is `bestof2`.

| metric | bestof2 | userlayer | delta | CI95 | w-l | p |
|---|---|---|---|---|---|---|
| `differentiator_coverage` | 0.530 | **0.580** | +0.050 | [0.017, 0.089] | 7-0 | 0.016 |
| `selection_regret` | 0.084 | 0.102 | +0.018 | [0.009, 0.028] | 17-3 | 0.003 |
| `ats_coverage` | 0.649 | 0.664 | +0.015 | [0.002, 0.028] | 11-5 | 0.210 |
| `skill_coverage` | 0.993 | **1.000** | +0.007 | [0.000, 0.018] | 2-0 | 0.500 |
| `grade_yield` | 0.759 | 0.768 | +0.010 | [−0.010, 0.028] | 13-6 | 0.167 |
| `noise_rate` | 0.180 | 0.190 | +0.010 | [−0.010, 0.033] | 7-6 | 1.000 |
| `exp_starved` | 0.469 | 0.469 | 0.000 | [−0.125, 0.125] | 2-2 | 1.000 |

`grounding`, `num_retention`, `metric_fab`, `product_fab`, `num_lost` and
`all_exp_starved` are all identically flat.

### Which edit moved which metric

They cannot be disentangled by a second arm without splitting them, but the code
answers it directly. `outputChunks` reads experience bullets, project name,
description and bullets, and the summary — never the skills block. And
`selectableAtoms` takes bullets from Experience and Projects only. So editing
`## Skills` cannot touch `differentiator_coverage`, cannot inflate it, and leaves
all 128 Opus labels valid: they are positional against atoms that did not move.
That puts coverage, regret and the bullet allocation on the pin, and
`skill_coverage` on the notation, where 0.993 was two offers naming a form the
taxonomy spelled differently.

### The pin does not do what the pin advertises

`CLAUDE.md` warns that a pin spends one of three project slots. It does — but on
2 of the 7 improved offers the selection is **identical** in both arms and
coverage still rose. Offer 156 ships Re:Link, PQC and Zero Trust in both, and
the allocation moves underneath: Re:Link 1 → 2 bullets, PQC 2 → 1, UBWIS 2 → 3,
Napier 2 → 1. Ten bullets before and ten after, redistributed.

`allocateLines` spends one shared budget, so forcing an entry past the cut
perturbs what everything else gets even when the surviving set does not change.
A pin is a change to the whole allocation and has to be benchmarked as one.

### The disagreement is the point

Regret rose on 17 offers and fell on 3. It is measured against a cosine oracle
over Block B requirements; coverage is measured against the Opus labels. A pin
exists precisely for an entry whose worth is something other than its cosine to
the posting, so this is the case the two metrics were built to disagree about.
The labels win here: 7 offers gained a flagged differentiator, none lost one.

`bestof2` is retired as a control. `userlayer` is the baseline for anything next.

### Reading the summaries, which the table could not see

Rule 7, and it paid again. Every falsity metric above reads flat, and the
summaries still moved — the summary stage is untouched, but selection feeds it,
so changing selection changes what it writes.

Read: the six changed offers whose bench output survives, plus all four
regenerated reports.

- **Offer 185 lost its positioning line.** It now opens *"Particularly in
  developing reliable, fully local LLM pipelines…"* — a fragment with no subject,
  where `bestof2` opened *"Product Engineer with deep expertise in full-stack
  product development…"*. Across all 32, openers that start mid-clause go
  **0/32 → 1/32**. No metric moves on it.
- **Offer 175 welds one project's attributes onto another.** *"a Zero Trust
  Security Analytics Dashboard with end-to-end distributed tracing and resilience
  patterns"* — the tracing and the circuit breakers belong to the Distributed
  Store Management System, which is on the same page. `bestof2` had the same two
  projects apart and said neither.
- **Two of the four regenerated reports borrow a figure.** 240 gives the MongoDB
  project 85%+ test coverage, which is UBWIS's (`cv.md:25`); the rest of that
  sentence is right, since the Zero Trust dashboard *is* the MongoDB client
  partnership. 243 says *"Reduced configuration time by 80%"*, welding UBWIS's
  onboarding figure onto the Teaching Assistant's configuration guides — where
  `cv.md` says 2+ hours to 30 minutes, and 90% is escalations. Reports 241 and
  242 are clean, and 242 quotes the configuration figure exactly right, so the
  stage can do it.

All three are §12's class — a figure or a capability that exists in `cv.md`
attached to an entry that did not earn it — and §12 records `sum-v5` shipping at
0/32 on the bench sample. The class is not fixed; it was measured to zero on one
sample and reappears when the evidence set changes underneath it.

Note what this does not say. `bestof2` was not clean either — its offer 4 summary
ends *"shipped cross-platform tools with 85%+ test coverage"*, the same borrowed
figure under a vaguer noun. One of the three defects is a genuine regression
(185's opener); the other two are the standing defect landing on different
offers.

**The +0.050 stands and the summaries are the open cost.** Coverage is measured
against labels, 7-0, and none of the above touches which evidence reached the
page. What it says is that `summaryUnsupported` cannot see attribution, so the
next summary experiment has a target: entry-scoped figures in the summary, which
§10 gave experience bullets and project blurbs and §12 left undone here.
