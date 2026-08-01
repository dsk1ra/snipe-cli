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

_Results below are filled in as each run completes._
