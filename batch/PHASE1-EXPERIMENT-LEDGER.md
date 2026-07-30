# Phase 1 accuracy / hallucination experiment ledger

**Reference truth:** Phase 2 (`batch/evals/`, 30B staged, evidence-grounded), 115 offers
overlapping `batch/scores` + `batch/jds`. Rank metrics are offset-immune so the archive's
~+0.5 calibration drift (CLAUDE.md benchmark rule 1) cancels out.

**Metrics**

| name | meaning | want |
|------|---------|------|
| `rho` | Spearman P1 vs P2 | high |
| `pair_acc` | fraction of P2-separated pairs P1 orders right | high |
| `recall_good` | of P2>=4.0 offers, fraction P1 passes the gate | **1.0** (never drop a good job) |
| `drop_bad` | of P2<3.0 offers, fraction P1 drops | high |
| `bal_acc` | mean of the two above | high |
| `fab_jd` | gap claims naming a tech absent from the JD | 0 |
| `fab_cv` | flat denials ("no X") where `cv.md` names X | 0 |

All runs `temperature 0`. 11.4 s/offer, 115 offers = ~22 min/run.
Harness: `run.sh <label> full|dev` → `metrics.mjs <runDir> <label>` → `ledger.jsonl`.
`sweep.mjs` sweeps the gate over an existing run for free.

## FINAL — all 115 offers, all variants, gate 2.5  ✅ ship V5

| variant | rho | pair_acc | recall_good | drop_bad | bal_acc | fab_jd | good lost |
|---------|-----|----------|-------------|----------|---------|--------|-----------|
| V0 baseline | 0.346 | 0.629 | 0.949 | 0.333 | 0.641 | 8.2% | #39, #41 |
| V1 | 0.663 | 0.754 | **1.000** | 0.722 | 0.861 | 2.8% | — |
| V3 | 0.658 | 0.752 | 0.974 | 0.722 | 0.848 | 1.5% | #39 |
| V4 | 0.658 | 0.752 | 0.974 | 0.722 | 0.848 | 1.5% | #39 |
| **V5 (shipped)** | **0.666** | **0.755** | 0.974 | **0.778** | **0.876** | **0.9%** | #39 |

V5 wins everything except `recall_good`, where it trades offer #39 for one extra genuine
mismatch dropped — a net wash in raw counts (V1: 39 good kept / 13 bad dropped;
V5: 38 / 14) in exchange for **~9x less fabrication**. `rho` and `pair_acc` differences
among V1–V5 are noise; the real V0→V1 jump is the accuracy story, and V3→V5 is the
hallucination story.

**V5's 6 timeouts were transient**, not a prompt cost: all 6 scored normally on retry
(4.4, 4.4, 5.0, 5.0, 3.4, 3.4) with median-sized JDs, and `local-runner.sh` already retries.

## Results (gate 2.0 unless noted)

| variant | rho | pair_acc | recall_good | drop_bad | bal_acc | fab_jd | fab_cv |
|---------|-----|----------|-------------|----------|---------|--------|--------|
| V0 baseline (orig prompt, t0.1) | 0.346 | 0.629 | 0.974 | 0.167 | 0.571 | 8.2% | 5/9 |
| V1 enum + no model hard_stops + t0 | 0.663 | 0.754 | **1.000** | 0.389 | 0.694 | 2.8% | 4/7 |
| V1 @ gate 2.5 (free sweep) | — | — | **1.000** | 0.722 | **0.861** | — | — |
| V2 = V1 + post-hoc cv-contradiction filter | 0.663 | 0.754 | 1.000 | 0.389 | 0.694 | 2.9% | 6/10 |
| **V3 = V1 + quote-grounded soft_gaps** | 0.658 | 0.752 | **1.000** | 0.389 | 0.694 | **1.5%** | 1/1 |
| V4 = V3 + top_strengths verifier + raw dims | 0.658 | 0.752 | 1.000 | 0.389 | 0.694 | 1.5% | 1/1 |
| V5 = V4 + gaps must fail BOTH conditions | 0.666 | 0.755 | 0.974 | 0.778 | **0.876** | 0.9% | 1/1 |

### V5 — a gap must satisfy BOTH conditions  ✅ SHIPPED
Prompt now states the two conditions explicitly with #39's exact failure as the worked
example. Best variant overall: `drop_bad` 0.722 → 0.778, `fab_jd` down to **1 claim in
107**, modal_mass slightly improved (0.365 → 0.357).

**It did not rescue #39.** The 4B still lists *"Experience with SQL and relational
databases"* as a gap for that offer even with that literal case spelled out in the prompt.
Prompt instruction alone cannot fix a 4B's CV lookup — worth knowing before spending more
prompt budget on it. The remaining lever would be structural (make the model emit the CV
line it checked, like `jd_quote` does for the JD side), not more wording.

**At the shipping gate 2.5** (the number that actually matters):

| variant | recall_good | drop_bad | bal_acc | good lost |
|---------|-------------|----------|---------|-----------|
| V0 | 0.949 | 0.333 | 0.641 | #39, #41 |
| **V1** | **1.000** | 0.722 | **0.861** | — |
| V4 | 0.974 | 0.722 | 0.848 | #39 |

`modal_mass` (label-free): V0 0.261 · V1 0.365 · V3 0.365 — the 3.4 pile is untouched by
anything done so far, because none of it changed the cap.

## Variants tried

### V0 — baseline (production before this work)
Deal-breaker checklist in prompt, free-text `archetype`, model-generated `hard_stops`, t0.1.
28/115 "Outside targets". Loses #39 (P2 4.7). Real fabrications present, e.g.
*"Python proficiency required but no Python experience in CV"* — cv.md lists Python.

### V1 — archetype enum + hard_stops removed + t0  ✅ big win
`hard_stops` out of the schema (was 90% verbatim checklist echo, 74/82); `archetype`
constrained to a `profile.yml` enum; checklist deleted from the prompt; worked examples
rewritten (one literally taught the bug); t0.1 → 0.
rho +0.32, pair_acc +0.13, recall_good 1.000, fab_jd cut 3x, "Outside targets" 28 → 9.

### V2 — post-hoc filter dropping gaps that contradict cv.md  ❌ not worth it
Dropped only 4 gaps corpus-wide; no score change by construction. **Superseded by the
metric fix below** — most of what it targeted was never fabrication.

### V3 — quote-grounded soft_gaps  ✅ keep (free hallucination cut)
`soft_gaps` schema → `{requirement, jd_quote}`; `verifyGaps()` drops any gap whose quote
is not a verbatim substring of the JD (whitespace/case-normalised, min 12 chars).
Prompt tells the model unverifiable gaps are discarded.

**Caught 22 invented requirements (3.8% of 572 gaps).** `fab_jd` 2.8% → 1.5%, and both
survivors are the known contrast-clause artifact, not fabrication. Accuracy unchanged:
rho 0.663 → 0.658, pair_acc 0.754 → 0.752 — well within noise at n=115. Only 1 offer lost
all its gaps. Cost: more output tokens per call.

### V4 — `top_strengths` verified against cv.md + raw dims recorded  ✅ keep
`verifyAgainstCv()` in `fit-rules.mjs` (shared, so Phase 2 can adopt it) drops any claim
naming a technology `cv.md` never mentions. Measured on the existing runs — post-hoc, so
no GPU cost to evaluate:

| run | strengths | dropped as unsupported |
|-----|-----------|------------------------|
| V0 | 351 | 3 (0.9%) |
| V1 | 351 | 5 (1.4%) |
| V3 | 345 | 5 (1.4%) |

Real fabrications removed: strengths pairing a technology the CV does list with a
neighbouring one it does not — a sibling cloud provider, another web framework in the same
family, a second JVM language. Rate is low but the consequence is high — this is the field
that asserts things on the candidate's behalf.

Also records `cv_match_raw` / `north_star_raw` / `cv_cap` / `ns_cap` so cap policy can be
evaluated offline. Only the 58 capped offers needed re-running; for the other 57 raw == final.

## Measurement corrections (things I got wrong)

1. **`fab_cv` was over-counting ~5x.** The first version flagged any absence-word near a
   tech that `cv.md` mentions. But where `cv.md` qualifies a skill as
   self-study/working-knowledge rather than production use, a model saying
   *"has working knowledge, not production-level"* is **accurate**, not fabricated. Likewise EKS / SQS / CloudWatch / EventBridge are genuinely absent
   while "AWS" is present. Metric now counts only **flat denials** with no qualifier.
   The "71%, unchanged" figure I reported was mostly artifact.
2. **`fab_jd` still over-counts.** All 3 V1 hits are the same claim, of the form
   *"JD wants cloud provider X, CV shows provider Y"* — flagged only because the CV-side
   technology is absent from the JD. Legitimate contrast, not fabrication. Hand-adjudicated V1 fabrication rate is ~0–1 in 107 claims.
3. **Gate 2.0 was the wrong call.** Made when the model was broken. V1's sweep shows a
   plateau: recall_good stays 1.000 from gate 1.5 all the way to 3.0, while drop_bad
   jumps 0.389 → 0.722 at 2.5. The original 2.5 default was right; the *model* was wrong.
   Picking the safe end of a flat plateau → **2.5**.

## Root cause found: the seniority cap is a degenerate attractor

| tier | n | mean P2 | P2>=4 | P2<3 | P1 == 3.4 |
|------|---|---------|-------|------|-----------|
| no cap | 58 | 4.21 | 38 | 4 | 0 |
| Senior/5+ | 49 | 3.28 | **1** | 6 | **42** |
| Staff/8+ | 8 | 2.25 | 0 | 8 | 0 |

`seniorityCaps` returns `{cvCap:3, nsCap:4}` for Senior/5+, whose exact score is
`3×0.625 + 4×0.375 = 3.4`. The 4B scores at or above that ceiling every time, so **42 of
49 senior-titled offers collapse onto one value** whose true P2 ranges 1.0 → 4.8. No gate
can split them. This is simultaneously the `drop_bad` ceiling and the `rho` ceiling.

Every offer that beats gate 2.5 while P2 says skip is in this pile: #13 (P2 1.0),
#30 (1.6), #129 (2.3), #14 (2.4), #51 (2.6) — all cv3/ns4.

**Caveat (circularity):** Phase 2 applies the *same* cap, so P2's low senior scores are
partly caused by it. But P2 still spreads within the band while P1 does not — the failure
is P1's lack of discrimination, which is not circular.

## ⚠ The reference truth is contaminated (biggest finding)

Phase 2 fabricates too, so `drop_bad` is not a usable objective.

**Proof — offer #13.** P2 scored it **1.0**, `cv_coverage: 0`, notes: *"the candidate
lacks all MUST requirements: [four named technologies], and UK work rights."*
The JD names all four (3, 3, 3 and 1 occurrences) — and **`cv.md` contains every one**. No JD in the whole corpus mentions work
rights or sponsorship; that requirement was invented. Phase 1's 3.4 was closer to correct
than the label calling it 1.0.

**Composition of the 17 "bad" labels:**
- 13/17 have `cv.md` covering >=60% of the technologies their JD names.
- Several carry P2's *own* `cv_coverage` of 0.8–1.0 yet still score 2.4 (#71 cov=1.0,
  #63 cov=0.875, #14 cov=0.844, #49 cov=0.8) — all Staff/Senior/Architect titles, i.e.
  pushed down by **P2's own `seniorityCaps`**, the same rule P1 applies.
- Mean JD-tech-in-CV: good labels 91%, bad labels 79%. Barely separated.

So the bad class = {P2 fabrications} ∪ {seniority-capped}. Tuning P1's cap to maximise
`drop_bad` = fitting P1's cap to P2's cap plus P2's mistakes. **Abandoned that objective.**

### What survives as trustworthy
| metric | trustworthy? | why |
|--------|--------------|-----|
| `fab_jd`, `fab_cv` | ✅ yes | measured against JD/CV text directly, no model in the loop |
| `modal_mass`, `entropy` | ✅ yes | label-free; a gate cannot split offers sharing a score |
| `recall_good` | ~ mostly | fabricated gaps push scores *down*, so false "good" labels are unlikely |
| `rho`, `pair_acc` | ~ partly | large deltas survive a few bad labels; small ones do not |
| `drop_bad` | ❌ no | contaminated as above |

### Cap policy — tested on the 58 capped offers, ❌ negative result
Recorded raw dims, then compared four policies offline (one GPU run, four policies):

| policy | rho | pair_acc | modal_mass | distinct |
|--------|-----|----------|------------|----------|
| **clamp (current)** | **0.474** | 0.655 | 0.724 | 6 |
| rescale both, `d' = 1+(d-1)(cap-1)/4` | 0.427 | 0.676 | **0.397** | 9 |
| clamp cv, ns uncapped | 0.397 | 0.624 | 0.741 | 5 |
| rescale cv, ns uncapped | 0.372 | 0.653 | 0.379 | 9 |

Rescaling **does** fix the degeneracy — modal mass 0.724 → 0.397, distinct scores 6 → 9 —
but buys **no accuracy**: rho drops 0.474 → 0.427 while pair_acc moves +0.02, and at gate
2.5 clamp and rescale give *identical* `recall_good` (1.000) and `drop_bad` (0.643).
The 4B's raw ordering within senior roles carries no signal against P2, so spreading it out
just spreads noise. **Keep the clamp** — simpler, already shipped, measurably no worse.

The 3.4 pile is therefore a *cosmetic* defect, not an accuracy one. Worth revisiting only
with real human labels.

### Candidate fixes (to test)
- **A. Lower the senior cap** to `cv<=2, ns<=3` → fixed point 2.4, below a 2.5 gate.
  Projected: drop_bad 0.722 → 1.000, recall_good 1.000 → 0.974 (costs the 1 good senior offer).
  Blunt, and close to "never apply to Senior-titled roles".
- **B. Rescale instead of clamp** — map the model's 1–5 into `[1, cap]` so ordering
  survives the cap. Needs the pre-cap dimensions, which are **not currently recorded**.
- **C. Eligibility gates** (clearance / work authorisation regex) — ❌ **DEAD END, tested.**
  Across all 115 JDs: 1 clearance mention (#56, P2 3.7 — not a bad offer) and **zero**
  work-authorisation mentions. #13's and #14's supposed clearance/work-rights blockers do
  not appear in their JD text at all — they were Phase 2 fabrications. No leverage; YAGNI.

**Next:** V4 = V3 + record `cv_match_raw`/`north_star_raw`, so cap policies A and B can be
evaluated offline from one run instead of one GPU run each. Only the 57 capped offers need
re-running — for uncapped offers raw == final, already known.

## Third hallucination surface: `top_strengths` (previously unaudited)

`top_strengths` asserts things about the **CV**, and it fabricates — worse, V1 slightly
regressed it:

| | V0 | V1 |
|---|---|---|
| strengths naming a technology | 118 | 163 |
| naming a tech **absent from cv.md** | 3 (2.5%) | 6 (3.7%) |

The failure shape is consistent: the model takes a technology the CV really does list and
appends a plausible neighbour it does not — a second cloud provider alongside the real one,
an extra framework in the same family, another language on the same runtime. The
neighbouring name is never in `cv.md`.

This is the most dangerous of the three surfaces: `top_strengths` is a claim the pipeline
makes *on the candidate's behalf*, and Phase 3 builds tailoring narrative from this kind of
signal. Fix is the same shape as `verifyGaps()` — drop any strength naming a technology
absent from `cv.md`. Purely post-hoc, so it needs no GPU run to evaluate.

## `confidence` is calibrated and completely unused

Mean |P1 − P2| by the model's self-reported confidence:

| confidence | V0 | V3 |
|-----------|-----|-----|
| High | 0.46 (n=67) | **0.45** (n=82) |
| Medium | 1.10 (n=27) | 0.73 (n=21) |
| Low | 1.53 (n=21) | **1.19** (n=12) |

Monotonic in both runs — the model knows when it is guessing, and nothing in the pipeline
reads the field. Obvious use: never let a *low-confidence* score drop an offer.

**Measured, gate 2.5:** 11 of the 19 drops are Low confidence. Passing them all through
recovers #103 (P2 3.8 — a low-latency trading-infrastructure role, P1's clearest single error),
#48 (3.4) and #62 (3.4), for 11 extra Phase 2 runs (~22 min).
But 8 of the 12 Low-confidence offers are genuinely bad and **none** reach P2 >= 4.0, so
`recall_good` is already 1.000 without it. **Verdict: optional insurance, not a default.**

## The gain survives deleting every suspect label
Re-ran the headline comparison with all 13 contaminated BAD rows removed
(#129 #13 #14 #16 #28 #30 #38 #55 #63 #7 #71 #73 #8), n=102:

| variant | rho | pair_acc | recall_good@2.5 |
|---------|-----|----------|-----------------|
| V0 | 0.267 | 0.603 | 0.949 |
| V1 | **0.585** | **0.724** | **1.000** |
| V4 | 0.580 | 0.721 | 0.974 |

Delta V0→V1 = +0.318 rho / +0.121 pair_acc — **identical** to the full-corpus +0.317/+0.125.
The improvement does not rest on the labels I showed to be wrong.

## The V0 → V1 gain is statistically real
Bootstrap over 4000 resamples of the 115 offers: rho delta **+0.317, 95% CI
[0.153, 0.498]**; 0/4000 resamples favour V0. Comfortably above the label-noise floor.

## V3's quote-grounding has a hole — it grounds only half a gap

A gap makes **two** claims: (1) *the JD asks for X*, (2) *the candidate lacks X*.
`jd_quote` proves only (1). Measured cost on offer **#39 "Junior Python Developer"**
(P2 4.7 — squarely a target role):

| | cv_match | score |
|---|---|---|
| V1 | 3 | 3.0 (passes gate 2.5) |
| V3/V4 | 2 | **2.4 (dropped)** |

V3 listed *"Experience with SQL and relational databases"* as a gap. It is quote-verified —
the JD really does ask for it — but the CV already lists several concrete databases that
satisfy it. The false
gap dragged `cv_match` down one point and pushed a good offer under the gate. This is why
V4 scores `recall_good` 0.974 at gate 2.5 where V1 scores 1.000.

**V5** closes it at generation time: the prompt now states both conditions explicitly, with
this exact failure as the worked example. A post-hoc filter cannot fix this — by the time
the gap is written, `cv_match` has already been decided in the same forward pass.

## No usable gold set exists in the repo
- `batch/labels.tsv` — 12 rows, ids `9xxx`, **zero overlap** with this corpus.
- `data/applications.md` — 136 `Evaluated`, only **3 SKIP + 2 Applied** carry a real human
  verdict. Not enough to label anything.

Written `label-these.tsv`: the 25 offers where P1 and P2 disagree most, which is where
human labels buy the most information. Top rows: #103 (P1 1.0 / P2 3.8), #13 (3.4 / 1.0),
#48 (1.0 / 3.4), #68 (3.1 / 5.0), #73 (1.0 / 2.9).
