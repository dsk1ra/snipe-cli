# docs/ — which file answers which question

The ledgers are append-only and hold the numbers. `CLAUDE.md` and
`batch/CLAUDE.md` state the conclusions; when you need the evidence behind one —
sample size, confidence interval, what was rejected on the way — it is here.

## Start here

| question | file |
|---|---|
| How does the system fit together? | `ARCHITECTURE.md` |
| How do I install and run it? | `SETUP.md` |
| What can I configure without touching code? | `CUSTOMIZATION.md` |
| What does script X do? | `SCRIPTS.md` |

## The measurement ledgers

Phase 3 is split three ways by *what can go wrong*, and each ledger owns one:

| ledger | the failure it tracks |
|---|---|
| `PHASE3-RETRIEVAL-LEDGER.md` | picking the **wrong** CV bullets — ranking, embeddings, the judge rerank |
| `PHASE3-GENERATION-LEDGER.md` | **inventing** things the CV does not say — grounding, fabrication metrics |
| `PHASE3-RETENTION-LEDGER.md` | **losing** what makes the candidate distinct — differentiator coverage |
| `PHASE2-EVIDENCE-LEDGER.md` | Block B: whether a cited CV line really proves the requirement |

`PHASE3-RETENTION-LEDGER.md` §1 is the one to read first if you are adding a
metric: it is the case study in a metric suite that scored an empty CV perfectly.

## Page-budget work

| file | holds |
|---|---|
| `CV-ONE-PAGE-PLAN.md` | the measurements motivating the one-page push |
| `CV-ONE-PAGE-EXPERIMENTS.md` | the falsifiable part — what gets run and what number decides it. §1 is the Phase 3 A/A noise floor |

## Planning documents (not results)

These describe intent and may not reflect what shipped. Check the ledgers before
trusting a number in one.

| file | status |
|---|---|
| `PHASE3-NEXT.md` | current attribution, open experiments, and ideas already closed — **read before re-opening any Phase 3 idea** |
| `CV-GENERATION-BACKLOG.md` | candidate improvements to CV generation — the pick-list, most items now struck through with results |
| `SESSION-HANDOFF.md` | where the CV-generation work stands, what is open, and what is waiting on the user — written to be read cold |
| `TAILORING-QUALITY-PLAN.md` | execution plan for the tailoring campaign |
| `PHASE3-TAILORING-STRATEGY.md` | proposed, awaiting verification — nothing here is implemented |

## Reference

| file | holds |
|---|---|
| `local-parser-cookbook.md` | writing a local parser so `scan.mjs` can read a career page offline |
