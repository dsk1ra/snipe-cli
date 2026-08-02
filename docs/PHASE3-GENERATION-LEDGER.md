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

## 3. Label noise — the ceiling on all retrieval work

Two of gold sheet 1's offers were re-labelled blind, with the original ticks
withheld from the labeller (M2):

```
per-atom agreement  0.857  (24/28)
jaccard             0.750
pair accuracy       0.875   identical on both offers, both directions
```

**The shipped ranker scores 0.851. Two careful humans agree with each other at
0.875.** The remaining headroom is 0.024, against a resolution limit that needs
50 labelled offers to see 0.05.

This retires retrieval as a work area, and it is the plan's own named risk #3
firing. R1 (doc2query), R3 (MMR/assignment), R4 (other embedders), R5 (learned
blend) and R6 (z-norm) are dropped, not attempted-and-failed. R2 (cross-encoder)
survives only as a **latency** candidate: matching 0.851 in ~1 s instead of 66 s
is worth having at equal quality.

Caveat that bounds the claim: n = 2 offers. Both landed on exactly 0.875, which
is reassuring but is not a tight interval.

## 4. Changes

Recorded per change, with the metric it targeted and what actually moved.
