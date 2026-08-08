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

> **The file paths in this section were wrong.** `local-pdf-offer.mjs:28` prefers
> the gitignored `local-tailor-prompt.local.md` when it exists, and it has since
> 22 July. Everything below is true of the *shipped* prompt; the prompt that
> actually ran was worse. See *The V2/V4 runs are void*.

Three independent signals all tell the 7B "one company", and they beat the prose
instruction that says *"ALL companies from the CV"*:

| location | what it shows |
|----------|---------------|
| worked example | exactly **1** company — and in the **active** prompt that company is `Acme SaaS`, with a complete finished answer attached |
| output template | exactly **1** experience object — `projects` shows **3** |
| `local-pdf-offer.mjs:184` schema | `experience: minItems 1` — `projects` is `minItems 3` |

The brevity disclaimer at `:102` explicitly corrects the count for projects and
skills (*"produce 3–4 projects and 5–6 skill categories"*) and says nothing about
experience, so the single-entry example reads as the target.

`cv-select.mjs` is **not** implicated. Run directly against `cv.md` it returns
both roles, correctly reverse-chronological, with Acme SaaS's real numbers intact.
The loss happens downstream, in the model.

Secondary: the prompt tells the model to preserve *"the CV's numbers"* and then
lists figures absent from `cv.md`. In the **active** prompt that list contained
`100+ subscribers` — the exact figure that then appeared in 11 of 24 outputs.
The model was not inventing it; it was obeying.

## Variants

All runs: 24 offers, `temperature 0`, `snipe-cv`, ~12 min/run.

| variant | role_retention | all_roles | invented_roles | metric_fab | example_copy | grounding |
|---------|----------------|-----------|----------------|------------|--------------|-----------|
| V0 baseline | 0.500 | 0.000 | 0.000 | 0.833 | 0.917 | 0.735 |
| V1 schema floor | 0.646 | 0.292 | **0.792** | 0.833 | 0.917 | 0.728 |
| ~~V2 = V1 + named employers~~ | 0.646 | 0.292 | 0.792 | 0.875 | 0.917 | 0.746 |
| **V3 = V2 + code reconcile** | **1.000** | **1.000** | **0.000** | 0.625 | 0.625 | 0.818 |
| ~~V4 = V3 + no fake metrics~~ | 1.000 | 1.000 | 0.000 | 0.625 | 0.625 | 0.835 |
| **V5 = V4 + number verify** | **1.000** | **1.000** | **0.000** | **0.000** | 0.667 | 0.883 |
| V5 repeat (noise floor) | 1.000 | 1.000 | 0.000 | 0.000 | 0.625 | 0.903 |
| **V6 = V5 + the real prompt** | **1.000** | **1.000** | **0.000** | **0.000** | **0.292** | **0.900** |

~~Struck rows~~ never ran: see *The V2/V4 runs are void* below.

### V0 — production as shipped
Every one of the 24 offers dropped a role. Not a tendency, a constant.
`grounding` 0.735 with `copied=2` on 20 of 24 offers: the model emits nearly the
same Acme SaaS block whatever the JD, so Phase 3 is barely tailoring experience at all.

### V1 — grammar floor on `experience.minItems`  ⚠ partial, and partly fake
Deriving `minItems` from the roles cv-select passed does force a second entry —
`all_roles_pct` 0.000 → 0.292 — but **`invented_roles` goes 0.000 → 0.792**. The
model satisfies the grammar with padding:

| second entry | offers |
|--------------|--------|
| genuine `Northgate College` | 7 |
| `Acme SaaS` duplicated (one offer thrice) | 9 |
| a *project* promoted to a job (`Zero Trust…`, `MongoDB`, `Re:Link…`) | 8 |

> `invented_roles` for V1/V2 was first reported as 0.417/0.458. The metric
> counted a repeated employer as a *match*, so `Acme|Acme` scored as two healthy
> roles on that column. Corrected to count a second claim on an already-matched
> employer as junk, every stored run was re-scored from disk (no GPU needed) and
> only these two rows moved. `role_retention` always caught the duplicate.

**This is why retention counts roles traceable to `cv.md` and not array length.**
Under the length-based metric the same run scores 0.500 → 0.958 and reads as
solved. The grammar can only compel a *shape*; it cannot tell the model which
company belongs in it. `grounding` also drifts down (-0.007) and `mean_bullets`
inflates 2.96 → 4.38, both consistent with padding rather than recall.

Keep the floor — it is a necessary precondition — but it is not the fix.

### V2 — inject the exact employer list into the prompt  ❌ no effect
The prompt now receives the real employers derived from the CV cv-select passed
(`Northgate College | Acme SaaS`), names both failure modes outright
("NEVER repeat a company", "a project is NOT a company"), shows two companies in
the worked example instead of one, and extends the brevity note to experience.

**`role_retention` and `all_roles_pct` did not move at all** — 0.646 and 0.292,
identical to V1 to three decimals. The *same 7 offers* succeed and the same 17
fail in the same way. `invented_roles` is identical too (0.792 both).

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

All 24 offers now carry `Northgate College | Acme SaaS`. `metric_fab`
0.833 → 0.625 and `example_copy_pct` 0.917 → 0.625 fall out of it: a backfilled
role cannot copy the worked example or invent a number.

**`role_retention = 1.000` is a guarantee of the code, not evidence about the
model, and must not be read as one.** The honest measurement is which entries
the model actually wrote:

| role | model-written | backfilled |
|------|---------------|------------|
| Acme SaaS | **24/24** | 0 |
| Northgate College | **7/24** | 17 |

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

~~That is now two prompt-level variants (V2, V4) with zero effect on this model.
Keep the change, stop reaching for the prompt.~~ **Wrong — see below.**

## The V2/V4 runs are void

`local-pdf-offer.mjs:28` resolves the prompt as
`existsSync(local-tailor-prompt.local.md) ? local : local-tailor-prompt.md`.
The `.local.md` override has existed since 22 July. **V2 and V4 both edited the
shipped file, which the pipeline never opened**, so neither change reached the
model and their 0.000 deltas measured nothing at all.

`exampleShingles()` in the harness had the same bug, so `example_copy_pct` was
computed against a prompt that never ran.

The conclusion drawn from those two runs — *"prompt instruction cannot fix this
model, the lever is structural"* — was an artefact of the mistake and is
withdrawn. V6 tests the change properly and reverses it.

### V6 — V5 plus the corrections applied to the prompt that actually runs  ✅
The active prompt now carries the V2 rules and `{{EXPERIENCE_COMPANIES}}`; its
worked example uses placeholder employers instead of a finished `Acme SaaS` answer;
`100+ subscribers` is corrected to `170 members` and `970% growth` (a role no
longer in `cv.md`) is gone.

| | V5 | V6 | noise floor |
|---|---|---|---|
| second role **model-written** | 7/24 | **24/24** | — |
| `example_copy_pct` | 0.667 | **0.292** | ±0.042 |
| `grounding` | 0.883 | 0.900 | ±0.020 |
| `mean_bullets` | 4.83 | 4.00 | ±0.12 |
| verbatim-CV bullets (code intervening) | 50/116, all 24 offers | **1/96, 1 offer** | — |

**The prompt was the lever.** With the worked example no longer handing the model
a finished single-company answer, it produces both roles unaided on every offer,
and the `example_copy` drop is ~9x the noise floor. The code guards did not
become useless — they became a safety net, firing on 1 offer in 24 instead of
carrying all 24.

The honest ordering of causes is therefore: the prompt's worked example was the
root cause of both the dropped role and the `100+` figure; the schema floor,
reconciler and number verifier are defence in depth that made the output correct
while the root cause was still present.

### V5 — verify bullet numbers against the CV in code  ✅ SHIPPED
`verifyBulletNumbers()` reverts any bullet asserting a figure `cv.md` does not
state to the CV bullet it most resembles. Reverted rather than dropped, so the
role keeps its depth and only the offending bullet loses its rewrite.

**`metric_fab` 0.625 → 0.000.** Not a single invented figure survives in 24
offers, and it holds on the repeat run. `grounding` 0.835 → 0.883 follows: a
reverted bullet is CV text by construction. 16 of 24 offers had at least one
bullet reverted, which is a direct measure of how often the 7B invents a number.

## Noise floor — measured, after the first estimate was wrong

V5 was re-run unchanged. **21 of 24 offers came back byte-identical; 3 differed**
(`159_openai`, `30_unknown`, `39_an-innovative…`), so ~12.5% of offers vary
between identical runs at temperature 0.

| metric | floor | reading |
|--------|-------|---------|
| `role_retention`, `all_roles_pct`, `invented_roles`, `metric_fab` | **0.000** | code-guaranteed, not model behaviour |
| `grounding` | **±0.020** | |
| `example_copy_pct` | **±0.042** | one offer of 24 |
| `mean_bullets` | ±0.120 | |

Applying it to the deltas already recorded:

| delta | size | verdict |
|-------|------|---------|
| V0→V5 `metric_fab` −0.833 | code guarantee | **real** |
| V0→V5 `all_roles_pct` +1.000 | code guarantee | **real** |
| V0→V5 `grounding` +0.148 | 7× floor | **real** |
| V3→V4 `grounding` +0.017 | **under** floor | not evidence |
| V4→V5 `example_copy` +0.042 | **at** floor | not evidence |

So V4's verdict ("no effect") is unchanged and now properly supported rather
than assumed. V1→V2's `role_retention` delta of exactly 0.000 is not merely an
aggregate match: the *same 7 offers* succeeded in both runs, which is a stronger
statement than the means agreeing, though with 12.5% of offers varying it is
evidence rather than proof.

**The four headline results rest on deterministic code paths, so the floor does
not touch them.** Every interpretive claim about model behaviour has been
re-checked against it.

## Not attempted

- **Shipping `temperature 0`.** Production stays at 0.15; the benchmark flag only
  overrides it. Temperature 0 is not fully reproducible here anyway (above), so
  dropping it would need its own A/B on quality, not determinism.
- **Per-role generation.** No longer indicated. V6 has the model writing both
  roles unaided 24/24, so the residual this was meant to address is gone.
- **Re-running Phase 2 for the sample.** The benchmark's input reports were
  generated under an older `cv.md`. Constant across V0–V5 so every delta holds,
  but the absolute `grounding`/`metric_fab` figures are measured against slightly
  aged tailoring briefs.

## V7 — content floors (bullets, projects, description length)

Every content guard in Phase 3 was a ceiling: the schema capped counts,
`clampContent` sliced, the density ladder trimmed. Nothing had a floor, so the
model's under-delivery went straight to the page. Twelve consecutive production
CVs shipped 2–6 experience bullets against the CV's 9, 3 projects on every single
run, and project descriptions with a median of 17 words against a prompt asking
for 35–55 — **0 of 36 in band**. Page 2 of the rendered PDF ran 5–15 lines of ~50.

Four floors, all derived from the CV handed to the model rather than hardcoded
(demanding 3 bullets from a 1-bullet role is an instruction to invent):

| change | where |
|---|---|
| `minItems` on `experience[].bullets` and `projects` | `contentFloors()`, `local-pdf-offer.mjs` |
| top a part-filled role back up from its CV bullets | `topUpBullets()`, `cv-select.mjs` |
| pad a short description from that project's CV clauses | `padProjectDescriptions()`, `cv-select.mjs` |
| honours classification no longer split off the degree | `buildEducationHtml()`, `fill-cv-template.mjs` |

12 offers, temperature 0, paired against a run of `HEAD` made the same day.

| metric | baseline | V7 | delta | floor | verdict |
|---|---|---|---|---|---|
| `ats_coverage` | 0.507 | 0.652 | **+0.145** | ±0.002 | real, 70× |
| `mean_bullets` | 4.92 | 8.00 | **+3.08** | ±0.120 | real |
| `selection_regret` | 0.071 | 0.095 | +0.024 | ±0.001 | real, accepted (below) |
| `grounding` | 0.960 | 0.981 | +0.021 | ±0.020 | **not claimable** |
| `metric_fab`, `product_fab`, `invented_roles`, `num_lost` | 0 | 0 | 0 | 0 | held |
| `role_retention`, `num_retention`, `all_roles_pct` | 1.000 | 1.000 | 0 | 0 | held |

Structurally: bullets `2,2,4,4,5,6,6,6,6,6,6,6` → `8×12`; projects `3×12` →
`4×12`; description median 16 → 48 words with 0 under the floor. Rendered page 2
went 15 → 36–44 lines, still inside the 2-page cap.

**`selection_regret` +0.024 is real and accepted, not a defect.** It is identical
(0.095, 0.094, 0.095) across every treatment run despite the padding and project
fixes, which is provable rather than argued: regret scores only
`exp.flatMap(e => e.bullets)`. A per-role floor is deliberately not a
globally-optimal pick — it spends slots guaranteeing every employer has substance,
the same trade already made for whole roles by pinning `role_retention` at 1.

### What the metrics could not see

Four defects were caught only by reading output (rule 7), and no metric here moves
on any of them:

- a clause splitter breaking on a `;` **inside** a parenthetical, shipping
  `"…(API gateway, hashing, and manifest-signing services."` — unbalanced bracket,
  truncated mid-aside;
- the length floor applied *before* `stripFabricatedProducts`, whose clause surgery
  then cut descriptions back under it — the cause of every short description in
  V7's first two runs. The floor is now asserted **last**, which is safe because
  padded text is verbatim `cv.md` and passes both guards by construction;
- `r.bullets.slice(0,2).join(' ')` in `remapProjectNames`' backfill welding two
  sentences with no separator (`"…zero cloud LLM calls Cut fabricated job
  requirements ~9x…"`). Pre-existing; raising `minProjects` to 4 made it frequent;
- the model emitting a fragment (`"Built a high-performance."`) that padding then
  cemented at the head of otherwise clean prose. A description whose opening clause
  is under 6 words is now discarded and rebuilt from the CV.

### Not fixed: the figure-conflation class

`"serving 4 GitHub Actions CI pipelines"` — a real CV figure hung on a noun it does
not measure. `verifyProjectFigures` cannot catch it: the figure *is* in the
project's own entry, so it is a wrong verb, not a wrong number.

Relaxing the prompt's `Each description MUST include at least one concrete metric`
mandate — the thing that pressures the model to weld a figure into any sentence —
**did not work**: 2/36 (5.6%) baseline → 3/48 (6.3%) after. Consistent with V4's
finding that the prompt-side fix for a number problem measures zero effect. The
relaxed wording was kept because it is more accurate guidance and costs nothing,
but it is not a fix. A real one is deterministic or nothing.

## V8 — the summary had no fabrication guards at all

Experience bullets have `verifyBulletNumbers` / `verifyBulletFigures` /
`verifyBulletProducts`. Project blurbs have `verifyProjectFigures` and
`stripFabricatedProducts`. The summary — the first block anyone reads — had a
tenure strip that missed ranges, and nothing else. One shipped summary carried
three false claims at once:

> Full-Stack Engineer with **1-3 years of real production experience** … a live
> subscription platform serving **150+ users** … **Russell Group graduate** with
> First Class Honours

`cv.md` states no tenure, says **170** paying members, and the university is
post-1992 — the model inferred "Russell Group" from the city in its name. Every
harness metric read 0 throughout, because `metric_fab` only inspects experience
bullets. Measured across the corpus: **3 of 12 summaries carried a fabrication**,
stable across four independent runs.

| guard | catches |
|---|---|
| `verifySummaryFigures` (`cv-select.mjs`) | a figure `cv.md` does not state |
| `TENURE` extended to ranges + multi-word qualifiers | `1-3 years`, `2 to 4 years` |
| `stripFabricatedCredentials` (`summary-stage.mjs`) | an institution, degree or certification the CV never claims |
| `stripFabricatedProducts` wired at the T2 site | the fallback path — T2's comment claimed the summary was product-guarded, but that only happened inside `generateSummary`, which is in a try/catch |

**Result: 3/12 → 0/12**, verified on a fresh run. Nothing else moved:
`grounding` 0.981 unchanged, `mean_bullets` 8.00, `selection_regret` 0.095,
`ats_coverage` −0.003 (floor ±0.002).

### Deflate before cutting

The first version deleted any clause holding an unsupported figure, which cost
real evidence: `"a GDPR-compliant membership platform with 170+ paying users"`
is a true, CV-specific claim where only the `+` is false — the exact pattern
`verifyBulletNumbers` measured on 3 of 24 offers. Correcting the figure in place
instead recovered every lost word on 2 of the 3 repaired summaries (65w→65w,
55w→55w) and, as a side effect, removed the one grammar artifact clause surgery
had produced: the orphaned `"building secure,"` only existed because the clause
was being deleted. Mean summary length ends at 59.6w against 60.9w before, and
**0 of 12 fall under the 50-word floor**, so the boilerplate closer does not fire
any more often than it already did.

### `summary_cv_fit` moved against a correct change

−0.023 before the deflate, −0.010 after, against a ±0.004 floor. It penalised
removing `"…platform with 170+ paying users"` because that phrasing is *close to
the CV* — a metric scoring CV-similarity necessarily prefers a near-miss
falsehood to its absence. This is rule 7 from CLAUDE.md reproducing exactly:
the summary metrics can be satisfied by output that is wrong. Recorded, not
chased. The correct instrument is a summary-level fabrication count, which does
not exist yet — `metric_fab` still reads only experience bullets, and reported a
clean 0 through every run where a CV claimed Russell Group membership.

### Two shared-code fixes this surfaced

- `NUMERIC` now ignores digits preceded by a letter. `40` was being extracted
  from **"L40 Engineer"** — Monzo's internal job level — and read as an invented
  figure. The same false positive was live in the bullet and project guards.
- `stripUnsupportedClauses` re-capitalises a rebuilt sentence; dropping a leading
  clause had been shipping `"…across Python, React, and Next.js. strong
  fundamentals,…"`. Affected the product guard equally.
