# Phase 3 tailoring experiment ledger

**Defect under investigation:** the tailored CV drops experience roles. Across the
121 real `output/*/cv-content.json` produced before this work, **90.9% are missing
a role** — mean 1.09 companies against the 2 in `cv.md` — and **84.3% copy an
8-gram verbatim from the tailor prompt's own worked example**.

**Reference truth:** none, and none is possible. Phase 3 emits a document, not a
score, and PHASE1-EXPERIMENT-LEDGER.md already established that this repo has no
usable gold set. Every metric here is therefore label-free and checked against
source text with no model in the loop — the property that survived contamination
last time.

**Metrics** (`batch/tailor-harness.mjs`)

| name | meaning | want |
|------|---------|------|
| `role_retention` | mean (roles in output / roles in `cv.md`) | **1.0** |
| `all_roles_pct` | fraction of offers keeping every role | **1.0** |
| `metric_fab` | mean count of output numbers absent from `cv.md` | **0** |
| `fab_offers_pct` | fraction of offers with ≥1 fabricated number | **0** |
| `example_copy_pct` | fraction lifting an 8-gram from the worked example | **0** |
| `grounding` | mean token overlap of each output bullet with the best-matching `cv.md` bullet for that role | higher |

Sample: 24 offers, stratified by Phase 2 score (range 1.0–5.0, mean 3.58),
written once to `batch/bench/tailor/sample.tsv` and reused by every variant.
Harness: `run <label> --temperature 0` → `metrics <label>` → `compare <a> <b>`.

## Determinism — measured, not assumed

CLAUDE.md benchmark rule 2 was verified for Phase 2. It does **not** hold for
Phase 3 as shipped, because Phase 3 runs at `temperature 0.15`
(`local-pdf-offer.mjs:197`), not 0.

Same offer (#13), two runs each:

| temperature | result |
|-------------|--------|
| **0** | **byte-identical** `cv-content.json` |
| 0.15 (production) | differs |

Every benchmark below runs at 0. Whether production should also drop to 0 is
itself a variant — Phase 1's V1 made exactly that change and it was part of the
win.

> **Correction — that check was too weak, and the conclusion drawn from it was
> wrong.** Two consecutive runs of *one* offer on warm state prove reproducibility
> within a session; they say nothing about a 24-offer run where the model is
> swapped, reloaded and batched differently. V5 changed only post-processing —
> same prompt, same schema, same temperature — yet 1 of 24 offers came back with
> different *model* text (`30_unknown`, which gained a bullet). Greedy decoding is
> byte-identical only when the numerics are; GPU batch/split variation can flip a
> token regardless of temperature.
>
> So temperature 0 does **not** reduce the noise floor to zero here, and a single
> run per variant is not automatically a valid A/B. The floor is measured
> directly in *Noise floor* below, and any delta smaller than it is not evidence.

## Root cause (read before the variants)

Three independent signals all tell the 7B "one company", and they beat the prose
instruction at `local-tailor-prompt.md:41` that says *"ALL companies from the CV"*:

| location | what it shows |
|----------|---------------|
| `local-tailor-prompt.md:92` worked example | exactly **1** company ("Acme SaaS") |
| `local-tailor-prompt.md:113` output template | exactly **1** experience object — `projects` shows **3** |
| `local-pdf-offer.mjs:184` schema | `experience: minItems 1` — `projects` is `minItems 3` |

The brevity disclaimer at `:102` explicitly corrects the count for projects and
skills (*"produce 3–4 projects and 5–6 skill categories"*) and says nothing about
experience, so the single-entry example reads as the target.

`cv-select.mjs` is **not** implicated. Run directly against `cv.md` it returns
both roles, correctly reverse-chronological, with UBWIS's real numbers intact.
The loss happens downstream, in the model.

Secondary: `local-tailor-prompt.md:43` tells the model to preserve *"the CV's
numbers"* and then lists `10,000+ users`, `over 500 users`, `90%+ coverage` —
none of which appear in `cv.md`. The prompt is presenting fabricated metrics as
the candidate's own.

## Variants

All runs: 24 offers, `temperature 0`, `snipe-cv`, ~12 min/run.

| variant | role_retention | all_roles | invented_roles | metric_fab | example_copy | grounding |
|---------|----------------|-----------|----------------|------------|--------------|-----------|
| V0 baseline | 0.500 | 0.000 | 0.000 | 0.833 | 0.917 | 0.735 |
| V1 schema floor | 0.646 | 0.292 | **0.417** | 0.833 | 0.917 | 0.728 |
| V2 = V1 + named employers | 0.646 | 0.292 | 0.458 | 0.875 | 0.917 | 0.746 |
| **V3 = V2 + code reconcile** | **1.000** | **1.000** | **0.000** | 0.625 | 0.625 | 0.818 |
| V4 = V3 + no fake metrics | 1.000 | 1.000 | 0.000 | 0.625 | 0.625 | 0.835 |

### V0 — production as shipped
Every one of the 24 offers dropped a role. Not a tendency, a constant.
`grounding` 0.735 with `copied=2` on 20 of 24 offers: the model emits nearly the
same UBWIS block whatever the JD, so Phase 3 is barely tailoring experience at all.

### V1 — grammar floor on `experience.minItems`  ⚠ partial, and partly fake
Deriving `minItems` from the roles cv-select passed does force a second entry —
`all_roles_pct` 0.000 → 0.292 — but **`invented_roles` goes 0.000 → 0.417**. The
model satisfies the grammar with padding:

| second entry | offers |
|--------------|--------|
| genuine `Edinburgh Napier University` | 7 |
| `UBWIS` duplicated (one offer thrice) | 9 |
| a *project* promoted to a job (`Zero Trust…`, `MongoDB`, `Re:Link…`) | 8 |

**This is why retention counts roles traceable to `cv.md` and not array length.**
Under the length-based metric the same run scores 0.500 → 0.958 and reads as
solved. The grammar can only compel a *shape*; it cannot tell the model which
company belongs in it. `grounding` also drifts down (-0.007) and `mean_bullets`
inflates 2.96 → 4.38, both consistent with padding rather than recall.

Keep the floor — it is a necessary precondition — but it is not the fix.

### V2 — inject the exact employer list into the prompt  ❌ no effect
The prompt now receives the real employers derived from the CV cv-select passed
(`Edinburgh Napier University | UBWIS`), names both failure modes outright
("NEVER repeat a company", "a project is NOT a company"), shows two companies in
the worked example instead of one, and extends the brevity note to experience.

**`role_retention` and `all_roles_pct` did not move at all** — 0.646 and 0.292,
identical to V1 to three decimals. The *same 7 offers* succeed and the same 17
fail in the same way. `invented_roles` got slightly worse (0.417 → 0.458).

This is the Phase 1 V5 result again: *prompt instruction alone cannot fix this
model's CV lookup*. The 7B is not failing to understand the requirement — the
requirement was already stated at `:41` before this change, and stating it three
more ways with the literal answer supplied changes nothing. Worth knowing before
spending more prompt budget: **the remaining lever is structural, not wording.**

`grounding` +0.018 and `metric_fab` +0.042 are both within what a 24-offer
sample can resolve and neither is worth reading as an effect.

### V3 — reconcile experience in code  ✅ fixes the defect, ⚠ does not fix the model
`reconcileExperience()` in `cv-select.mjs`. Every real employer claims its best
unclaimed model entry (by name, else by bullet overlap ≥ 0.35); an employer that
claims nothing is backfilled from the CV; unclaimed entries are dropped.

All 24 offers now carry `Edinburgh Napier University | UBWIS`. `metric_fab`
0.833 → 0.625 and `example_copy_pct` 0.917 → 0.625 fall out of it: a backfilled
role cannot copy the worked example or invent a number.

**`role_retention = 1.000` is a guarantee of the code, not evidence about the
model, and must not be read as one.** The honest measurement is which entries
the model actually wrote:

| role | model-written | backfilled |
|------|---------------|------------|
| UBWIS | **24/24** | 0 |
| Edinburgh Napier University | **7/24** | 17 |

7/24 = 0.292, *identical* to V1's and V2's `all_roles_pct`. So the model still
omits the teaching role ~71% of the time and nothing in V1–V3 changed that; the
reconciler substitutes true, relevance-ranked CV text where it fails. The
outcome is a complete and accurate CV, not a better model.

The residual defect is therefore "17 of 24 CVs carry one untailored role", which
is a far smaller problem than "24 of 24 omit a job" but is not zero. The next
structural lever would be a per-role generation call rather than one call for the
whole document — the same shape as Phase 2's staged split — at the cost of a
second model call per offer. Not attempted here.

### V4 — remove the fabricated metrics from the prompt  ❌ no effect on fabrication
Both "preserve the CV's numbers" rules illustrated themselves with figures absent
from `cv.md`. 9 of 12 were fabricated, several being near-misses of real ones
(`50,000+ runs` vs the real `63,000+`, `90%+ coverage` vs `85%+`, `10,000+ users`
and `over 500 users` vs nothing). Removed rather than corrected — the CV is
already interpolated as `{{CV_CONTENT}}`, so a hardcoded list can only drift from
`cv.md` again on the next edit.

**`metric_fab` did not move: 0.625 → 0.625.** The offending numbers are
byte-identical across V3 and V4:

| invented number | offers |
|-----------------|--------|
| `100+` | 11 |
| `170+` | 3 |
| `150+` | 1 |

Nothing in the prompt says 100 any more, so the model is not copying the examples
— it is inventing a round number for "subscribers" outright, and `170+` is the
real 170 with a `+` appended, which overstates it. The prompt was a genuine
defect worth removing on its own merits, but it was **not the cause**.

That is now two prompt-level variants (V2, V4) with zero effect on this model.
Keep the change, stop reaching for the prompt.
