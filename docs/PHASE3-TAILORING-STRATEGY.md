# Phase 3 tailoring — calibration strategy

Status: **proposed, awaiting verification.** Nothing here is implemented yet.

## The problem, stated once

Three objectives, two of which pull against each other:

1. **ATS keyword strength.** The CV must use the JD's vocabulary or it dies before a
   human reads it.
2. **Truth at the level of the whole picture.** A stretch is fine. A checkable
   falsehood is not, because it dies in the first screen and costs the room.
3. **Selection.** Right facts, wrong emphasis — the live defect. The pipeline
   picks bullets and projects that are not what the JD is asking about.

Education and certifications are static. They are not tailored and this document
does not touch them. The tailoring surface is **summary, experience, projects,
skills** — plus core competencies, which are already close to right.

## Confirmed decisions

| Decision | Choice |
|---|---|
| Truth line | Two-tier: generic capability phrases free, named products grounded |
| Ground truth for selection | ~12-offer gold set validating a label-free proxy |
| Benchmark sample | Rebuild, stratified toward apply-worthy (score ≥ 3.5) |
| Scope | Selection moves into code; the model only rewrites wording |

## Why the current filter is the wrong shape

Measured over the last 30 reports, 592 JD keywords:

| filter | keywords surviving |
|---|---|
| none | 100% |
| word-level | 50% |
| phrase-level (currently on disk) | 34% |

Tightening to phrase-level discarded `API design`, `system architecture`,
`platform engineering`, `Security Engineering`, `end-to-end development`. Those
are all real. It also discarded `Google Cloud`, `Terraform`, `Blue Prism`,
`Genesys` — those are not.

Word-vs-phrase was the wrong axis. The distinction that matters:

- **Capability phrase** — a description of work ("API design", "distributed
  systems", "platform engineering"). Survives an interview because the underlying
  work is real. **Free to take from the JD.**
- **Named product** — a vendor, tool or platform ("Terraform", "Azure", "Blue
  Prism", "Snowflake", "Genesys"). Binary and checkable. **Requires CV support.**

This rule applies uniformly to competencies, skills rows and the summary. Bullets
stay strict regardless — they are the part an interviewer drills into.

## Phase 0 — measurement (no behaviour change)

Nothing gets tuned before it can be measured. `batch/tailor-harness.mjs` already
covers truthfulness and is the base to extend:

```
role_retention 1.0   invented_roles 0   metric_fab 0
grounding 0.90       example_copy_pct 0.292   ← live defect, 29% plagiarise the
                                                prompt's own worked example
```

Four metrics get added. All label-free, all checked against source text with no
model in the loop — the property that made the existing metrics trustworthy.

**`ats_coverage`** — of the JD requirement terms the CV *can* legitimately
support, what fraction appear in the tailored output. Scored against the
supportable subset deliberately, so it cannot be gamed by inventing.

**`selection_regret`** — embed the Block B requirements, score every `cv.md`
bullet against them, and compare what shipped against the best-possible pick of
the same size. `0` = optimal selection. Reported per section (experience,
projects). This is the direct measurement of "picks random unrelated ones".

**`summary_jd_fit` / `summary_cv_fit`** — two separate cosines, never one. A
summary that parrots the posting scores high on the first and low on the second;
generic boilerplate scores low on both. Both must move.

**`product_fab`** — count of named-product claims in the output absent from
`cv.md`. Must stay 0. This is what makes the two-tier rule enforceable rather
than aspirational.

The benchmark sample is rebuilt stratified toward score ≥ 3.5 and fixed once.
v6 is re-run against it to re-establish a baseline (~13 min), since the existing
v0–v6 history was measured on a sample skewed to offers that would never be
applied to.

**Gate:** the proxy's bullet ranking agrees with the gold set. If it does not,
the proxy is wrong and tuning against it would optimise toward the wrong target.

## Gold set — the only part needing your time

12 offers drawn from the new sample. I generate a sheet listing every `cv.md`
bullet and project; you mark which ones belong on the CV for that offer. Roughly
30–45 minutes.

It is used **only** to validate that `selection_regret` agrees with your
judgement — never tuned against directly, so it cannot be overfitted, and it
stays a genuine check at the end of each phase.

## Phase 1 — selection into code

Every defect fixed this session had the same shape: the 7B deciding something it
should not decide. Which company a bullet belonged to. Which project a
description described. Which skills to list. Selection is that same bug one level
up.

`cv-select.mjs` already ranks bullets by embedding, but the model may ignore the
ranking and the density ladder re-cut by date. After this phase: code fixes the
set and the order; the model receives the final selection and rewrites wording
only. It cannot add or drop an entry.

**Gate:** `selection_regret` down; `role_retention` 1.0 and `metric_fab` 0 hold.

## Phase 2 — summary as its own stage

Today the summary is one field in a large JSON blob and gets a length-driven
repair pass. It becomes its own generation stage with the selected evidence as
its input, so it describes what the CV actually shows.

**Gate:** both alignment numbers up, nothing else regresses.

## Phase 3 — two-tier vocabulary

Replaces the filter currently on disk. Sequenced last on purpose: with
`ats_coverage` and `product_fab` in place we can see whether loosening actually
buys keyword strength without buying fabrication. Doing it first would be
guessing.

**Gate:** `ats_coverage` up toward the 50%+ band; `product_fab` stays 0.

## Benchmark discipline

From batch/CLAUDE.md, learned the hard way and not relitigated here:

- Temperature 0. Greedy decoding is byte-identical on this stack, so the noise
  floor is 0 and a single run is a valid A/B.
- Compare two runs made now. Historical artifacts are not a control.
- Know the resolution limit before believing a delta.
- Report pair accuracy alongside any correlation.

## Named risks

- **Embedding relevance ≠ recruiter relevance.** This is precisely what the gold
  set exists to catch. If it disagrees, the proxy changes, not the labels.
- **Repetition.** Deterministic selection means similar JDs produce similar CVs.
  That is correct behaviour, but worth seeing before deciding it is fine.
- **`ats_coverage` invites stuffing.** Capped, and held down by `product_fab`
  and by grounding staying flat.

## Out of scope

Education, certifications, the evaluator's scoring (Phases 1–2), and the
`hard_stops` schema bug — that last one is real and worth fixing, but it is a
Phase 2 evaluator defect, not a tailoring one.
