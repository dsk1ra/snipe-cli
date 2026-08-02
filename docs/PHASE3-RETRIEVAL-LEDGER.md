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
