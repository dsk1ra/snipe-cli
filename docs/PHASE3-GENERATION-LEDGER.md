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
current sample's ids. This is benchmark rule 1 from `CLAUDE.md` biting in
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

**G2 — drop the worked example. Shipped.** The single largest quality move in
the campaign. 14 of 24 offers were lifting an 8-gram straight out of the
prompt's own example; now 1. `TAILOR_SCHEMA` already guaranteed the JSON shape
through constrained decoding, so the example was demonstrating nothing the
grammar did not already enforce — pure downside, exactly as the plan predicted.

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
