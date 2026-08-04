# Phase 3 selection — retrieval experiment ledger

What was tried to make the pipeline pick the *right* CV bullets and projects for
a posting, what the numbers were, and what was rejected. Append-only.

Primary metric is **pair accuracy** against the human gold set: over every
(ticked, unticked) atom pair, how often the ranking puts the ticked one higher.
Chance is 0.5. It does not move with how many atoms were ticked, which
precision@k does — and the human ticked between 3 and 8 across the twelve
offers, so p@k is not comparable between them.

Reproduce: `node batch/retrieval-bench.mjs run`.

## Ground truth

`batch/bench/goldset.md` — 12 offers × 14 CV atoms (9 experience bullets, 5
projects), hand-ticked. Offers are stratified over eval score ≥ 3.5, which is
the band Phase 3 actually runs on, and deduplicated by company+role.

Marking discipline held: 3–8 ticks per offer, mean 6. Mean label overlap
between any two offers is 0.344, so the picks genuinely vary by posting rather
than being one fixed favourite set.

**Label noise is unmeasured.** No two gold JDs are near-duplicates (max token
Jaccard 0.156), so there is no repeated-item estimate of how consistently the
same human would re-tick the same offer. Every result below is therefore an
underestimate of the true ceiling by an unknown amount.

## Where the signal is

| ranker | pair | note |
|---|---|---|
| random | 0.561 | metric sanity check; theoretical 0.5 |
| JD-blind popularity prior (leave-one-out) | 0.670 | never looks at the posting |
| cosine baseline | 0.764 | production |
| perfect | 1.000 | |

Roughly half of what a ranker can earn here is available without reading the
posting at all — some CV atoms are simply better than others. Retrieval clears
the prior by a significant margin, so the conditional signal is real, but the
headroom above the prior is only about 0.33 and that is what all the work below
is competing over.

## Fixed before any of this

**The entry-name prefix.** `selectCvForJd` embedded every bullet as
`"Teaching Assistant: Delivered technical instruction…"`. On a 25-word bullet
the prefix is a large fraction of the text, so every bullet within an entry was
pulled toward one point.

| | pair |
|---|---|
| prefixed (was) | 0.689 |
| bare bullet | **0.757** |
| prefix on projects only | 0.720 |

Better on 11 of 12 offers, one worse. Shipped.

**Projects scored as one blob.** A project's bullets joined is 100–375 words
against a 24–41 word experience bullet, and a long text's embedding drifts to a
generic centroid — it ranked by length, not relevance, and buried both Rust
systems projects at 13th and 14th for a C++ HFT role. Projects are now scored
by their best individual bullet. Shipped.

## Rejected

**CSLS / hubness correction — 0.686 (−0.078).** The lead hypothesis, and wrong.
Motivated by one bullet ("Built the admin console") topping both a Rust systems
role and a trading analytics role, which is the classic hub signature.
Correcting for it made things worse on 8 of 12 offers. The popularity-prior
control explains why: broadly-relevant bullets are broadly *correct* here, so
penalising them penalises right answers. Hubness correction assumes hubs are
artefacts; in this corpus they are the good bullets.

**30B judge as a label oracle — 0.693 set / 0.670 graded 0-shot.** The plan was
to expand ground truth by having `snipe-eval` label 80 more offers, because at
n=12 the bootstrap CI on a paired delta is about ±0.05 and a variant worth
+0.02 is invisible. Pre-registered criterion: the judge must reproduce the human
well above the 0.764 retrieval baseline, or tuning toward it chases the judge.

It did not. 0-shot the judge graded almost everything 2 or 3 — it does not
discriminate. Two leave-one-out human exemplars fixed the discrimination and
lifted it to **0.739**, which is a large gain and still below the baseline.
Rejected as a label source by the stated criterion.

Kept as a *feature*: at 2-shot the judge's precision@k is 0.670 against the
embeddings' 0.635, so it knows something the embeddings do not, even though its
full ranking is worse. Evaluated below with the exemplar offers dropped, since
they are training data.

## Sweep 1 — 12 offers, nothing significant

| variant | pair | Δ vs base | CI95 | w/l |
|---|---|---|---|---|
| hyb-0.10 | 0.787 | +0.023 | [−0.016, 0.065] | 6/2 |
| rrf-cos-bm25 | 0.786 | +0.023 | [−0.038, 0.079] | 8/3 |
| hyb-0.20 | 0.779 | +0.016 | [−0.047, 0.077] | 5/3 |
| zscore | 0.776 | +0.013 | [−0.044, 0.080] | 4/5 |
| **base-cos** | **0.764** | — | — | — |
| bm25 | 0.715 | −0.048 | [−0.153, 0.053] | 4/8 |
| csls | 0.686 | −0.078 | [−0.154, 0.007] | 3/8 |

Every CI straddles zero. The lexical hybrid is the most consistent direction
(6 wins, 2 losses) and is the reason BM25 stays in the portfolio despite being
poor alone.

## Sweep 2 — 28 variants, judge as a feature (n=10, exemplars dropped)

| variant | pair | Δ vs base | CI95 | w/l | sig |
|---|---|---|---|---|---|
| rrf-cos-judge-bm25 | 0.852 | +0.096 | [0.029, 0.163] | 8/1 | **YES** |
| cos+judge-0.10 | 0.851 | +0.095 | [0.029, 0.175] | 8/1 | **YES** |
| cos+judge-0.05 | 0.844 | +0.088 | [0.029, 0.157] | 8/1 | **YES** |
| rrf-cos-judge | 0.829 | +0.073 | [0.008, 0.131] | 7/3 | **YES** |
| cos+judge-0.02 | 0.823 | +0.067 | [0.027, 0.112] | 8/1 | **YES** |
| rrf-cos-bm25 | 0.797 | +0.041 | [−0.002, 0.093] | 7/2 | no |
| hyde+cos+bm25 | 0.790 | +0.034 | [−0.008, 0.077] | 6/3 | no |
| hyb-0.10 | 0.789 | +0.033 | [−0.002, 0.075] | 5/1 | no |
| top2-cos | 0.773 | +0.017 | [−0.005, 0.046] | 4/2 | no |
| **base-cos** | **0.756** | — | — | — | — |
| zscore | 0.753 | −0.004 | | 3/5 | no |
| hyde+cos | 0.746 | −0.010 | | 6/3 | no |
| judge (alone) | 0.730 | −0.026 | | 4/6 | no |
| bm25 | 0.726 | −0.031 | | 3/7 | no |
| csls | 0.695 | −0.061 | | 3/6 | no |
| hyde | 0.651 | −0.106 | [−0.204, −0.033] | 1/9 | WORSE |
| prior-only | 0.644 | −0.112 | [−0.187, −0.036] | 1/8 | WORSE |
| instruct | 0.633 | −0.123 | [−0.197, −0.061] | 1/9 | WORSE |
| random | 0.595 | −0.161 | | 1/9 | WORSE |

Twenty-eight variants against ten offers would produce roughly one false
positive at α=0.05. Five significant results, all from one family, is not that
shape.

### Robustness: swap the exemplars

The judge's two exemplars are the one place the human's labels enter the
ranker, so the obvious failure is a gain fitted to two lucky offers. Regraded
with a disjoint exemplar pair (167, 182 instead of 5, 50):

| variant | pair | Δ | CI95 | w/l |
|---|---|---|---|---|
| cos+judge-0.10 | 0.886 | +0.102 | [0.027, 0.190] | 7/0 |
| rrf-cos-judge-bm25 | 0.870 | +0.087 | [0.015, 0.160] | 7/2 |
| cos+judge-0.05 | 0.865 | +0.081 | [0.031, 0.142] | 8/0 |
| base-cos | 0.783 | — | — | — |

The gain holds and slightly strengthens. `cos+judge-0.10` is top or joint-top
under both exemplar pairs (8/1 and 7/0), so that is what shipped.

### Two clean negatives worth not repeating

**Qwen3-Embedding's instruction prefix — 0.633, significantly worse.** The
model documents a query-side `Instruct: …\nQuery:…` format and it is reported
to help on MTEB. Here it is the second-worst variant tested, below the JD-blind
prior. `snipe-embed` is a Modelfile over the base, so the template may already
account for it; either way, do not add it.

**HyDE — 0.651 alone, significantly worse.** Rewriting each requirement into a
hypothetical CV bullet before embedding was meant to fix the register mismatch
between a demand ("Strong commercial C++ experience") and an achievement
("Built a Rust microservice testbed…"). The rewrite loses more than the
mismatch costs. It survives only inside a three-way ensemble, marginally, and
is not worth a 7B call per requirement.

## Shipped

`cos + 0.10 × judge_grade`, in `selectCvForJd`.

- Exemplars live in `batch/judge-shots.json`, written by
  `node batch/goldset.mjs export-shots --ids 5,50`. Keyed by bullet **text**,
  not id — ids are positional in cv.md, so adding one bullet renumbers
  everything after it and a baked id would silently point at the wrong bullet.
  Any exemplar whose text no longer matches is dropped rather than guessed.
- No exemplars means no judge call at all. 0-shot scores 0.670 against cosine's
  0.756, so degrading to it would be worse than not running.
- Any failure — model missing, timeout, malformed JSON — returns null and the
  cosine ranking stands. Covered by self-checks.
- **Cost: +44s per Phase 3 run** (3.2s → 47.4s on a 12-requirement offer).
  One 30B call. Phase 2 already runs the same model for minutes, and Phase 3
  only runs above the score threshold, so this is a small share of a run.

## Still open

- **Label noise is unmeasured**, so the true ceiling is unknown. The cheapest
  fix is a second gold sheet that repeats two offers from the first.
- ~~**n=10 after exemplars.**~~ **RESOLVED** — confirmed on sheet 2, see
  *Held-out confirmation*.
- The lexical hybrid (`rrf-cos-bm25`, +0.041, 7/2) is free and consistently
  positive across both sweeps but never significant. It is a reasonable thing
  to combine with the judge — `rrf-cos-judge-bm25` was the top variant under
  the original exemplars — but on this evidence it is not distinguishable from
  `cos+judge-0.10`, which is simpler.
  **Settled on sheet 2, against it:** `rrf-cos-bm25` −0.045 (3/7),
  `rrf-cos-judge-bm25` +0.016 (ns), and adding bm25 to the shipped blend
  (`cos+judge-0.10+bm25`, +0.100) is *below* the blend without it. Dropped.

## Sweep 3 — tuning the shipped variant

**Judge weight is a plateau, not a peak.**

| weight | pair |
|---|---|
| 0.05 | 0.844 |
| 0.10 | 0.851 |
| 0.15 / 0.25 / 0.50 | 0.849 |

Everything from 0.10 up is the same answer: past that point the grade decides
the ordering and the cosine only breaks ties within a grade. 0.10 is the
smallest weight on the plateau, so it keeps the most cosine information for the
ties, and it is not a value that had to be found precisely.

**More exemplars do not pay.** Four exemplars instead of two: Δ +0.107 at n=8
against +0.095 at n=10, absolute 0.838 against 0.851. Flat, while costing
prompt tokens and two more offers of held-out data. The jump is entirely
0-shot → 2-shot (0.670 → 0.739); the curve is level after that.

**Block B is not lossy.** Requirement-shaped sentences taken straight from the
posting score 0.757 against Block B's 0.756 — indistinguishable. Everything
downstream depends on the 30B's parse of the posting, and this says that parse
is not throwing away retrievable signal. Hypothesis eliminated, no change.

**BM25 on top of the judge** is ahead in two of three exemplar configurations
and behind in the third, by ~0.006 each way. Not distinguishable; not shipped,
on grounds of simplicity.

## What the change actually does

C++ / HFT posting (#111), requirements "strong commercial C++", "algorithms,
data structures and concurrency", "high-performance or low-latency systems".
Bullets the reranker changed:

```
- Built the Axum signalling server with a broadcast-channel push handler
- Designed a blind rendezvous protocol with client-side end-to-end encryption
- Migrating the ephemeral state store from Redis to Valkey
+ Implemented an actor-based async file-transfer engine over WebRTC
+ Engineered a lock-free, pre-allocated video frame ring (atomic ...)
+ Built a GStreamer/PipeWire screen-capture pipeline for Wayland
```

A lock-free pre-allocated ring buffer is the single most on-target thing on the
CV for a low-latency C++ role, and cosine ranked it below a Redis migration.

## Still open

- **Label noise is unmeasured**, so the true ceiling is unknown. Cheapest fix is
  a second sheet that repeats two offers from the first.
- ~~**A second gold sheet is generated and unlabelled**~~ — **RESOLVED**, see
  *Held-out confirmation* below. `batch/bench/goldset-2.md` is labelled (86
  ticks) and `cos+judge-0.10` is confirmed on it.

## Sweep 4 — a bigger embedder is worse

`snipe-embed` is Qwen3-Embedding 0.6B at q8_0. Swapping in the 4B is the most
obvious "just use a better model" move available locally, and it loses:

| embedder | base-cos | cos+judge-0.10 |
|---|---|---|
| snipe-embed (0.6B q8_0) | **0.756** | **0.851** |
| qwen3-embedding:0.6b-q8_0 (raw base) | 0.756 | 0.851 |
| qwen3-embedding:4b | 0.715 | 0.791 |

The raw base scoring identically to `snipe-embed` rules out the Modelfile as
the cause — it is a bare `FROM` with no configuration — so this is size and
quantization, not setup. The 4B ships at 2.5 GB for 4B parameters, roughly q4,
against 0.6B at q8_0; on this task the heavier quantization costs more than the
extra parameters buy. It is also a worse fit for a 6 GB card.

The instruction prefix hurts the 4B as well (0.673 against 0.715), matching the
0.6B result. Two models, same direction — the likely explanation is that
Ollama's embed endpoint already applies the model's template and the manual
`Instruct:` prefix double-applies it. Do not add it to either.

**Keep `snipe-embed`.** "Upgrade the embedder" is the natural next idea and it
is measured here as a regression.

## How much more labelling is worth it

More JDs do not help. The binding constraint is *labelled* offers: 186 JDs are
cached, 12 carry ticks, and a sweep over unlabelled JDs has nothing to score
against.

Power, from the observed per-offer SD of the paired delta (paired t, 80% power,
α=0.05):

| improvement worth detecting | labelled offers needed | labelling time at ~25 min / 12 |
|---|---|---|
| +0.05 | 50 | ~1h 45m |
| +0.03 | 137 | ~4h 45m |
| +0.02 | 308 | ~10h 40m |

The shipped +0.095 needed n=14 and had n=10; it landed because the effect was
large. What remains is not:

| candidate | Δ | SD | n needed |
|---|---|---|---|
| cos+judge-0.10 (shipped) | +0.095 | 0.125 | 14 |
| rrf-cos-bm25 | +0.042 | 0.081 | 29 |
| hyb-0.10 | +0.035 | 0.063 | 26 |
| top2-cos | +0.017 | 0.044 | 54 |

So variant-hunting is done, not for lack of ideas but because the effects left
are smaller than the available ground truth can resolve. One more sheet (n≈22)
is worth it — it confirms the winner on offers that played no part in choosing
it, which nothing so far has done. Beyond that the return falls off fast.

## Degeneracy check (label-free, scales)

The risk the gold set cannot see is the reranker collapsing to one favourite
set regardless of posting. Over the 12 graded offers:

```
distinct top-6 sets      11 / 12
per-atom entropy         0.68   (1.0 = maximally JD-dependent)
always selected          1 atom   (800+ students, Java and C++ teaching)
never selected           1 atom   (Terms & Conditions / GDPR documentation)
```

No collapse, and both extremes are the right ones. This class of check needs no
labels and is what a 150-JD run is actually good for — alongside the
`ats_coverage` / `product_fab` metrics in PHASE3-TAILORING-STRATEGY.md, which
are also label-free and currently unbuilt.

## Held-out confirmation — sheet 2, 12 fresh offers

`batch/bench/goldset-2.md`, hand-ticked, 86 ticks over 12 offers × 14 atoms, no
offer overlapping sheet 1. This is the check every result above lacked: the
winner was chosen on sheet 1 and these offers played no part in choosing it.

```
node batch/retrieval-bench.mjs run --sheet batch/bench/goldset-2.md \
  --grades batch/bench/judge-grades-2.json
```

| variant | pair | Δpair | CI95 | w/l | sig |
|---|---|---|---|---|---|
| cos+jdsent+judge | 0.940 | +0.126 | [0.085, 0.172] | 12/0 | YES |
| **cos+judge-0.10** (shipped) | **0.930** | **+0.115** | **[0.074, 0.159]** | **11/0** | **YES** |
| cos+judge-0.05 | 0.921 | +0.107 | [0.071, 0.144] | 11/0 | YES |
| base-cos | 0.815 | — | — | — | — |
| judge (alone) | 0.801 | −0.014 | [−0.092, 0.065] | 6/6 | no |
| bm25 | 0.627 | −0.187 | [−0.275, −0.095] | 2/10 | WORSE |
| random | 0.517 | −0.297 | [−0.436, −0.162] | 2/10 | WORSE |

**The shipped variant holds, and by more than it earned on sheet 1** (+0.115 vs
+0.095 there), 11 offers better and none worse. Every significant variant in the
run is a judge blend; nothing without the judge clears the CI. The judge *alone*
is again below plain cosine (0.801 vs 0.815) — same conclusion as sheet 1 from
independent offers, so "only the blend pays" is now confirmed rather than
observed once.

Run twice, before and after an edit to the Snipe project atom in `cv.md`:
identical to three decimals. The bench keeps its own text-keyed embedding cache
(`batch/bench/embed-cache-snipe-embed.json`) and imports `embed` directly rather
than going through `loadCvIndex`, so changed atom text is a cache miss and gets
re-embedded — the result is genuinely insensitive to that atom's wording, not
cached past the change.

### cos+jdsent+judge scored higher and is NOT shipped

0.940 / +0.126 / 12–0 is nominally the best row in the table. Deliberately not
taken:

- **Sheet 2 is the confirmation set for `cos+judge-0.10`.** Promoting a
  different variant off the same sheet converts it from a held-out check into a
  selection set, and there is no third sheet left to confirm against. That is
  the whole failure this sheet existed to avoid.
- **The margin is inside the noise.** +0.011 over the shipped variant, with CIs
  overlapping over nearly their whole length ([0.085, 0.172] vs
  [0.074, 0.159]). 36 variants against 12 offers will produce a top row this
  close by chance.
- **The added signal does nothing on its own**: `cos+jdsent` is +0.013 (ns) and
  `jdsent` alone is −0.004. Consistent with sheet 1, where requirement-shaped
  JD sentences scored 0.757 against Block B's 0.756 — indistinguishable.

To ship it properly: label a third sheet, or pre-register a two-variant
comparison (`cos+judge-0.10` vs `cos+jdsent+judge`, nothing else in the run) so
the test is not a 36-way max.
