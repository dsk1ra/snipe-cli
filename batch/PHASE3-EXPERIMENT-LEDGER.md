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

So a single run per variant is a valid A/B **only at temperature 0**, and every
benchmark below is run there. Whether production should also drop to 0 is itself
a variant — Phase 1's V1 made exactly that change and it was part of the win.

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
